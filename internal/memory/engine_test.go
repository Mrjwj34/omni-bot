package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"omni-bot/internal/ai"
	"omni-bot/internal/store"
)

type stubEmbedder struct {
	calls atomic.Int64
	fn    func(text string) []float32
}

func (s *stubEmbedder) Embed(_ context.Context, req ai.EmbedRequest) (ai.EmbedResponse, error) {
	s.calls.Add(1)
	vecs := make([][]float32, len(req.Texts))
	for i, text := range req.Texts {
		vecs[i] = s.fn(text)
	}
	return ai.EmbedResponse{Vectors: vecs}, nil
}

// basisVec creates a deterministic 1024-dim unit vector concentrated on given (axis, weight) pairs.
func basisVec(weights ...struct {
	axis int
	w    float32
}) []float32 {
	v := make([]float32, DefaultVectorDim)
	for _, pair := range weights {
		v[pair.axis%DefaultVectorDim] = pair.w
	}
	return NormalizeL2(v)
}

func singleAxisVec(axis int) []float32 {
	return basisVec(struct {
		axis int
		w    float32
	}{axis: axis, w: 1.0})
}

func blendVec(axisA int, cosA float32, axisB int) []float32 {
	sinA := float32(math.Sqrt(max(0, 1.0-float64(cosA)*float64(cosA))))
	return basisVec(
		struct {
			axis int
			w    float32
		}{axis: axisA, w: cosA},
		struct {
			axis int
			w    float32
		}{axis: axisB, w: sinA},
	)
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "omni.db")
	st, err := store.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestVectorCacheConcurrentTopKAndMutations(t *testing.T) {
	cache := NewVectorCache(DefaultVectorDim)

	// Populate > 512 entries to trigger multi-worker fanout in TopK.
	const total = 620
	for i := range total {
		vec := blendVec(10, 0.25, 100+(i%800))
		if i == 123 {
			vec = blendVec(10, 0.98, 200)
		} else if i == 456 {
			vec = blendVec(10, 0.92, 201)
		}
		err := cache.Upsert(VectorEntry{
			ID:     fmt.Sprintf("m_%04d", i),
			RoomID: "room_a",
		}, vec)
		if err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}
	// Add a strong match in another room to verify room isolation.
	if err := cache.Upsert(VectorEntry{ID: "m_other_room", RoomID: "room_b"}, singleAxisVec(10)); err != nil {
		t.Fatalf("upsert other room: %v", err)
	}

	if got := cache.Len(); got != total+1 {
		t.Fatalf("cache.Len() = %d, want %d", got, total+1)
	}

	// Reject wrong vector dimension (e.g. legacy 2560d).
	if err := cache.Upsert(VectorEntry{ID: "bad_dim", RoomID: "room_a"}, make([]float32, 2560)); err == nil {
		t.Fatal("expected error for non-1024d vector, got nil")
	}

	query := singleAxisVec(10)
	hits := cache.TopK("room_a", query, 3, 0.50, 4)
	if len(hits) != 2 {
		t.Fatalf("TopK len = %d, want 2 (hits=%+v)", len(hits), hits)
	}
	if hits[0].ID != "m_0123" || hits[1].ID != "m_0456" {
		t.Fatalf("unexpected TopK order: %+v", hits)
	}

	// Concurrent reads and mutations must remain race-free and consistent.
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := range 25 {
				_ = cache.TopK("room_a", query, 4, 0.50, 4)
				id := fmt.Sprintf("tmp_%d_%d", worker, i)
				_ = cache.Upsert(VectorEntry{ID: id, RoomID: "room_a"}, singleAxisVec(300+worker))
				cache.Delete(id)
			}
		}(w)
	}
	wg.Wait()

	cache.Delete("m_0123")
	hitsAfterDelete := cache.TopK("room_a", query, 3, 0.50, 4)
	if len(hitsAfterDelete) != 1 || hitsAfterDelete[0].ID != "m_0456" {
		t.Fatalf("expected only m_0456 after delete, got %+v", hitsAfterDelete)
	}
}

func TestEnginePassiveRecallHybridFormattingAndPrecomputedVector(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	emb := &stubEmbedder{
		fn: func(text string) []float32 {
			return singleAxisVec(1)
		},
	}

	// Pre-populate SQLite before Engine startup to verify hydration on NewEngine.
	if err := st.UpsertMemory(ctx, store.Memory{
		ID:          "mem_cilantro",
		RoomID:      "room_1",
		Kind:        store.MemoryKindHabit,
		Content:     "小明不吃香菜，点外卖必备注不要香菜",
		TEventMs:    time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC).UnixMilli(),
		CreatedAtMs: now.Add(-4 * 24 * time.Hour).UnixMilli(),
		Importance:  8,
		Entities:    []string{"xiaoming"},
		Embedding:   singleAxisVec(1),
	}); err != nil {
		t.Fatalf("seed cilantro memory: %v", err)
	}
	if err := st.UpsertMemory(ctx, store.Memory{
		ID:          "mem_noise",
		RoomID:      "room_1",
		Kind:        store.MemoryKindEpisodic,
		Content:     "今天地铁早高峰特别挤",
		TEventMs:    now.Add(-20 * 24 * time.Hour).UnixMilli(),
		CreatedAtMs: now.Add(-20 * 24 * time.Hour).UnixMilli(),
		Importance:  2,
		Embedding:   blendVec(1, 0.51, 99),
	}); err != nil {
		t.Fatalf("seed noise memory: %v", err)
	}

	eng, err := NewEngine(ctx, st, emb, Config{
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if eng.VectorCount() != 2 {
		t.Fatalf("VectorCount() = %d, want 2", eng.VectorCount())
	}

	// Pass precomputed QueryVector (as OnGate would) and confirm Embedder is NOT called.
	res, err := eng.PassiveRecall(ctx, PassiveRecallRequest{
		RoomID:      "room_1",
		Query:       "别放那种绿油油气味冲的配菜",
		QueryVector: singleAxisVec(1),
	})
	if err != nil {
		t.Fatalf("PassiveRecall: %v", err)
	}
	if emb.calls.Load() != 0 {
		t.Fatalf("expected 0 Embedder calls with precomputed QueryVector, got %d", emb.calls.Load())
	}
	if len(res.Hits) != 1 || res.Hits[0].Memory.ID != "mem_cilantro" {
		t.Fatalf("expected only mem_cilantro, got %+v", res.Hits)
	}
	wantLine := "[2026-11-18] 小明不吃香菜，点外卖必备注不要香菜"
	if res.Hits[0].Formatted != wantLine {
		t.Fatalf("Formatted = %q, want %q", res.Hits[0].Formatted, wantLine)
	}
	if !strings.HasPrefix(res.FormattedBlock, "<associated_memories>\n- "+wantLine+"\n</associated_memories>") {
		t.Fatalf("unexpected FormattedBlock:\n%s", res.FormattedBlock)
	}
	if len(res.Trace) != 1 || res.Trace[0].MemoryID != "mem_cilantro" || res.Trace[0].TEventDate != "2026-11-18" {
		t.Fatalf("unexpected Trace: %+v", res.Trace)
	}
	rawTrace, err := json.Marshal(res.Trace)
	if err != nil || !strings.Contains(string(rawTrace), `"mem_cilantro"`) {
		t.Fatalf("marshal Trace failed: err=%v json=%s", err, string(rawTrace))
	}

	// Pure unrelated casual chat with borderline cosine ~0.51 should stay silent.
	noiseRes, err := eng.PassiveRecall(ctx, PassiveRecallRequest{
		RoomID:      "room_1",
		Query:       "哈哈今天天气不错",
		QueryVector: blendVec(1, 0.45, 50),
	})
	if err != nil {
		t.Fatalf("PassiveRecall noise: %v", err)
	}
	if len(noiseRes.Hits) != 0 || noiseRes.FormattedBlock != "" {
		t.Fatalf("expected silent noise rejection, got %d hits (%s)", len(noiseRes.Hits), noiseRes.FormattedBlock)
	}
}

func TestEngineNicknameDriftViaAliases(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	eng, err := NewEngine(ctx, st, nil, Config{
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Old nickname and new nickname both map to canonical entity "alice".
	for _, a := range []store.EntityAlias{
		{RoomID: "room_1", Alias: "熬夜冠军", CanonicalEntity: "alice", SenderID: "u_alice"},
		{RoomID: "room_1", Alias: "早睡达人", CanonicalEntity: "alice", SenderID: "u_alice"},
	} {
		if err := st.UpsertEntityAlias(ctx, a); err != nil {
			t.Fatalf("upsert alias: %v", err)
		}
	}

	if err := eng.UpsertMemory(ctx, store.Memory{
		ID:          "mem_alice_cat",
		RoomID:      "room_1",
		Kind:        store.MemoryKindSemantic,
		Content:     "熬夜冠军养了一只叫汤圆的橘猫，爱踩机械键盘",
		TEventMs:    now.Add(-5 * 24 * time.Hour).UnixMilli(),
		CreatedAtMs: now.Add(-5 * 24 * time.Hour).UnixMilli(),
		Importance:  8,
		Entities:    []string{"alice"},
		Embedding:   singleAxisVec(7),
	}); err != nil {
		t.Fatalf("UpsertMemory: %v", err)
	}

	// Query uses the NEW nickname ("早睡达人") plus topic cue ("键盘"), while memory text only mentions the OLD nickname.
	res, err := eng.PassiveRecall(ctx, PassiveRecallRequest{
		RoomID:      "room_1",
		Query:       "早睡达人家的猫又踩键盘了？",
		QueryVector: blendVec(7, 0.58, 88),
	})
	if err != nil {
		t.Fatalf("PassiveRecall nickname drift: %v", err)
	}
	if len(res.Hits) == 0 || res.Hits[0].Memory.ID != "mem_alice_cat" {
		t.Fatalf("expected mem_alice_cat to surface via alias resolution, got %+v", res.Hits)
	}
	if res.Hits[0].EntityBoost <= 0 {
		t.Fatalf("expected positive EntityBoost from alias resolution, got %+v", res.Hits[0])
	}
}

func TestEngineSupersededExclusion(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	eng, err := NewEngine(ctx, st, nil, Config{
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	if err := eng.UpsertMemory(ctx, store.Memory{
		ID:          "mem_gpu_old",
		RoomID:      "room_1",
		Kind:        store.MemoryKindEpisodic,
		Content:     "老王目前用的是RTX3060显卡",
		TEventMs:    now.Add(-20 * 24 * time.Hour).UnixMilli(),
		CreatedAtMs: now.Add(-20 * 24 * time.Hour).UnixMilli(),
		Importance:  8,
		RecallCount: 5,
		Entities:    []string{"wang"},
		Embedding:   singleAxisVec(12),
	}); err != nil {
		t.Fatalf("upsert old: %v", err)
	}
	if err := eng.UpsertMemory(ctx, store.Memory{
		ID:          "mem_gpu_new",
		RoomID:      "room_1",
		Kind:        store.MemoryKindSemantic,
		Content:     "老王已经升级换成了RTX5070Ti显卡",
		TEventMs:    now.Add(-2 * 24 * time.Hour).UnixMilli(),
		CreatedAtMs: now.Add(-2 * 24 * time.Hour).UnixMilli(),
		Importance:  8,
		Entities:    []string{"wang"},
		Embedding:   blendVec(12, 0.90, 13),
	}); err != nil {
		t.Fatalf("upsert new: %v", err)
	}

	if err := eng.SupersedeMemory(ctx, "mem_gpu_old", "mem_gpu_new"); err != nil {
		t.Fatalf("SupersedeMemory: %v", err)
	}
	if eng.VectorCount() != 1 {
		t.Fatalf("expected superseded memory evicted from VectorCache, count=%d", eng.VectorCount())
	}

	res, err := eng.PassiveRecall(ctx, PassiveRecallRequest{
		RoomID:      "room_1",
		Query:       "老王现在用什么显卡",
		QueryVector: singleAxisVec(12),
	})
	if err != nil {
		t.Fatalf("PassiveRecall: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Memory.ID != "mem_gpu_new" {
		t.Fatalf("expected only mem_gpu_new, got %+v", res.Hits)
	}
}

func TestEngineInWindowDeduplication(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	eng, err := NewEngine(ctx, st, nil, Config{
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	if err := eng.UpsertMemory(ctx, store.Memory{
		ID:          "mem_in_window",
		RoomID:      "room_1",
		Kind:        store.MemoryKindHabit,
		Content:     "小明不吃香菜",
		TEventMs:    now.Add(-3 * 24 * time.Hour).UnixMilli(),
		CreatedAtMs: now.Add(-3 * 24 * time.Hour).UnixMilli(),
		Importance:  9,
		Entities:    []string{"xiaoming"},
		Embedding:   singleAxisVec(20),
	}); err != nil {
		t.Fatalf("upsert mem_in_window: %v", err)
	}
	if err := eng.UpsertMemory(ctx, store.Memory{
		ID:          "mem_fresh_fact",
		RoomID:      "room_1",
		Kind:        store.MemoryKindHabit,
		Content:     "小明爱喝冰美式加燕麦奶",
		TEventMs:    now.Add(-4 * 24 * time.Hour).UnixMilli(),
		CreatedAtMs: now.Add(-4 * 24 * time.Hour).UnixMilli(),
		Importance:  8,
		Entities:    []string{"xiaoming"},
		Embedding:   blendVec(20, 0.85, 21),
	}); err != nil {
		t.Fatalf("upsert mem_fresh_fact: %v", err)
	}

	res, err := eng.PassiveRecall(ctx, PassiveRecallRequest{
		RoomID:      "room_1",
		Query:       "小明点外卖要注意什么",
		QueryVector: singleAxisVec(20),
		RecentWindow: []string{
			"刚小明自己说了，小明不吃香菜，别点错了",
		},
	})
	if err != nil {
		t.Fatalf("PassiveRecall: %v", err)
	}
	if res.SkippedInWin != 1 {
		t.Fatalf("SkippedInWin = %d, want 1", res.SkippedInWin)
	}
	for _, h := range res.Hits {
		if h.Memory.ID == "mem_in_window" {
			t.Fatalf("expected mem_in_window to be deduplicated, got %+v", res.Hits)
		}
	}
	if len(res.Hits) != 1 || res.Hits[0].Memory.ID != "mem_fresh_fact" {
		t.Fatalf("expected mem_fresh_fact to surface, got %+v", res.Hits)
	}
}

func TestEngineActiveRecallTimeEntityAndColdArchive(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	eng, err := NewEngine(ctx, st, nil, Config{
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	if err := st.UpsertEntityAlias(ctx, store.EntityAlias{
		RoomID:          "room_1",
		Alias:           "阿明",
		CanonicalEntity: "xiaoming",
	}); err != nil {
		t.Fatalf("upsert alias: %v", err)
	}

	octDate := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC).UnixMilli()
	novDate := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC).UnixMilli()

	if err := eng.UpsertMemory(ctx, store.Memory{
		ID:          "mem_cold_trip",
		RoomID:      "room_1",
		Kind:        store.MemoryKindEpisodic,
		Content:     "小明国庆节去京都看红叶买了抹茶点心",
		TEventMs:    octDate,
		CreatedAtMs: octDate,
		Importance:  4,
		Entities:    []string{"xiaoming"},
		Embedding:   singleAxisVec(30),
	}); err != nil {
		t.Fatalf("upsert cold trip: %v", err)
	}
	if err := eng.ArchiveMemory(ctx, "mem_cold_trip"); err != nil {
		t.Fatalf("ArchiveMemory: %v", err)
	}
	if eng.VectorCount() != 0 {
		t.Fatalf("expected archived memory evicted from VectorCache, got %d", eng.VectorCount())
	}

	if err := eng.UpsertMemory(ctx, store.Memory{
		ID:          "mem_active_nov",
		RoomID:      "room_1",
		Kind:        store.MemoryKindSemantic,
		Content:     "小明十一月开始学做手冲咖啡",
		TEventMs:    novDate,
		CreatedAtMs: novDate,
		Importance:  7,
		Entities:    []string{"xiaoming"},
		Embedding:   singleAxisVec(31),
	}); err != nil {
		t.Fatalf("upsert active nov: %v", err)
	}

	// Default ActiveRecall (IncludeArchived=false) must NOT return archived memory.
	hotOnly, err := eng.ActiveRecall(ctx, ActiveRecallRequest{
		RoomID:          "room_1",
		Query:           "京都红叶抹茶",
		IncludeArchived: false,
	})
	if err != nil {
		t.Fatalf("ActiveRecall hotOnly: %v", err)
	}
	if len(hotOnly.Hits) != 0 {
		t.Fatalf("expected 0 hits when IncludeArchived=false, got %+v", hotOnly.Hits)
	}

	// Cold archive ActiveRecall (IncludeArchived=true) with entity alias + time range must find it!
	coldRes, err := eng.ActiveRecall(ctx, ActiveRecallRequest{
		RoomID:          "room_1",
		Query:           "京都红叶抹茶",
		QueryVector:     singleAxisVec(30),
		Entity:          "阿明",
		TimeFrom:        time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).UnixMilli(),
		TimeTo:          time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC).UnixMilli(),
		IncludeArchived: true,
		Limit:           5,
	})
	if err != nil {
		t.Fatalf("ActiveRecall cold: %v", err)
	}
	if len(coldRes.Hits) != 1 || coldRes.Hits[0].Memory.ID != "mem_cold_trip" || !coldRes.Hits[0].Memory.Archived {
		t.Fatalf("expected archived mem_cold_trip, got %+v", coldRes.Hits)
	}

	// Entity-only ActiveRecall without text query returns active memories for that canonical entity.
	entOnly, err := eng.ActiveRecall(ctx, ActiveRecallRequest{
		RoomID: "room_1",
		Entity: "阿明",
		Limit:  5,
	})
	if err != nil {
		t.Fatalf("ActiveRecall entity-only: %v", err)
	}
	if len(entOnly.Hits) != 1 || entOnly.Hits[0].Memory.ID != "mem_active_nov" {
		t.Fatalf("expected mem_active_nov, got %+v", entOnly.Hits)
	}

	// Time-range-only ActiveRecall without text query or entity returns active memories in window.
	timeOnly, err := eng.ActiveRecall(ctx, ActiveRecallRequest{
		RoomID:   "room_1",
		TimeFrom: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).UnixMilli(),
		TimeTo:   time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC).UnixMilli(),
		Limit:    5,
	})
	if err != nil {
		t.Fatalf("ActiveRecall time-range-only: %v", err)
	}
	if len(timeOnly.Hits) != 1 || timeOnly.Hits[0].Memory.ID != "mem_active_nov" {
		t.Fatalf("expected mem_active_nov from time-range query, got %+v", timeOnly.Hits)
	}
}

func TestEngineReloadVectorsPreservesOtherRoomsAndEmbedFallback(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	emb := &stubEmbedder{
		fn: func(text string) []float32 { return singleAxisVec(55) },
	}
	eng, err := NewEngine(ctx, st, emb, Config{
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	if err := eng.UpsertMemory(ctx, store.Memory{
		ID:          "r1_m1",
		RoomID:      "room_1",
		Kind:        store.MemoryKindSemantic,
		Content:     "房间一的固定记忆",
		TEventMs:    now.UnixMilli(),
		CreatedAtMs: now.UnixMilli(),
		Importance:  8,
		Embedding:   singleAxisVec(55),
	}); err != nil {
		t.Fatalf("upsert r1_m1: %v", err)
	}
	if err := eng.UpsertMemory(ctx, store.Memory{
		ID:          "r2_m1",
		RoomID:      "room_2",
		Kind:        store.MemoryKindSemantic,
		Content:     "房间二的固定记忆",
		TEventMs:    now.UnixMilli(),
		CreatedAtMs: now.UnixMilli(),
		Importance:  8,
		Embedding:   singleAxisVec(56),
	}); err != nil {
		t.Fatalf("upsert r2_m1: %v", err)
	}

	// Reloading room_1 must not wipe room_2 from cache.
	if err := eng.ReloadVectors(ctx, "room_1"); err != nil {
		t.Fatalf("ReloadVectors(room_1): %v", err)
	}
	if eng.VectorCount() != 2 {
		t.Fatalf("VectorCount() = %d, want 2", eng.VectorCount())
	}

	// PassiveRecall without precomputed QueryVector calls Embedder automatically.
	res, err := eng.PassiveRecall(ctx, PassiveRecallRequest{
		RoomID: "room_1",
		Query:  "完全不同的口语问法",
	})
	if err != nil {
		t.Fatalf("PassiveRecall embed fallback: %v", err)
	}
	if emb.calls.Load() != 1 {
		t.Fatalf("expected 1 Embedder call, got %d", emb.calls.Load())
	}
	if len(res.Hits) != 1 || res.Hits[0].Memory.ID != "r1_m1" {
		t.Fatalf("expected r1_m1, got %+v", res.Hits)
	}
}

func TestEngineAntiMatthewReinforceAndCooldown(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)

	currentTime := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	eng, err := NewEngine(ctx, st, nil, Config{
		ReinforceCooldown: 30 * time.Minute,
		Clock:             func() time.Time { return currentTime },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	if err := eng.UpsertMemory(ctx, store.Memory{
		ID:          "mem_habit",
		RoomID:      "room_1",
		Kind:        store.MemoryKindHabit,
		Content:     "大刘每周五晚上打CS2",
		TEventMs:    currentTime.Add(-7 * 24 * time.Hour).UnixMilli(),
		CreatedAtMs: currentTime.Add(-7 * 24 * time.Hour).UnixMilli(),
		Importance:  7,
		Embedding:   singleAxisVec(40),
	}); err != nil {
		t.Fatalf("UpsertMemory: %v", err)
	}

	// Gate rejects when bot did not reply or memory was not relevant.
	n, err := eng.RecordEffectiveRecall(ctx, "room_1", []string{"mem_habit"}, false, true)
	if err != nil || n != 0 {
		t.Fatalf("botReplied=false: got (%d, %v), want (0, nil)", n, err)
	}
	n, err = eng.RecordEffectiveRecall(ctx, "room_1", []string{"mem_habit"}, true, false)
	if err != nil || n != 0 {
		t.Fatalf("relevant=false: got (%d, %v), want (0, nil)", n, err)
	}

	// Effective recall boosts once.
	n, err = eng.RecordEffectiveRecall(ctx, "room_1", []string{"mem_habit", "mem_habit"}, true, true)
	if err != nil || n != 1 {
		t.Fatalf("first effective recall: got (%d, %v), want (1, nil)", n, err)
	}

	// Repeat within 30-min cooldown must be ignored.
	currentTime = currentTime.Add(15 * time.Minute)
	n, err = eng.RecordEffectiveRecall(ctx, "room_1", []string{"mem_habit"}, true, true)
	if err != nil || n != 0 {
		t.Fatalf("within cooldown: got (%d, %v), want (0, nil)", n, err)
	}

	// After 30-min cooldown expires, second reinforcement succeeds.
	currentTime = currentTime.Add(16 * time.Minute)
	n, err = eng.RecordEffectiveRecall(ctx, "room_1", []string{"mem_habit"}, true, true)
	if err != nil || n != 1 {
		t.Fatalf("after cooldown: got (%d, %v), want (1, nil)", n, err)
	}

	gotMem, ok, err := st.GetMemory(ctx, "mem_habit")
	if err != nil || !ok {
		t.Fatalf("GetMemory: ok=%v err=%v", ok, err)
	}
	if gotMem.RecallCount != 2 || gotMem.LastRecalledMs != currentTime.UnixMilli() {
		t.Fatalf("DB state mismatch: recall_count=%d last_recalled=%d", gotMem.RecallCount, gotMem.LastRecalledMs)
	}
	cachedEntry, _, ok := eng.vectors.Get("mem_habit")
	if !ok || cachedEntry.RecallCount != 2 || cachedEntry.LastRecalledMs != currentTime.UnixMilli() {
		t.Fatalf("VectorCache metadata not synced: %+v (ok=%v)", cachedEntry, ok)
	}
}

func TestComputeStrengthPowerLawProperties(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC).UnixMilli()
	old := now - int64(25*24*time.Hour/time.Millisecond)

	lowImp := ComputeStrength(store.Memory{Importance: 2, CreatedAtMs: old}, now, 14, 0.85)
	highImp := ComputeStrength(store.Memory{Importance: 9, CreatedAtMs: old}, now, 14, 0.85)
	reinforced := ComputeStrength(store.Memory{Importance: 5, CreatedAtMs: old, RecallCount: 4, LastRecalledMs: now - int64(2*24*time.Hour/time.Millisecond)}, now, 14, 0.85)
	unreinforced := ComputeStrength(store.Memory{Importance: 5, CreatedAtMs: old, RecallCount: 0}, now, 14, 0.85)
	superseded := ComputeStrength(store.Memory{Importance: 7, CreatedAtMs: old, SupersededBy: "new_id"}, now, 14, 0.85)
	active := ComputeStrength(store.Memory{Importance: 7, CreatedAtMs: old}, now, 14, 0.85)

	if !(highImp > lowImp) {
		t.Fatalf("expected high importance strength (%v) > low importance (%v)", highImp, lowImp)
	}
	if !(reinforced > unreinforced) {
		t.Fatalf("expected reinforced strength (%v) > unreinforced (%v)", reinforced, unreinforced)
	}
	if !(active > superseded) {
		t.Fatalf("expected active strength (%v) > superseded (%v)", active, superseded)
	}
}
