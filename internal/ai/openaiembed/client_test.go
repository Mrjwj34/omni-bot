package openaiembed_test

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"omni-bot/internal/ai"
	"omni-bot/internal/ai/openaiembed"
)

func TestBatchChunkingOrderingAndL2Normalization(t *testing.T) {
	var (
		batchSizes []int
		seenAuths  []string
		seenDims   []int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.Error(w, "bad path "+r.URL.Path, http.StatusNotFound)
			return
		}
		seenAuths = append(seenAuths, r.Header.Get("Authorization"))

		var req struct {
			Model      string   `json:"model"`
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		batchSizes = append(batchSizes, len(req.Input))
		seenDims = append(seenDims, req.Dimensions)

		// Return embeddings in reverse index order to verify order preservation.
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		data := make([]item, 0, len(req.Input))
		for i := len(req.Input) - 1; i >= 0; i-- {
			var id int
			_, _ = fmt.Sscanf(req.Input[i], "doc-%d", &id)
			data = append(data, item{
				Index:     i,
				Embedding: []float32{float32(id + 1), 3, 4},
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   data,
			"usage": map[string]any{
				"prompt_tokens": len(req.Input) * 2,
				"total_tokens":  len(req.Input) * 2,
			},
		})
	}))
	defer srv.Close()

	client, err := openaiembed.New(ai.ResolvedModel{
		Provider: ai.ProviderConfig{
			ID:      "sf",
			Kind:    "openai",
			BaseURL: srv.URL + "/v1",
			APIKeys: []string{"k1", "k2"},
		},
		Model: ai.ModelConfig{
			ID:         "emb-1",
			ProviderID: "sf",
			Protocol:   openaiembed.Protocol,
			ModelID:    "Qwen/Qwen3-Embedding-4B",
		},
		Keys: ai.NewKeyRotator([]string{"k1", "k2"}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	texts := make([]string, 70)
	for i := range texts {
		texts[i] = fmt.Sprintf("doc-%d", i)
	}

	resp, err := client.Embed(t.Context(), ai.EmbedRequest{Texts: texts})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	// Verify 70 texts split into 32 + 32 + 6 chunks.
	wantBatches := []int{32, 32, 6}
	if len(batchSizes) != len(wantBatches) {
		t.Fatalf("batchSizes = %v, want %v", batchSizes, wantBatches)
	}
	for i := range wantBatches {
		if batchSizes[i] != wantBatches[i] {
			t.Fatalf("batch[%d] = %d, want %d", i, batchSizes[i], wantBatches[i])
		}
		if seenDims[i] != 1024 {
			t.Fatalf("dimensions[%d] = %d, want 1024", i, seenDims[i])
		}
	}

	// Verify key rotation across the 3 chunk calls.
	wantAuths := []string{"Bearer k1", "Bearer k2", "Bearer k1"}
	for i := range wantAuths {
		if seenAuths[i] != wantAuths[i] {
			t.Fatalf("auth[%d] = %q, want %q", i, seenAuths[i], wantAuths[i])
		}
	}

	if len(resp.Vectors) != 70 {
		t.Fatalf("len(Vectors) = %d, want 70", len(resp.Vectors))
	}
	for i, vec := range resp.Vectors {
		var normSq float64
		for _, v := range vec {
			normSq += float64(v) * float64(v)
		}
		if math.Abs(math.Sqrt(normSq)-1.0) > 1e-5 {
			t.Fatalf("vec[%d] not L2-normalized: norm=%f", i, math.Sqrt(normSq))
		}
		// Check ordering preserved: first component increases monotonically with id.
		wantFirst := float64(i+1) / math.Sqrt(float64((i+1)*(i+1)+25))
		if math.Abs(float64(vec[0])-wantFirst) > 1e-5 {
			t.Fatalf("vec[%d][0] = %f, want %f (ordering mismatch)", i, vec[0], wantFirst)
		}
	}

	if resp.Usage.InputTokens == nil || *resp.Usage.InputTokens != 140 {
		t.Fatalf("InputTokens = %v, want 140", resp.Usage.InputTokens)
	}
	if resp.Usage.TotalTokens == nil || *resp.Usage.TotalTokens != 140 {
		t.Fatalf("TotalTokens = %v, want 140", resp.Usage.TotalTokens)
	}
}

func TestHTTPErrorClassificationAndNoRetries(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantKind  ai.ErrorKind
		retryable bool
	}{
		{"401", http.StatusUnauthorized, `{"error":"invalid key"}`, ai.ErrAuth, false},
		{"429", http.StatusTooManyRequests, `{"error":"rate limit"}`, ai.ErrRateLimit, true},
		{"500", http.StatusInternalServerError, `{"error":"server err"}`, ai.ErrTransient, true},
		{"400 context", http.StatusBadRequest, `{"error":"context_length_exceeded"}`, ai.ErrContextTooLong, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			client, err := openaiembed.New(ai.ResolvedModel{
				Provider: ai.ProviderConfig{ID: "sf", BaseURL: srv.URL, APIKeys: []string{"k"}},
				Model:    ai.ModelConfig{ID: "e", ProviderID: "sf", Protocol: openaiembed.Protocol, ModelID: "m"},
				Keys:     ai.NewKeyRotator([]string{"k"}),
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			_, err = client.Embed(t.Context(), ai.EmbedRequest{Texts: []string{"hello"}})
			if err == nil {
				t.Fatal("expected error")
			}
			if hits.Load() != 1 {
				t.Fatalf("expected 1 attempt (no internal retries), got %d", hits.Load())
			}
			if got := ai.ErrorKindOf(err); got != tc.wantKind {
				t.Fatalf("ErrorKindOf = %q, want %q", got, tc.wantKind)
			}
			if got := ai.IsRetryable(err); got != tc.retryable {
				t.Fatalf("IsRetryable = %v, want %v", got, tc.retryable)
			}
		})
	}
}
