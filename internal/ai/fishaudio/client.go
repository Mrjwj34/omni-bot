package fishaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"omni-bot/internal/ai"
)

const (
	Protocol      = "fishaudio-tts"
	DefaultFormat = "mp3"
)

type Options struct {
	HTTPClient    *http.Client
	DefaultFormat string
}

type Option func(*Options)

func WithHTTPClient(c *http.Client) Option {
	return func(o *Options) { o.HTTPClient = c }
}

func WithDefaultFormat(format string) Option {
	return func(o *Options) { o.DefaultFormat = format }
}

// Client implements ai.SpeechModel against Fish Audio /tts, passing model via HTTP header.
type Client struct {
	httpClient    *http.Client
	endpoint      string
	provider      string
	modelID       string
	defaultVoice  string
	defaultFormat string
	keys          *ai.KeyRotator
	headers       map[string]string
}

func RegisterFactory(reg *ai.Registry, opts ...Option) {
	reg.RegisterSpeechFactory(Protocol, func(rm ai.ResolvedModel) (ai.SpeechModel, error) {
		return New(rm, opts...)
	})
}

func New(resolved ai.ResolvedModel, opts ...Option) (*Client, error) {
	o := Options{DefaultFormat: DefaultFormat}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	var defaultVoice string
	if resolved.Model.DefaultParams != nil {
		if f, ok := resolved.Model.DefaultParams["format"].(string); ok && strings.TrimSpace(f) != "" {
			o.DefaultFormat = strings.TrimSpace(f)
		}
		if v, ok := resolved.Model.DefaultParams["reference_id"].(string); ok {
			defaultVoice = strings.TrimSpace(v)
		}
	}
	if strings.TrimSpace(o.DefaultFormat) == "" {
		o.DefaultFormat = DefaultFormat
	}

	if strings.TrimSpace(resolved.Provider.BaseURL) == "" {
		return nil, &ai.Error{Kind: ai.ErrInvalidArg, Provider: resolved.Provider.ID, Message: "fishaudio tts requires BaseURL"}
	}
	if strings.TrimSpace(resolved.Model.ModelID) == "" {
		return nil, &ai.Error{Kind: ai.ErrInvalidArg, Provider: resolved.Provider.ID, Message: "fishaudio tts requires ModelID"}
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
	if !strings.HasSuffix(endpoint, "/tts") {
		endpoint += "/tts"
	}

	return &Client{
		httpClient:    hc,
		endpoint:      endpoint,
		provider:      resolved.Provider.ID,
		modelID:       resolved.Model.ModelID,
		defaultVoice:  defaultVoice,
		defaultFormat: o.DefaultFormat,
		keys:          keys,
		headers:       resolved.Provider.Headers,
	}, nil
}

type ttsRequestBody struct {
	Text        string `json:"text"`
	Format      string `json:"format"`
	ReferenceID string `json:"reference_id,omitempty"`
}

func (c *Client) Synthesize(ctx context.Context, req ai.SpeechRequest) (ai.SpeechResponse, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return ai.SpeechResponse{}, &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: c.provider,
			Message:  "speech text is required",
		}
	}

	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = c.defaultFormat
	}
	voiceID := strings.TrimSpace(req.VoiceID)
	if voiceID == "" {
		voiceID = c.defaultVoice
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.modelID
	}

	key, err := c.keys.Next()
	if err != nil {
		return ai.SpeechResponse{}, &ai.Error{Kind: ai.ErrAuth, Provider: c.provider, Message: "no API keys configured", Err: err}
	}

	payload, err := json.Marshal(ttsRequestBody{
		Text:        text,
		Format:      format,
		ReferenceID: voiceID,
	})
	if err != nil {
		return ai.SpeechResponse{}, &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: "marshal tts request", Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return ai.SpeechResponse{}, &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: "build tts request", Err: err}
	}
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)
	// Fish Audio expects the TTS model selector in the HTTP "model" header, not the JSON body.
	httpReq.Header.Set("model", model)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return ai.SpeechResponse{}, classifyTransportError(c.provider, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ai.SpeechResponse{}, classifyTransportError(c.provider, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 400 {
			msg = msg[:400] + "..."
		}
		return ai.SpeechResponse{}, &ai.Error{
			Kind:       ai.ClassifyHTTPStatus(resp.StatusCode, msg),
			StatusCode: resp.StatusCode,
			Provider:   c.provider,
			Message:    msg,
			RetryAfter: parseRetryAfter(resp),
		}
	}

	mimeType := normalizeContentType(resp.Header.Get("Content-Type"), format)
	return ai.SpeechResponse{
		Audio:    body,
		MIMEType: mimeType,
	}, nil
}

func normalizeContentType(header, format string) string {
	if header != "" {
		if mediaType, _, err := mime.ParseMediaType(header); err == nil && mediaType != "" && mediaType != "application/octet-stream" {
			return mediaType
		}
	}
	switch strings.ToLower(format) {
	case "opus":
		return "audio/opus"
	case "wav":
		return "audio/wav"
	default:
		return "audio/mpeg"
	}
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
