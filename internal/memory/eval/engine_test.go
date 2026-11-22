package eval

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTokenizeForFTS(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "mixed chinese and ascii",
			in:   "[2026-11-19] 显卡猎人抢到了RTX 5080首发",
			want: "2026 显卡 卡猎 猎人 人抢 抢到 到了 rtx 5080 首发",
		},
		{
			name: "short two-char chinese word",
			in:   "香菜",
			want: "香菜",
		},
		{
			name: "punctuation only",
			in:   "！？，。",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TokenizeForFTS(tc.in); got != tc.want {
				t.Errorf("TokenizeForFTS(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestVectorIndexTopKAndConcurrency(t *testing.T) {
	idx := NewVectorIndex(4)
	vecs := map[string][]float32{
		"a": {1, 0, 0, 0},
		"b": {0.8, 0.6, 0, 0},
		"c": {0, 1, 0, 0},
		"d": {0.9, 0.1, 0, 0},
	}
	for id, v := range vecs {
		if err := idx.Add(id, v); err != nil {
			t.Fatalf("Add(%q): %v", id, err)
		}
	}
	if err := idx.Add("bad", []float32{1, 2}); err == nil {
		t.Fatal("expected dim mismatch error")
	}

	q := []float32{1, 0, 0, 0}
	single := idx.TopK(q, 3, 0.1, 1)
	if len(single) != 3 || single[0].ID != "a" || single[1].ID != "d" || single[2].ID != "b" {
		t.Fatalf("unexpected top-3 order: %+v", single)
	}
	if math.Abs(float64(single[0].Similarity-1.0)) > 1e-5 {
		t.Fatalf("expected unit self-similarity, got %v", single[0].Similarity)
	}

	// Verify multi-worker scan matches single-threaded scan on >512 vectors.
	big := NewVectorIndex(16)
	for i := range 700 {
		v := make([]float32, 16)
		v[i%16] = 1.0
		v[(i+3)%16] = float32(i%7) * 0.1
		if err := big.Add(string(rune('A'+i/26))+string(rune('a'+i%26))+string(rune('0'+i%10)), v); err != nil {
			t.Fatal(err)
		}
	}
	bigQ := make([]float32, 16)
	bigQ[0] = 1.0
	bigQ[3] = 0.5
	w1 := big.TopK(bigQ, 10, 0, 1)
	w4 := big.TopK(bigQ, 10, 0, 4)
	if len(w1) != len(w4) {
		t.Fatalf("worker mismatch len: %d vs %d", len(w1), len(w4))
	}
	for i := range w1 {
		if w1[i].ID != w4[i].ID || math.Abs(float64(w1[i].Similarity-w4[i].Similarity)) > 1e-6 {
			t.Fatalf("mismatch at %d: %+v vs %+v", i, w1[i], w4[i])
		}
	}
}

func TestOpenAIEmbedderBatchingAndDiskCache(t *testing.T) {
	var calls atomic.Int32
	var maxBatch atomic.Int32
	var seenDim atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		calls.Add(1)
		if int32(len(req.Input)) > maxBatch.Load() {
			maxBatch.Store(int32(len(req.Input)))
		}
		seenDim.Store(int32(req.Dimensions))

		resp := embeddingResponse{}
		for i, text := range req.Input {
			resp.Data = append(resp.Data, struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}{
				Index:     i,
				Embedding: []float32{float32(len(text)), 1, 0, 0},
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	raw, err := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		BaseURL:    srv.URL + "/v1",
		APIKey:     "test-secret",
		Model:      "Qwen/Qwen3-Embedding-4B",
		Dimensions: 1024,
		BatchSize:  32,
	})
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	cacheDir := t.TempDir()
	cached, err := NewCachedEmbedder(raw, cacheDir, "Qwen/Qwen3-Embedding-4B", 1024)
	if err != nil {
		t.Fatalf("NewCachedEmbedder: %v", err)
	}

	texts := make([]string, 40)
	for i := range texts {
		texts[i] = strings.Repeat("x", i+1)
	}
	vecs, err := cached.Embed(t.Context(), texts)
	if err != nil {
		t.Fatalf("Embed first call: %v", err)
	}
	if len(vecs) != 40 {
		t.Fatalf("got %d vectors, want 40", len(vecs))
	}
	if calls.Load() != 2 || maxBatch.Load() != 32 || seenDim.Load() != 1024 {
		t.Fatalf("unexpected HTTP stats: calls=%d maxBatch=%d dim=%d", calls.Load(), maxBatch.Load(), seenDim.Load())
	}

	// Reload from disk with nil inner embedder: must satisfy 100% from cache.
	reloaded, err := NewCachedEmbedder(nil, cacheDir, "Qwen/Qwen3-Embedding-4B", 1024)
	if err != nil {
		t.Fatalf("reload cache: %v", err)
	}
	vecs2, err := reloaded.Embed(t.Context(), texts)
	if err != nil {
		t.Fatalf("Embed from cache: %v", err)
	}
	if vecs2[15][0] != 16 {
		t.Fatalf("unexpected cached vector value: %v", vecs2[15])
	}
}

func TestEngineAblationStagesOffline(t *testing.T) {
	ds := BuildSyntheticDataset()
	dbPath := filepath.Join(t.TempDir(), "mem_eval.db")

	eng, err := NewEngine(t.Context(), dbPath, ds)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	// Verify SQLite history chain traversal for 3-step job evolution.
	chain, err := eng.HistoryChain(t.Context(), "m_t01_v1")
	if err != nil {
		t.Fatalf("HistoryChain: %v", err)
	}
	if len(chain) != 3 || chain[0] != "m_t01_v1" || chain[1] != "m_t01_v2" || chain[2] != "m_t01_v3" {
		t.Fatalf("unexpected history chain: %v", chain)
	}

	// Verify anti-Matthew gate suppresses burst/irrelevant distractor recalls.
	antiStats := eng.computeRecallStats(ds.Now, true)
	naiveStats := eng.computeRecallStats(ds.Now, false)
	if antiStats["d_001"].count != 0 {
		t.Fatalf("anti-Matthew should ignore irrelevant distractor recalls, got %d", antiStats["d_001"].count)
	}
	if naiveStats["d_001"].count == 0 {
		t.Fatal("naive stats should reflect raw distractor hits")
	}
	if antiStats["m_ming_coriander"].count != 2 {
		t.Fatalf("expected 2 effective recalls for m_ming_coriander, got %d", antiStats["m_ming_coriander"].count)
	}

	// Verify exponential & power-law decay both monotonically decrease strength over time.
	sample := Memory{ID: "s", Importance: 7, TIngest: ds.Now - 10*daySec}
	for _, decay := range []DecayModel{DecayExponential, DecayPowerLaw} {
		cfg := DefaultStageConfig(StageB1)
		cfg.Decay = decay
		sNow := ComputeStrength(sample, 1, ds.Now-2*daySec, ds.Now, cfg, false)
		sLater := ComputeStrength(sample, 1, ds.Now-2*daySec, ds.Now+30*daySec, cfg, false)
		if !(sNow > sLater && sLater > 0) {
			t.Fatalf("decay %s not strictly decreasing: now=%v later=%v", decay, sNow, sLater)
		}
	}

	// Run offline ablation B0 -> B1 -> B2 -> B3 on real SQLite FTS5.
	mB0, _, err := eng.EvaluateStage(t.Context(), ds, nil, DefaultStageConfig(StageB0))
	if err != nil {
		t.Fatalf("EvaluateStage B0: %v", err)
	}
	mB1, _, err := eng.EvaluateStage(t.Context(), ds, nil, DefaultStageConfig(StageB1))
	if err != nil {
		t.Fatalf("EvaluateStage B1: %v", err)
	}
	mB2, _, err := eng.EvaluateStage(t.Context(), ds, nil, DefaultStageConfig(StageB2))
	if err != nil {
		t.Fatalf("EvaluateStage B2: %v", err)
	}
	mB3, _, err := eng.EvaluateStage(t.Context(), ds, nil, DefaultStageConfig(StageB3))
	if err != nil {
		t.Fatalf("EvaluateStage B3: %v", err)
	}

	if mB1.NoiseRate >= mB0.NoiseRate {
		t.Errorf("expected B1 noise rate (%.2f) < B0 (%.2f)", mB1.NoiseRate, mB0.NoiseRate)
	}
	if mB2.SupersededLeak != 0 {
		t.Errorf("expected B2 SupersededLeak == 0, got %.2f", mB2.SupersededLeak)
	}
	if mB2.TemporalAccuracy <= mB1.TemporalAccuracy {
		t.Errorf("expected B2 TemporalAccuracy (%.2f) > B1 (%.2f)", mB2.TemporalAccuracy, mB1.TemporalAccuracy)
	}
	if mB3.ByCategory[CategoryNicknameDrift].RecallAt4 <= mB2.ByCategory[CategoryNicknameDrift].RecallAt4 {
		t.Errorf("expected B3 NicknameDrift Recall@4 (%.2f) > B2 (%.2f)",
			mB3.ByCategory[CategoryNicknameDrift].RecallAt4,
			mB2.ByCategory[CategoryNicknameDrift].RecallAt4)
	}

	// Verify B4 concurrent vector dual-seed lifts associative recall using deterministic test vectors.
	vecIdx, qVecs := buildDeterministicTestVectors(ds)
	eng.SetVectors(vecIdx)
	mB4, _, err := eng.EvaluateStage(t.Context(), ds, qVecs, DefaultStageConfig(StageB4))
	if err != nil {
		t.Fatalf("EvaluateStage B4: %v", err)
	}
	t.Logf("Offline B0: Recall@4=%.3f MRR=%.3f TempAcc=%.3f Leak=%.3f Noise=%.3f", mB0.RecallAt4, mB0.MRR, mB0.TemporalAccuracy, mB0.SupersededLeak, mB0.NoiseRate)
	t.Logf("Offline B1: Recall@4=%.3f MRR=%.3f TempAcc=%.3f Leak=%.3f Noise=%.3f", mB1.RecallAt4, mB1.MRR, mB1.TemporalAccuracy, mB1.SupersededLeak, mB1.NoiseRate)
	t.Logf("Offline B2: Recall@4=%.3f MRR=%.3f TempAcc=%.3f Leak=%.3f Noise=%.3f", mB2.RecallAt4, mB2.MRR, mB2.TemporalAccuracy, mB2.SupersededLeak, mB2.NoiseRate)
	t.Logf("Offline B3: Recall@4=%.3f MRR=%.3f TempAcc=%.3f Leak=%.3f Noise=%.3f (Nick=%.3f Asc=%.3f)", mB3.RecallAt4, mB3.MRR, mB3.TemporalAccuracy, mB3.SupersededLeak, mB3.NoiseRate, mB3.ByCategory[CategoryNicknameDrift].RecallAt4, mB3.ByCategory[CategoryAssociative].RecallAt4)
	t.Logf("Offline B4: Recall@4=%.3f MRR=%.3f TempAcc=%.3f Leak=%.3f Noise=%.3f (Nick=%.3f Asc=%.3f)", mB4.RecallAt4, mB4.MRR, mB4.TemporalAccuracy, mB4.SupersededLeak, mB4.NoiseRate, mB4.ByCategory[CategoryNicknameDrift].RecallAt4, mB4.ByCategory[CategoryAssociative].RecallAt4)

	if mB4.ByCategory[CategoryAssociative].RecallAt4 <= mB3.ByCategory[CategoryAssociative].RecallAt4 {
		t.Errorf("expected B4 Associative Recall@4 (%.2f) > B3 (%.2f)",
			mB4.ByCategory[CategoryAssociative].RecallAt4,
			mB3.ByCategory[CategoryAssociative].RecallAt4)
	}
	if mB4.NoiseRate > 0.25 {
		t.Errorf("expected B4 NoiseRate <= 0.25 with deterministic vectors, got %.2f", mB4.NoiseRate)
	}
}

// buildDeterministicTestVectors constructs low-dim orthogonal topic vectors for offline B4 unit tests.
func buildDeterministicTestVectors(ds Dataset) (*VectorIndex, map[string][]float32) {
	const dim = 128
	idx := NewVectorIndex(dim)
	topicDim := make(map[string]int)
	nextDim := 1
	assignDim := func(tag string) int {
		if d, ok := topicDim[tag]; ok {
			return d
		}
		d := nextDim
		nextDim++
		if nextDim >= dim-1 {
			nextDim = 1
		}
		topicDim[tag] = d
		return d
	}

	memVecs := make(map[string][]float32, len(ds.Memories))
	for _, m := range ds.Memories {
		v := make([]float32, dim)
		if m.IsDistractor {
			v[0] = 1.0
		} else {
			d := assignDim(m.ID)
			v[d] = 1.0
		}
		memVecs[m.ID] = v
		_ = idx.Add(m.ID, v)
	}

	qVecs := make(map[string][]float32, len(ds.Queries))
	for _, q := range ds.Queries {
		v := make([]float32, dim)
		if len(q.ExpectedIDs) > 0 {
			for _, id := range q.ExpectedIDs {
				for d, val := range memVecs[id] {
					v[d] += val
				}
			}
		} else {
			// Orthogonal noise query vector with low similarity to stored memories.
			v[dim-1] = 1.0
		}
		qVecs[q.ID] = v
	}
	return idx, qVecs
}
