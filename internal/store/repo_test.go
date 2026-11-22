package store

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func TestTimelineAndRoomSummary(t *testing.T) {
	ctx := t.Context()
	s := openTestStore(t)

	seq1, err := s.AppendTimelineMessage(ctx, TimelineMessage{
		ChannelID:    "napcat",
		RoomID:       "room-1",
		MsgID:        "m1",
		SenderID:     "u1",
		SenderName:   "Alice",
		TimestampMs:  1000,
		RenderedText: "hello",
	})
	if err != nil {
		t.Fatalf("AppendTimelineMessage 1: %v", err)
	}
	seq2, err := s.AppendTimelineMessage(ctx, TimelineMessage{
		ChannelID:    "napcat",
		RoomID:       "room-1",
		MsgID:        "m2",
		SenderID:     "bot",
		SenderName:   "bk101",
		IsBot:        true,
		TimestampMs:  2000,
		RenderedText: "hi there",
	})
	if err != nil {
		t.Fatalf("AppendTimelineMessage 2: %v", err)
	}
	seq3, err := s.AppendTimelineMessage(ctx, TimelineMessage{
		SeqID:        100,
		ChannelID:    "napcat",
		RoomID:       "room-1",
		MsgID:        "m100",
		SenderID:     "u2",
		SenderName:   "Bob",
		IsAtBot:      true,
		TimestampMs:  3000,
		RenderedText: "@bk101 ping",
	})
	if err != nil || seq3 != 100 {
		t.Fatalf("explicit SeqID append = (%d, %v), want (100, nil)", seq3, err)
	}

	recent, err := s.ListRecentTimeline(ctx, "room-1", 2)
	if err != nil {
		t.Fatalf("ListRecentTimeline: %v", err)
	}
	if len(recent) != 2 || recent[0].SeqID != seq2 || recent[1].SeqID != seq3 {
		t.Fatalf("recent seqs = %+v, want [%d, %d]", recent, seq2, seq3)
	}

	if err := s.MarkTimelineArchived(ctx, []int64{seq1}); err != nil {
		t.Fatalf("MarkTimelineArchived: %v", err)
	}
	if err := s.UpdateTimelineStatus(ctx, []int64{seq1, seq2}, "PROCESSED"); err != nil {
		t.Fatalf("UpdateTimelineStatus: %v", err)
	}

	unarchived, err := s.ListUnarchivedTimeline(ctx, "room-1", 10)
	if err != nil {
		t.Fatalf("ListUnarchivedTimeline: %v", err)
	}
	if len(unarchived) != 2 || unarchived[0].SeqID != seq2 || unarchived[0].Status != "PROCESSED" {
		t.Fatalf("unexpected unarchived = %+v", unarchived)
	}

	if _, ok, err := s.GetRoomSummary(ctx, "room-1"); err != nil || ok {
		t.Fatalf("expected no initial summary, got ok=%v err=%v", ok, err)
	}
	wantSummary := RoomSummary{
		RoomID:            "room-1",
		SummaryText:       "Alice and Bob chatted with bk101.",
		LastSummarizedSeq: seq3,
		UpdatedAtMs:       4000,
	}
	if err := s.UpsertRoomSummary(ctx, wantSummary); err != nil {
		t.Fatalf("UpsertRoomSummary: %v", err)
	}
	gotSummary, ok, err := s.GetRoomSummary(ctx, "room-1")
	if err != nil || !ok || gotSummary != wantSummary {
		t.Fatalf("GetRoomSummary = (%+v, %v, %v), want %+v", gotSummary, ok, err, wantSummary)
	}
}

func TestMemoryLifecycleFTSVectorsAndAliases(t *testing.T) {
	ctx := t.Context()
	s := openTestStore(t)

	vec1024 := make([]float32, 1024)
	vec1024[0] = 0.75
	vec1024[1023] = -0.25

	if err := s.UpsertMemory(ctx, Memory{
		ID:          "mem-old",
		RoomID:      "r1",
		Kind:        MemoryKindEpisodic,
		Content:     "老王正在准备秋招面试",
		TEventMs:    1000,
		CreatedAtMs: 1000,
		UpdatedAtMs: 1000,
		Importance:  7,
		Embedding:   vec1024,
		Entities:    []string{"老王", "秋招"},
	}); err != nil {
		t.Fatalf("UpsertMemory old: %v", err)
	}
	if err := s.UpsertMemory(ctx, Memory{
		ID:          "mem-new",
		RoomID:      "r1",
		Kind:        MemoryKindSemantic,
		Content:     "老王已经入职游戏公司做后端开发",
		TEventMs:    2000,
		CreatedAtMs: 2000,
		UpdatedAtMs: 2000,
		Importance:  9,
		Embedding:   vec1024,
		Entities:    []string{"老王", "游戏公司"},
	}); err != nil {
		t.Fatalf("UpsertMemory new: %v", err)
	}
	if err := s.UpsertMemory(ctx, Memory{
		ID:          "mem-arch",
		RoomID:      "r1",
		Kind:        MemoryKindHabit,
		Content:     "老王每天晚上十点打游戏",
		TEventMs:    1500,
		CreatedAtMs: 1500,
		UpdatedAtMs: 1500,
		Importance:  4,
		Embedding:   vec1024,
		Entities:    []string{"老王"},
	}); err != nil {
		t.Fatalf("UpsertMemory arch: %v", err)
	}

	// Supersede mem-old and archive mem-arch.
	if err := s.SupersedeMemory(ctx, "mem-old", "mem-new", 2500); err != nil {
		t.Fatalf("SupersedeMemory: %v", err)
	}
	if err := s.ArchiveMemory(ctx, "mem-arch", 2600); err != nil {
		t.Fatalf("ArchiveMemory: %v", err)
	}

	// Active FTS search must exclude both superseded and archived memories.
	hits, err := s.SearchMemoriesFTS(ctx, "r1", "老王", 10, false)
	if err != nil {
		t.Fatalf("SearchMemoriesFTS active: %v", err)
	}
	if len(hits) != 1 || hits[0].Memory.ID != "mem-new" {
		t.Fatalf("active FTS hits = %+v, want only mem-new", hits)
	}

	// includeArchived=true returns archived mem-arch too, but STILL excludes superseded mem-old.
	hitsAll, err := s.SearchMemoriesFTS(ctx, "r1", "老王", 10, true)
	if err != nil {
		t.Fatalf("SearchMemoriesFTS includeArchived: %v", err)
	}
	var ids []string
	for _, h := range hitsAll {
		ids = append(ids, h.Memory.ID)
	}
	if len(ids) != 2 || slices.Contains(ids, "mem-old") {
		t.Fatalf("includeArchived FTS ids = %v, want [mem-new, mem-arch] without mem-old", ids)
	}

	// Re-upserting content must refresh FTS5 index cleanly without stale tokens.
	if err := s.UpsertMemory(ctx, Memory{
		ID:          "mem-new",
		RoomID:      "r1",
		Kind:        MemoryKindSemantic,
		Content:     "老王已经入职火星基地写代码",
		TEventMs:    2000,
		CreatedAtMs: 2000,
		UpdatedAtMs: 3000,
		Importance:  9,
		Embedding:   vec1024,
		Entities:    []string{"老王", "火星基地"},
	}); err != nil {
		t.Fatalf("re-upsert mem-new: %v", err)
	}
	staleHits, err := s.SearchMemoriesFTS(ctx, "r1", "游戏公司", 10, false)
	if err != nil || len(staleHits) != 0 {
		t.Fatalf("stale FTS token still matched: %+v err=%v", staleHits, err)
	}
	freshHits, err := s.SearchMemoriesFTS(ctx, "r1", "火星基地", 10, false)
	if err != nil || len(freshHits) != 1 || freshHits[0].Memory.ID != "mem-new" {
		t.Fatalf("fresh FTS token missed: %+v err=%v", freshHits, err)
	}

	// Active vector loading returns only non-superseded, non-archived 1024d vectors.
	vecs, err := s.LoadActiveMemoryVectors(ctx, "r1")
	if err != nil {
		t.Fatalf("LoadActiveMemoryVectors: %v", err)
	}
	if len(vecs) != 1 || vecs[0].ID != "mem-new" || len(vecs[0].Embedding) != 1024 || vecs[0].Embedding[0] != 0.75 {
		t.Fatalf("unexpected active vectors: len=%d", len(vecs))
	}

	// Anti-Matthew reinforcement cooldown enforcement.
	const cooldownMs = 30 * 60 * 1000
	ok1, err := s.ReinforceMemory(ctx, "mem-new", 10_000, cooldownMs)
	if err != nil || !ok1 {
		t.Fatalf("first ReinforceMemory = (%v, %v), want (true, nil)", ok1, err)
	}
	ok2, err := s.ReinforceMemory(ctx, "mem-new", 10_000+1000, cooldownMs)
	if err != nil || ok2 {
		t.Fatalf("cooldown ReinforceMemory = (%v, %v), want (false, nil)", ok2, err)
	}
	ok3, err := s.ReinforceMemory(ctx, "mem-new", 10_000+cooldownMs, cooldownMs)
	if err != nil || !ok3 {
		t.Fatalf("post-cooldown ReinforceMemory = (%v, %v), want (true, nil)", ok3, err)
	}
	gotMem, found, err := s.GetMemory(ctx, "mem-new")
	if err != nil || !found || gotMem.RecallCount != 2 || gotMem.LastRecalledMs != 10_000+cooldownMs {
		t.Fatalf("unexpected reinforced memory: %+v found=%v err=%v", gotMem, found, err)
	}

	// Entity lookup & alias CRUD.
	byEnt, err := s.ListMemoriesByEntity(ctx, "r1", "火星基地", false)
	if err != nil || len(byEnt) != 1 || byEnt[0].ID != "mem-new" {
		t.Fatalf("ListMemoriesByEntity = %+v err=%v", byEnt, err)
	}
	if err := s.UpsertEntityAlias(ctx, EntityAlias{
		RoomID:          "r1",
		Alias:           "王哥",
		CanonicalEntity: "老王",
		SenderID:        "qq123",
		UpdatedAtMs:     5000,
	}); err != nil {
		t.Fatalf("UpsertEntityAlias: %v", err)
	}
	aliases, err := s.ListEntityAliases(ctx, "r1")
	if err != nil || len(aliases) != 1 || aliases[0].CanonicalEntity != "老王" {
		t.Fatalf("ListEntityAliases = %+v err=%v", aliases, err)
	}
	if err := s.DeleteEntityAlias(ctx, "r1", "王哥"); err != nil {
		t.Fatalf("DeleteEntityAlias: %v", err)
	}
	aliases, err = s.ListEntityAliases(ctx, "r1")
	if err != nil || len(aliases) != 0 {
		t.Fatalf("expected empty aliases after delete, got %+v err=%v", aliases, err)
	}
}

func TestPersonaSnapshotsAndDiff(t *testing.T) {
	ctx := t.Context()
	s := openTestStore(t)

	core := PersonaCore{
		RoomID:  DefaultPersonaRoomID,
		Beliefs: "我是熟人群里的朋友 bk101。",
		Temperament: TemperamentParams{
			InitialBaseline: PADTriple{P: 0.12, A: 0.02, D: 0.02},
			Baseline:        PADTriple{P: 0.12, A: 0.02, D: 0.02},
			Eta:             PADTriple{P: 0.18, A: 0.22, D: 0.18},
			TauSec:          PADTriple{P: 720, A: 360, D: 600},
			Deadzone:        0.18,
		},
		LegacyPrompt: "legacy text",
		UpdatedAtMs:  1000,
	}
	if err := s.SetPersonaCore(ctx, core, ""); err != nil {
		t.Fatalf("SetPersonaCore: %v", err)
	}
	gotCore, ok, err := s.GetPersonaCore(ctx, "")
	if err != nil || !ok || gotCore != core {
		t.Fatalf("GetPersonaCore = (%+v, %v, %v), want %+v", gotCore, ok, err, core)
	}

	snap1, err := s.ReplacePersonaTraits(ctx, DefaultPersonaRoomID, []PersonaTrait{
		{ID: "t1", Content: "我喜欢替群友开心。", Position: 1},
		{ID: "t2", Content: "聊私事时我安静待着。", Position: 2},
	}, "initial_seed")
	if err != nil {
		t.Fatalf("ReplacePersonaTraits 1: %v", err)
	}

	snap2, err := s.ReplacePersonaTraits(ctx, DefaultPersonaRoomID, []PersonaTrait{
		{ID: "t1", Content: "群友出货时我特别替他们高兴。", Position: 1},
		{ID: "t3", Content: "被夸时会有点小得意。", Position: 2},
	}, "dreaming_reflection")
	if err != nil {
		t.Fatalf("ReplacePersonaTraits 2: %v", err)
	}

	snaps, err := s.ListPersonaSnapshots(ctx, 10)
	if err != nil || len(snaps) != 2 || snaps[0].ID != snap2 || snaps[1].ID != snap1 {
		t.Fatalf("ListPersonaSnapshots = %+v err=%v", snaps, err)
	}

	diff, err := s.DiffPersonaSnapshots(ctx, snap1, snap2)
	if err != nil {
		t.Fatalf("DiffPersonaSnapshots: %v", err)
	}
	if diff.BeliefsChanged || diff.TemperamentChanged {
		t.Fatalf("expected core unchanged, got %+v", diff)
	}
	if len(diff.AddedTraits) != 1 || diff.AddedTraits[0].ID != "t3" {
		t.Fatalf("AddedTraits = %+v, want [t3]", diff.AddedTraits)
	}
	if len(diff.RemovedTraits) != 1 || diff.RemovedTraits[0].ID != "t2" {
		t.Fatalf("RemovedTraits = %+v, want [t2]", diff.RemovedTraits)
	}
	if len(diff.UpdatedTraits) != 1 || diff.UpdatedTraits[0].ID != "t1" {
		t.Fatalf("UpdatedTraits = %+v, want [t1]", diff.UpdatedTraits)
	}
}

func TestPADMemesTracesAndDynamicConfig(t *testing.T) {
	ctx := t.Context()
	s := openTestStore(t)

	// PAD state + history
	wantPAD := PADState{RoomID: "r1", P: 0.4, A: 0.3, D: 0.1, PrototypeID: "happy_lively", UpdatedAtMs: 2000}
	if err := s.UpsertPADState(ctx, wantPAD); err != nil {
		t.Fatalf("UpsertPADState: %v", err)
	}
	gotPAD, ok, err := s.GetPADState(ctx, "r1")
	if err != nil || !ok || gotPAD != wantPAD {
		t.Fatalf("GetPADState = (%+v, %v, %v)", gotPAD, ok, err)
	}
	if _, err := s.AppendPADHistory(ctx, PADHistoryEntry{
		RoomID: "r1", RunID: "run-1", P: 0.2, A: 0.1, D: 0.0, DeltaP: 0.1, CreatedAtMs: 1000,
	}); err != nil {
		t.Fatalf("AppendPADHistory 1: %v", err)
	}
	if _, err := s.AppendPADHistory(ctx, PADHistoryEntry{
		RoomID: "r1", RunID: "run-2", P: 0.4, A: 0.3, D: 0.1, DeltaP: 0.2, CreatedAtMs: 2000,
	}); err != nil {
		t.Fatalf("AppendPADHistory 2: %v", err)
	}
	hist, err := s.ListPADHistory(ctx, "r1", 5)
	if err != nil || len(hist) != 2 || hist[0].RunID != "run-2" {
		t.Fatalf("ListPADHistory = %+v err=%v", hist, err)
	}

	// Memes + meme_seen
	if err := s.UpsertMeme(ctx, Meme{
		Hash:         "2ecd45abcdef0123",
		RelPath:      "memes/2ecd45abcdef0123.gif",
		ThumbRelPath: "memes/thumbs/2ecd45abcdef0123.webp",
		PHash:        "ph_abcd",
		OverlayText:  "破防了",
		Description:  "小猫捂脸崩溃",
		Emotions:     "破防,搞笑",
		Scenes:       "游戏输了",
		SearchBlob:   "破防了 小猫捂脸崩溃 搞笑",
		Embedding:    []float32{0.5, 0.5, 0.5, 0.5},
		UseCount:     3,
		CreatedAtMs:  1000,
		UpdatedAtMs:  1000,
	}); err != nil {
		t.Fatalf("UpsertMeme: %v", err)
	}
	if err := s.IncrementMemeUse(ctx, "2ecd45abcdef0123", 1500); err != nil {
		t.Fatalf("IncrementMemeUse: %v", err)
	}
	foundMeme, ok, err := s.FindMemeByPrefix(ctx, "#2ecd45")
	if err != nil || !ok || foundMeme.UseCount != 4 || len(foundMeme.Embedding) != 4 {
		t.Fatalf("FindMemeByPrefix = (%+v, %v, %v)", foundMeme, ok, err)
	}
	searched, err := s.SearchMemes(ctx, "小猫", 10)
	if err != nil || len(searched) != 1 {
		t.Fatalf("SearchMemes = %+v err=%v", searched, err)
	}
	if err := s.DeleteMeme(ctx, "2ecd45abcdef0123", 2000); err != nil {
		t.Fatalf("DeleteMeme: %v", err)
	}
	activeMemes, err := s.ListActiveMemes(ctx)
	if err != nil || len(activeMemes) != 0 {
		t.Fatalf("expected 0 active memes after delete, got %+v err=%v", activeMemes, err)
	}
	seen, ok, err := s.GetMemeSeen(ctx, "2ecd45abcdef0123")
	if err != nil || !ok || seen.Status != MemeStatusDeleted {
		t.Fatalf("expected meme_seen deleted, got (%+v, %v, %v)", seen, ok, err)
	}

	// Run traces
	if err := s.UpsertRunTrace(ctx, RunTrace{
		RunID: "run-1", RoomID: "r1", TriggerSource: "debounce", StartedAtMs: 1000, Status: "running",
	}); err != nil {
		t.Fatalf("UpsertRunTrace insert: %v", err)
	}
	if err := s.UpsertRunTrace(ctx, RunTrace{
		RunID: "run-1", RoomID: "r1", TriggerSource: "debounce", StartedAtMs: 1000, FinishedAtMs: 1450,
		Status: "replied", GateJSON: `{"p":0.92}`, ActionsJSON: `[{"type":"text"}]`,
	}); err != nil {
		t.Fatalf("UpsertRunTrace update: %v", err)
	}
	tr, ok, err := s.GetRunTrace(ctx, "run-1")
	if err != nil || !ok || tr.Status != "replied" || tr.FinishedAtMs != 1450 {
		t.Fatalf("GetRunTrace = (%+v, %v, %v)", tr, ok, err)
	}
	traces, err := s.ListRunTraces(ctx, "r1", 10)
	if err != nil || len(traces) != 1 {
		t.Fatalf("ListRunTraces = %+v err=%v", traces, err)
	}

	// Dynamic config + write-only secret redaction in RuntimeConfigView
	if err := s.SetSecret(ctx, "provider.google.key", "SUPER_SECRET_GEMINI_KEY_9876"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	cfg := RuntimeConfig{
		Providers: []ProviderConfig{{
			ID: "google", Name: "Gemini", Kind: "google", BaseURL: "https://generativelanguage.googleapis.com",
			SecretKey: "provider.google.key", Enabled: true,
		}},
		Models: []ModelConfig{{
			ID: "chat-main", ProviderID: "google", Protocol: "gemini-interactions", ModelID: "gemini-2.5-pro",
			Capabilities: ModelCapabilities{Vision: true, AudioIn: true, VideoIn: true, ToolCalls: true},
		}},
		Slots: SlotBindings{Chat: "chat-main"},
		MCPServers: []MCPServerConfig{{
			ID: "docs", Name: "Docs MCP", Transport: "http", URL: "http://127.0.0.1:9000/mcp", Enabled: false,
		}},
		Features: DefaultFeatureFlags(),
	}
	if err := s.SaveRuntimeConfig(ctx, cfg); err != nil {
		t.Fatalf("SaveRuntimeConfig: %v", err)
	}
	view, err := s.LoadRuntimeConfigView(ctx)
	if err != nil {
		t.Fatalf("LoadRuntimeConfigView: %v", err)
	}
	if view.Config.Features.BashToolsEnabled {
		t.Fatalf("BashToolsEnabled must default to false")
	}
	if len(view.Config.Providers) != 1 || view.Config.Slots.Chat != "chat-main" {
		t.Fatalf("unexpected runtime config: %+v", view.Config)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal RuntimeConfigView: %v", err)
	}
	if strings.Contains(string(raw), "SUPER_SECRET_GEMINI_KEY") {
		t.Fatalf("RuntimeConfigView leaked raw secret: %s", raw)
	}
	if len(view.Secrets) != 1 || view.Secrets[0].MaskedHint != "***9876" {
		t.Fatalf("unexpected secret view: %+v", view.Secrets)
	}
}

func TestConcurrentWALReadWrite(t *testing.T) {
	ctx := t.Context()
	s := openTestStore(t)

	// Seed one record so readers always have data to scan.
	if _, err := s.AppendTimelineMessage(ctx, TimelineMessage{
		RoomID: "room-wal", SenderID: "u0", SenderName: "seed", TimestampMs: 1, RenderedText: "seed",
	}); err != nil {
		t.Fatalf("seed timeline: %v", err)
	}

	const writers = 4
	const writesPerWorker = 25
	const readers = 4
	const readsPerWorker = 40

	errCh := make(chan error, writers*writesPerWorker+readers*readsPerWorker)
	var wg sync.WaitGroup

	for w := range writers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := range writesPerWorker {
				_, err := s.AppendTimelineMessage(ctx, TimelineMessage{
					ChannelID:    "napcat",
					RoomID:       "room-wal",
					SenderID:     fmt.Sprintf("u-%d", worker),
					SenderName:   "tester",
					TimestampMs:  int64(worker*1000 + i),
					RenderedText: fmt.Sprintf("msg-%d-%d", worker, i),
				})
				if err != nil {
					errCh <- err
					return
				}
			}
		}(w)
	}

	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range readsPerWorker {
				msgs, err := s.ListRecentTimeline(ctx, "room-wal", 20)
				if err != nil {
					errCh <- err
					return
				}
				if len(msgs) == 0 {
					errCh <- fmt.Errorf("expected non-empty timeline under WAL concurrent reads")
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent WAL error: %v", err)
	}

	all, err := s.ListRecentTimeline(ctx, "room-wal", 200)
	if err != nil {
		t.Fatalf("ListRecentTimeline final: %v", err)
	}
	wantCount := 1 + writers*writesPerWorker
	if len(all) != wantCount {
		t.Fatalf("final timeline count = %d, want %d", len(all), wantCount)
	}
}
