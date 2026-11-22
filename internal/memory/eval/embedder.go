package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type OpenAIEmbedderConfig struct {
	BaseURL    string
	APIKey     string
	Model      string
	Dimensions int
	BatchSize  int
	HTTPClient *http.Client
}

type OpenAIEmbedder struct {
	baseURL    string
	apiKey     string
	model      string
	dimensions int
	batchSize  int
	client     *http.Client
}

func NewOpenAIEmbedder(cfg OpenAIEmbedderConfig) (*OpenAIEmbedder, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("embedder: BaseURL is required")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("embedder: APIKey is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("embedder: Model is required")
	}
	batch := cfg.BatchSize
	if batch <= 0 || batch > 32 {
		batch = 32
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	return &OpenAIEmbedder{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		dimensions: cfg.Dimensions,
		batchSize:  batch,
		client:     client,
	}, nil
}

type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, len(texts))
	for start := 0; start < len(texts); start += e.batchSize {
		end := min(start+e.batchSize, len(texts))
		vecs, err := e.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("embedder batch [%d:%d]: %w", start, end, err)
		}
		copy(out[start:end], vecs)
	}
	return out, nil
}

func (e *OpenAIEmbedder) embedBatch(ctx context.Context, batch []string) ([][]float32, error) {
	payload, err := json.Marshal(embeddingRequest{
		Model:      e.model,
		Input:      batch,
		Dimensions: e.dimensions,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed embeddingResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("api error: %s", parsed.Error.Message)
	}
	if len(parsed.Data) != len(batch) {
		return nil, fmt.Errorf("expected %d embeddings, got %d", len(batch), len(parsed.Data))
	}

	vecs := make([][]float32, len(batch))
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= len(batch) {
			return nil, fmt.Errorf("invalid embedding index %d", item.Index)
		}
		vecs[item.Index] = item.Embedding
	}
	return vecs, nil
}

// CachedEmbedder persists embeddings in a single JSON file per (model, dim) so benchmark reruns avoid repeat API calls.
type CachedEmbedder struct {
	inner     Embedder
	cachePath string
	namespace string

	mu      sync.Mutex
	entries map[string][]float32
}

func NewCachedEmbedder(inner Embedder, cacheDir, model string, dim int) (*CachedEmbedder, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("embed cache mkdir: %w", err)
	}
	safeModel := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_").Replace(model)
	cachePath := filepath.Join(cacheDir, fmt.Sprintf("emb_%s_d%d.json", safeModel, dim))
	ce := &CachedEmbedder{
		inner:     inner,
		cachePath: cachePath,
		namespace: fmt.Sprintf("%s|d%d|", model, dim),
		entries:   make(map[string][]float32),
	}
	if raw, err := os.ReadFile(cachePath); err == nil {
		if err := json.Unmarshal(raw, &ce.entries); err != nil {
			return nil, fmt.Errorf("decode embed cache %s: %w", cachePath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read embed cache %s: %w", cachePath, err)
	}
	return ce, nil
}

func (c *CachedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, len(texts))
	keys := make([]string, len(texts))

	var missTexts []string
	var missIdx []int
	missPosByKey := make(map[string]int)

	c.mu.Lock()
	for i, text := range texts {
		k := c.key(text)
		keys[i] = k
		if vec, ok := c.entries[k]; ok {
			out[i] = vec
			continue
		}
		if _, queued := missPosByKey[k]; !queued {
			missPosByKey[k] = len(missTexts)
			missTexts = append(missTexts, text)
		}
		missIdx = append(missIdx, i)
	}
	c.mu.Unlock()

	if len(missTexts) == 0 {
		return out, nil
	}
	if c.inner == nil {
		return nil, fmt.Errorf("embed cache miss (%d texts) with nil inner embedder", len(missTexts))
	}

	// Never hold the mutex across network I/O.
	fetched, err := c.inner.Embed(ctx, missTexts)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	for textIdx, text := range missTexts {
		c.entries[c.key(text)] = fetched[textIdx]
	}
	for _, i := range missIdx {
		out[i] = c.entries[keys[i]]
	}
	err = c.saveLocked()
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *CachedEmbedder) key(text string) string {
	sum := sha256.Sum256([]byte(c.namespace + text))
	return hex.EncodeToString(sum[:16])
}

func (c *CachedEmbedder) saveLocked() error {
	raw, err := json.Marshal(c.entries)
	if err != nil {
		return fmt.Errorf("encode embed cache: %w", err)
	}
	tmp := c.cachePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("write embed cache tmp: %w", err)
	}
	if err := os.Rename(tmp, c.cachePath); err != nil {
		return fmt.Errorf("rename embed cache: %w", err)
	}
	return nil
}
