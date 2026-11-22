package openaiembed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"omni-bot/internal/ai"
)

const (
	Protocol          = "openai-embedding"
	DefaultDimensions = 1024
	DefaultBatchSize  = 32
)

type Options struct {
	HTTPClient        *http.Client
	DefaultDimensions int
	MaxBatchSize      int
}

type Option func(*Options)

func WithHTTPClient(c *http.Client) Option {
	return func(o *Options) { o.HTTPClient = c }
}

func WithDefaultDimensions(dim int) Option {
	return func(o *Options) { o.DefaultDimensions = dim }
}

func WithMaxBatchSize(n int) Option {
	return func(o *Options) { o.MaxBatchSize = n }
}

// Client implements ai.EmbeddingModel against OpenAI-compatible /embeddings endpoints.
type Client struct {
	httpClient   *http.Client
	endpoint     string
	provider     string
	modelID      string
	keys         *ai.KeyRotator
	headers      map[string]string
	defaultDims  int
	maxBatchSize int
}

func RegisterFactory(reg *ai.Registry, opts ...Option) {
	reg.RegisterEmbeddingFactory(Protocol, func(rm ai.ResolvedModel) (ai.EmbeddingModel, error) {
		return New(rm, opts...)
	})
}

func New(resolved ai.ResolvedModel, opts ...Option) (*Client, error) {
	o := Options{
		DefaultDimensions: DefaultDimensions,
		MaxBatchSize:      DefaultBatchSize,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if resolved.Model.DefaultParams != nil {
		if d := intParam(resolved.Model.DefaultParams, "dimensions"); d > 0 {
			o.DefaultDimensions = d
		}
		if b := intParam(resolved.Model.DefaultParams, "max_batch_size"); b > 0 {
			o.MaxBatchSize = b
		}
	}
	if o.DefaultDimensions <= 0 {
		o.DefaultDimensions = DefaultDimensions
	}
	if o.MaxBatchSize <= 0 || o.MaxBatchSize > DefaultBatchSize {
		if o.MaxBatchSize <= 0 {
			o.MaxBatchSize = DefaultBatchSize
		}
	}

	if strings.TrimSpace(resolved.Provider.BaseURL) == "" {
		return nil, &ai.Error{Kind: ai.ErrInvalidArg, Provider: resolved.Provider.ID, Message: "openai embedding requires BaseURL"}
	}
	if strings.TrimSpace(resolved.Model.ModelID) == "" {
		return nil, &ai.Error{Kind: ai.ErrInvalidArg, Provider: resolved.Provider.ID, Message: "openai embedding requires ModelID"}
	}
	keys := resolved.Keys
	if keys == nil {
		keys = ai.NewKeyRotator(resolved.Provider.APIKeys)
	}
	if keys.Len() == 0 {
		return nil, &ai.Error{Kind: ai.ErrAuth, Provider: resolved.Provider.ID, Message: "at least one API key is required"}
	}

	hc, err := buildHTTPClient(resolved.Provider, o.HTTPClient)
	if err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(strings.TrimSpace(resolved.Provider.BaseURL), "/")
	if !strings.HasSuffix(endpoint, "/embeddings") {
		endpoint += "/embeddings"
	}

	return &Client{
		httpClient:   hc,
		endpoint:     endpoint,
		provider:     resolved.Provider.ID,
		modelID:      resolved.Model.ModelID,
		keys:         keys,
		headers:      resolved.Provider.Headers,
		defaultDims:  o.DefaultDimensions,
		maxBatchSize: o.MaxBatchSize,
	}, nil
}

type embedRequestBody struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embedResponseBody struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage *struct {
		PromptTokens *int `json:"prompt_tokens"`
		TotalTokens  *int `json:"total_tokens"`
	} `json:"usage"`
}

func (c *Client) Embed(ctx context.Context, req ai.EmbedRequest) (ai.EmbedResponse, error) {
	if len(req.Texts) == 0 {
		return ai.EmbedResponse{}, &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: c.provider,
			Message:  "at least one text is required for embedding",
		}
	}

	dims := req.Dimensions
	if dims <= 0 {
		dims = c.defaultDims
	}

	allVectors := make([][]float32, len(req.Texts))
	var (
		hasInputTokens bool
		inputTokens    int
		hasTotalTokens bool
		totalTokens    int
	)

	for start := 0; start < len(req.Texts); start += c.maxBatchSize {
		end := min(start+c.maxBatchSize, len(req.Texts))
		chunk := req.Texts[start:end]

		vecs, usage, err := c.embedChunk(ctx, chunk, dims)
		if err != nil {
			return ai.EmbedResponse{}, err
		}
		copy(allVectors[start:end], vecs)
		if usage.InputTokens != nil {
			hasInputTokens = true
			inputTokens += *usage.InputTokens
		}
		if usage.TotalTokens != nil {
			hasTotalTokens = true
			totalTokens += *usage.TotalTokens
		}
	}

	var outUsage ai.Usage
	if hasInputTokens {
		outUsage.InputTokens = ai.IntPtr(inputTokens)
	}
	if hasTotalTokens {
		outUsage.TotalTokens = ai.IntPtr(totalTokens)
	}
	return ai.EmbedResponse{
		Vectors: allVectors,
		Usage:   outUsage,
	}, nil
}

func (c *Client) embedChunk(ctx context.Context, texts []string, dims int) ([][]float32, ai.Usage, error) {
	key, err := c.keys.Next()
	if err != nil {
		return nil, ai.Usage{}, &ai.Error{Kind: ai.ErrAuth, Provider: c.provider, Message: "no API keys configured", Err: err}
	}

	payload, err := json.Marshal(embedRequestBody{
		Model:      c.modelID,
		Input:      texts,
		Dimensions: dims,
	})
	if err != nil {
		return nil, ai.Usage{}, &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: "marshal embedding request", Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, ai.Usage{}, &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: "build embedding request", Err: err}
	}
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, ai.Usage{}, classifyTransportError(c.provider, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, ai.Usage{}, classifyTransportError(c.provider, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 400 {
			msg = msg[:400] + "..."
		}
		return nil, ai.Usage{}, &ai.Error{
			Kind:       ai.ClassifyHTTPStatus(resp.StatusCode, msg),
			StatusCode: resp.StatusCode,
			Provider:   c.provider,
			Message:    msg,
			RetryAfter: parseRetryAfter(resp),
		}
	}

	var parsed embedResponseBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, ai.Usage{}, &ai.Error{
			Kind:     ai.ErrTransient,
			Provider: c.provider,
			Message:  fmt.Sprintf("decode embedding response: %v", err),
			Err:      err,
		}
	}
	if len(parsed.Data) != len(texts) {
		return nil, ai.Usage{}, &ai.Error{
			Kind:     ai.ErrTransient,
			Provider: c.provider,
			Message:  fmt.Sprintf("embedding count mismatch: got %d, want %d", len(parsed.Data), len(texts)),
		}
	}

	ordered := make([][]float32, len(texts))
	seen := make([]bool, len(texts))
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= len(texts) || seen[item.Index] {
			return nil, ai.Usage{}, &ai.Error{
				Kind:     ai.ErrTransient,
				Provider: c.provider,
				Message:  fmt.Sprintf("invalid or duplicate embedding index %d", item.Index),
			}
		}
		seen[item.Index] = true
		ordered[item.Index] = L2Normalize(item.Embedding)
	}

	var usage ai.Usage
	if parsed.Usage != nil {
		if parsed.Usage.PromptTokens != nil {
			usage.InputTokens = ai.IntPtr(*parsed.Usage.PromptTokens)
		}
		if parsed.Usage.TotalTokens != nil {
			usage.TotalTokens = ai.IntPtr(*parsed.Usage.TotalTokens)
		}
	}
	return ordered, usage, nil
}

// L2Normalize returns a unit-length copy of vec (or a copy of a zero vector unchanged).
func L2Normalize(vec []float32) []float32 {
	out := make([]float32, len(vec))
	var sum float64
	for _, v := range vec {
		f := float64(v)
		sum += f * f
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		copy(out, vec)
		return out
	}
	for i, v := range vec {
		out[i] = float32(float64(v) / norm)
	}
	return out
}

func buildHTTPClient(cfg ai.ProviderConfig, custom *http.Client) (*http.Client, error) {
	if custom != nil {
		return custom, nil
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.ProxyURL != "" {
		u, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, &ai.Error{Kind: ai.ErrInvalidArg, Provider: cfg.ID, Message: fmt.Sprintf("invalid proxy url: %v", err), Err: err}
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{
		Timeout:   cfg.Timeout,
		Transport: tr,
	}, nil
}

func classifyTransportError(provider string, err error) error {
	if errors.Is(err, context.Canceled) {
		return &ai.Error{Kind: ai.ErrUnknown, Provider: provider, Message: "request canceled", Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ai.Error{Kind: ai.ErrTransient, Provider: provider, Message: "request timed out", Err: err}
	}
	kind := ai.ErrorKindOf(err)
	if kind == "" {
		kind = ai.ErrTransient
	}
	return &ai.Error{Kind: kind, Provider: provider, Message: err.Error(), Err: err}
}

func parseRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

func intParam(params map[string]any, key string) int {
	switch v := params[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return 0
	}
}
