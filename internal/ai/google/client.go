package google

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/genai"

	"omni-bot/internal/ai"
)

const (
	ProtocolInteractions = "gemini-interactions"
	ProtocolDecisions    = "gemini-decisions"

	defaultBaseURL          = "https://generativelanguage.googleapis.com"
	defaultAPIVersion       = "v1beta"
	defaultInlineMaxBytes   = 4 << 20
	defaultFileCacheTTL     = 44 * time.Hour
	defaultFilePollInterval = time.Second
	defaultFilePollTimeout  = 2 * time.Minute
)

// Options configures optional dependencies and tuning parameters for Google protocol adapters.
type Options struct {
	HTTPClient       *http.Client
	BlobReader       ai.BlobReader
	InlineMaxBytes   int64
	FileCacheTTL     time.Duration
	FilePollInterval time.Duration
	FilePollTimeout  time.Duration
	Now              func() time.Time
}

type Option func(*Options)

func WithHTTPClient(c *http.Client) Option {
	return func(o *Options) { o.HTTPClient = c }
}

func WithBlobReader(r ai.BlobReader) Option {
	return func(o *Options) { o.BlobReader = r }
}

func WithInlineMaxBytes(n int64) Option {
	return func(o *Options) { o.InlineMaxBytes = n }
}

func WithFileCacheTTL(d time.Duration) Option {
	return func(o *Options) { o.FileCacheTTL = d }
}

func WithFilePollConfig(interval, timeout time.Duration) Option {
	return func(o *Options) {
		o.FilePollInterval = interval
		o.FilePollTimeout = timeout
	}
}

func WithClock(now func() time.Time) Option {
	return func(o *Options) { o.Now = now }
}

func resolveOptions(opts []Option) Options {
	o := Options{
		InlineMaxBytes:   defaultInlineMaxBytes,
		FileCacheTTL:     defaultFileCacheTTL,
		FilePollInterval: defaultFilePollInterval,
		FilePollTimeout:  defaultFilePollTimeout,
		Now:              time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.InlineMaxBytes <= 0 {
		o.InlineMaxBytes = defaultInlineMaxBytes
	}
	if o.FileCacheTTL <= 0 {
		o.FileCacheTTL = defaultFileCacheTTL
	}
	if o.FilePollInterval <= 0 {
		o.FilePollInterval = defaultFilePollInterval
	}
	if o.FilePollTimeout <= 0 {
		o.FilePollTimeout = defaultFilePollTimeout
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// RegisterFactories registers the Gemini Interactions and Decisions protocol factories on reg.
func RegisterFactories(reg *ai.Registry, opts ...Option) {
	reg.RegisterLanguageFactory(ProtocolInteractions, func(rm ai.ResolvedModel) (ai.LanguageModel, error) {
		return NewLanguageModel(context.Background(), rm, opts...)
	})
	reg.RegisterDecisionFactory(ProtocolDecisions, func(rm ai.ResolvedModel) (ai.DecisionModel, error) {
		return NewDecisionModel(context.Background(), rm, opts...)
	})
}

type rotatingTransport struct {
	base     http.RoundTripper
	provider string
	keys     *ai.KeyRotator
	headers  map[string]string
}

func (t *rotatingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	key, err := t.keys.Next()
	if err != nil {
		return nil, &ai.Error{
			Kind:     ai.ErrAuth,
			Provider: t.provider,
			Message:  "no API keys configured",
			Err:      err,
		}
	}
	cloned := req.Clone(req.Context())
	for k, v := range t.headers {
		cloned.Header.Set(k, v)
	}
	cloned.Header.Set("x-goog-api-key", key)
	return t.base.RoundTrip(cloned)
}

func buildGenAIClient(ctx context.Context, resolved ai.ResolvedModel, opts Options) (*genai.Client, error) {
	keys := resolved.Keys
	if keys == nil {
		keys = ai.NewKeyRotator(resolved.Provider.APIKeys)
	}
	if keys.Len() == 0 {
		return nil, &ai.Error{
			Kind:     ai.ErrAuth,
			Provider: resolved.Provider.ID,
			Message:  "at least one API key is required",
		}
	}

	httpClient, err := buildHTTPClient(resolved.Provider, keys, opts.HTTPClient)
	if err != nil {
		return nil, err
	}

	baseURL := strings.TrimSpace(resolved.Provider.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	// Provide a placeholder key to satisfy genai.NewClient validation;
	// rotatingTransport replaces x-goog-api-key per outgoing request.
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:     "rotated-per-request",
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: httpClient,
		HTTPOptions: genai.HTTPOptions{
			BaseURL:    baseURL,
			APIVersion: defaultAPIVersion,
		},
	})
	if err != nil {
		return nil, &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: resolved.Provider.ID,
			Message:  fmt.Sprintf("init genai client: %v", err),
			Err:      err,
		}
	}
	return client, nil
}

func buildHTTPClient(cfg ai.ProviderConfig, keys *ai.KeyRotator, custom *http.Client) (*http.Client, error) {
	var baseTransport http.RoundTripper
	timeout := cfg.Timeout

	if custom != nil {
		baseTransport = custom.Transport
		if timeout == 0 {
			timeout = custom.Timeout
		}
	}
	if baseTransport == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		if cfg.ProxyURL != "" {
			u, err := url.Parse(cfg.ProxyURL)
			if err != nil {
				return nil, &ai.Error{
					Kind:     ai.ErrInvalidArg,
					Provider: cfg.ID,
					Message:  fmt.Sprintf("invalid proxy url: %v", err),
					Err:      err,
				}
			}
			tr.Proxy = http.ProxyURL(u)
		}
		baseTransport = tr
	}

	return &http.Client{
		Timeout: timeout,
		Transport: &rotatingTransport{
			base:     baseTransport,
			provider: cfg.ID,
			keys:     keys,
			headers:  cfg.Headers,
		},
	}, nil
}
