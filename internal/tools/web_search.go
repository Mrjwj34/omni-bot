package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"omni-bot/internal/ai"
)

const (
	defaultDeepSeekBaseURL = "https://api.deepseek.com"
	defaultSearchTimeout   = 15 * time.Second
	maxSearchRespBytes     = 2 << 20
)

// WebSearchConfig configures the DeepSeek /v1/tools/search built-in tool.
type WebSearchConfig struct {
	BaseURL           string
	APIKey            string
	HTTPClient        *http.Client
	DefaultMaxResults int
}

// SearchHit represents one web search result returned by DeepSeek search.
type SearchHit struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	PublishedAt string `json:"published_at,omitempty"`
}

type webSearchTool struct {
	baseURL           string
	apiKey            string
	client            *http.Client
	defaultMaxResults int
}

// NewWebSearchTool creates the built-in web_search tool targeting DeepSeek /v1/tools/search.
func NewWebSearchTool(cfg WebSearchConfig) Tool {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = defaultDeepSeekBaseURL
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultSearchTimeout}
	}
	maxRes := cfg.DefaultMaxResults
	if maxRes <= 0 {
		maxRes = 5
	}
	return &webSearchTool{
		baseURL:           base,
		apiKey:            strings.TrimSpace(cfg.APIKey),
		client:            client,
		defaultMaxResults: maxRes,
	}
}

func (t *webSearchTool) Definition() ai.ToolDef {
	return ai.ToolDef{
		Name:        "web_search",
		Description: "Search the web for recent facts, news, or references via the DeepSeek search API.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query string.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum number of results to return (default 5, max 10).",
				},
			},
			"required": []string{"query"},
		},
	}
}

type webSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
	Limit      int    `json:"limit"`
}

type deepSeekSearchPayload struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

type deepSeekSearchItem struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	Content     string `json:"content"`
	PublishedAt string `json:"published_at"`
}

type deepSeekSearchResponse struct {
	Results []deepSeekSearchItem `json:"results"`
	Data    []deepSeekSearchItem `json:"data"`
}

func (t *webSearchTool) Execute(ctx context.Context, argsJSON []byte) (Result, error) {
	var args webSearchArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return Result{}, fmt.Errorf("decode web_search args: %w", err)
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return Result{}, errors.New("web_search: query is required")
	}
	maxRes := args.MaxResults
	if maxRes <= 0 {
		maxRes = args.Limit
	}
	if maxRes <= 0 {
		maxRes = t.defaultMaxResults
	}
	if maxRes > 10 {
		maxRes = 10
	}

	reqBody, err := json.Marshal(deepSeekSearchPayload{
		Query:      query,
		MaxResults: maxRes,
	})
	if err != nil {
		return Result{}, fmt.Errorf("web_search: marshal request: %w", err)
	}

	endpoint := t.baseURL + "/v1/tools/search"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return Result{}, fmt.Errorf("web_search: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if t.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("web_search: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSearchRespBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("web_search: read response: %w", err)
	}
	if len(raw) > maxSearchRespBytes {
		return Result{}, errors.New("web_search: response exceeded 2MB limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 240 {
			msg = msg[:240] + "..."
		}
		return Result{}, fmt.Errorf("web_search: upstream HTTP %d: %s", resp.StatusCode, msg)
	}

	var parsed deepSeekSearchResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Result{}, fmt.Errorf("web_search: decode response: %w", err)
	}
	items := parsed.Results
	if len(items) == 0 {
		items = parsed.Data
	}
	if len(items) > maxRes {
		items = items[:maxRes]
	}

	hits := make([]SearchHit, 0, len(items))
	for _, it := range items {
		snippet := strings.TrimSpace(it.Snippet)
		if snippet == "" {
			snippet = strings.TrimSpace(it.Content)
		}
		hits = append(hits, SearchHit{
			Title:       strings.TrimSpace(it.Title),
			URL:         strings.TrimSpace(it.URL),
			Snippet:     snippet,
			PublishedAt: strings.TrimSpace(it.PublishedAt),
		})
	}

	if len(hits) == 0 {
		return Result{
			Text:       "No search results found.",
			Structured: hits,
		}, nil
	}

	var sb strings.Builder
	for i, hit := range hits {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "%d. %s\n%s", i+1, hit.Title, hit.URL)
		if hit.PublishedAt != "" {
			sb.WriteString(" (")
			sb.WriteString(hit.PublishedAt)
			sb.WriteByte(')')
		}
		if hit.Snippet != "" {
			sb.WriteByte('\n')
			sb.WriteString(hit.Snippet)
		}
	}
	return Result{
		Text:       sb.String(),
		Structured: hits,
	}, nil
}
