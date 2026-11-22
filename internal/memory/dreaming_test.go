package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"omni-bot/internal/ai"
	"omni-bot/internal/store"
)

type stubGenerator struct {
	lastReq ai.GenerateRequest
	resp    string
	err     error
}

func (g *stubGenerator) Generate(_ context.Context, req ai.GenerateRequest) (ai.GenerateResponse, error) {
	g.lastReq = req
	if g.err != nil {
		return ai.GenerateResponse{}, g.err
	}
	return ai.GenerateResponse{Text: g.resp}, nil
}

type stubDistiller struct {
	fn func(ctx context.Context, roomID string, messages []store.TimelineMessage, aliases []store.EntityAlias) (DistillOutput, error)
}

func (s *stubDistiller) Distill(ctx context.Context, roomID string, messages []store.TimelineMessage, aliases []store.EntityAlias) (DistillOutput, error) {
	return s.fn(ctx, roomID, messages, aliases)
}

func TestLLMDistillerPromptParsingAndBoilerplateSanitization(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	now := time.Date(2026, 11, 22, 14, 0, 0, 0, time.UTC)

	gen := &stubGenerator{
		resp: "```json\n" + `{
			"facts": [
				{
					"kind": "habit",
					"content": "群聊成员“小明”（QQ: 12345678）于2026年11月21日表示：不吃香菜，点外卖必备注",
					"t_event_date": "2026-11-21",
					"importance": 8,
					"entities": ["阿明"]
				}
			],
			"aliases": [
				{
					"alias": "阿明",
					"canonical_entity": "xiaoming",
					"sender_id": "u_ming"
				}
			]
		}` + "\n```",
	}
	emb := &stubEmbedder{
		fn: func(text string) []float32 { return singleAxisVec(5) },
	}

	eng, err := NewEngine(ctx, st, emb, Config{
		Distiller: NewLLMDistiller(gen, time.UTC),
		Clock:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	seqID, err := st.AppendTimelineMessage(ctx, store.TimelineMessage{
		RoomID:       "room_1",
		SenderID:     "u_ming",
		SenderName:   "阿明",
		TimestampMs:  time.Date(2026, 11, 21, 18, 30, 0, 0, time.UTC).UnixMilli(),
		RenderedText: "我真的一点香菜都不吃，点外卖都要备注",
	})
	if err != nil {
		t.Fatalf("AppendTimelineMessage: %v", err)
	}

	msgs, err := st.ListUnarchivedTimeline(ctx, "room_1", 50)
	if err != nil || len(msgs) != 1 || msgs[0].SeqID != seqID {
		t.Fatalf("ListUnarchivedTimeline: len=%d err=%v", len(msgs), err)
	}

	rep, err := eng.DistillBatch(ctx, "room_1", msgs)
	if err != nil {
		t.Fatalf("DistillBatch: %v", err)
	}
	if rep.Inserted != 1 || rep.AliasesSynced != 1 || len(rep.MemoryIDs) != 1 {
		t.Fatalf("unexpected DistillReport: %+v", rep)
	}
	if gen.lastReq.MaxOutputTokens < 2048 || gen.lastReq.ResponseSchema == nil {
		t.Fatalf("expected MaxOutputTokens >= 2048 and ResponseSchema, got max=%d schema=%v", gen.lastReq.MaxOutputTokens, gen.lastReq.ResponseSchema != nil)
	}
	if len(gen.lastReq.Messages) != 1 || !strings.Contains(gen.lastReq.Messages[0].Parts[0].Text, "[2026-11-21 18:30]") {
		t.Fatalf("expected absolute timestamp in prompt, got %+v", gen.lastReq.Messages)
	}

	saved, ok, err := st.GetMemory(ctx, rep.MemoryIDs[0])
	if err != nil || !ok {
		t.Fatalf("GetMemory: ok=%v err=%v", ok, err)
	}
	if strings.Contains(saved.Content, "QQ") || strings.Contains(saved.Content, "群聊成员") {
		t.Fatalf("boilerplate was not stripped from content: %q", saved.Content)
	}
	if saved.Content != "小明不吃香菜，点外卖必备注" {
		t.Fatalf("sanitized content = %q, want %q", saved.Content, "小明不吃香菜，点外卖必备注")
	}
	if len(saved.Entities) != 1 || saved.Entities[0] != "xiaoming" {
		t.Fatalf("expected canonicalized entity [xiaoming], got %+v", saved.Entities)
	}
	if gotDate := time.UnixMilli(saved.TEventMs).UTC().Format("2006-01-02"); gotDate != "2026-11-21" {
		t.Fatalf("TEventDate = %q, want 2026-11-21", gotDate)
	}

	// Timeline message should now be marked archived_to_memory = 1.
	rem, err := st.ListUnarchivedTimeline(ctx, "room_1", 50)
	if err != nil || len(rem) != 0 {
		t.Fatalf("expected 0 unarchived timeline messages, got %d (err=%v)", len(rem), err)
	}
}

func TestDistillBatchDuplicateMergeAndTemporalSuperseding(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	emb := &stubEmbedder{
		fn: func(text string) []float32 {
			switch {
			case strings.Contains(text, "香菜"):
				if strings.Contains(text, "坚决") {
					return blendVec(10, 0.94, 11) // cos = 0.94 >= 0.88 near-duplicate
				}
				return singleAxisVec(10)
			case strings.Contains(text, "RTX5070Ti"):
				return blendVec(20, 0.78, 21) // cos = 0.78 temporal update on GPU topic
			case strings.Contains(text, "RTX3060"):
				return singleAxisVec(20)
			default:
				return singleAxisVec(99)
			}
		},
	}

	eng, err := NewEngine(ctx, st, emb, Config{
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Batch 1: seed initial facts & aliases.
	rep1, err := eng.DistillBatchWith(ctx, "room_1", []store.TimelineMessage{
		{RoomID: "room_1", SenderID: "u_ming", SenderName: "阿明", TimestampMs: now.Add(-10 * 24 * time.Hour).UnixMilli(), RenderedText: "我不吃香菜"},
		{RoomID: "room_1", SenderID: "u_wang", SenderName: "老王", TimestampMs: now.Add(-10 * 24 * time.Hour).UnixMilli(), RenderedText: "我现在还是3060"},
	}, &stubDistiller{
		fn: func(_ context.Context, _ string, _ []store.TimelineMessage, _ []store.EntityAlias) (DistillOutput, error) {
			return DistillOutput{
				Facts: []DistilledFact{
					{Kind: "episodic", Content: "小明不吃香菜", TEventDate: "2026-11-12", Importance: 6, Entities: []string{"阿明"}},
					{Kind: "semantic", Content: "老王目前用的是RTX3060显卡", TEventDate: "2026-11-12", Importance: 7, Entities: []string{"老王"}},
				},
				Aliases: []AliasObservation{
					{Alias: "阿明", CanonicalEntity: "xiaoming", SenderID: "u_ming"},
					{Alias: "老王", CanonicalEntity: "wang", SenderID: "u_wang"},
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("DistillBatch 1: %v", err)
	}
	if rep1.Inserted != 2 || rep1.Merged != 0 || rep1.Superseded != 0 {
		t.Fatalf("unexpected batch 1 report: %+v", rep1)
	}
	cilantroID := rep1.MemoryIDs[0]
	oldGPUID := rep1.MemoryIDs[1]

	// Record one effective recall on cilantro memory so we can verify merge preserves recall_count.
	if _, err := eng.RecordEffectiveRecall(ctx, "room_1", []string{cilantroID}, true, true); err != nil {
		t.Fatalf("RecordEffectiveRecall: %v", err)
	}

	// Batch 2: near-duplicate restatement (cos=0.94 >= 0.88), temporal GPU upgrade, and nickname drift for u_ming.
	rep2, err := eng.DistillBatchWith(ctx, "room_1", []store.TimelineMessage{
		{RoomID: "room_1", SenderID: "u_ming", SenderName: "香菜克星", TimestampMs: now.UnixMilli(), RenderedText: "我坚决不吃香菜"},
		{RoomID: "room_1", SenderID: "u_wang", SenderName: "老王", TimestampMs: now.UnixMilli(), RenderedText: "今天把显卡换成5070Ti了"},
	}, &stubDistiller{
		fn: func(_ context.Context, _ string, _ []store.TimelineMessage, _ []store.EntityAlias) (DistillOutput, error) {
			return DistillOutput{
				Facts: []DistilledFact{
					{Kind: "habit", Content: "小明坚决不吃香菜，每次聚餐都会避开", TEventDate: "2026-11-22", Importance: 9, Entities: []string{"香菜克星"}},
					{Kind: "semantic", Content: "老王已经升级换成了RTX5070Ti显卡", TEventDate: "2026-11-22", Importance: 8, Entities: []string{"wang"}, Updates: "老王显卡型号"},
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("DistillBatch 2: %v", err)
	}
	if rep2.Merged != 1 || rep2.Inserted != 1 || rep2.Superseded != 1 {
		t.Fatalf("unexpected batch 2 report: %+v", rep2)
	}

	// Verify duplicate collapsed into original ID, promoted to habit, kept max importance=9 & preserved recall_count=1.
	mergedMem, ok, err := st.GetMemory(ctx, cilantroID)
	if err != nil || !ok {
		t.Fatalf("GetMemory cilantro: ok=%v err=%v", ok, err)
	}
	if mergedMem.Kind != store.MemoryKindHabit || mergedMem.Importance != 9 || mergedMem.RecallCount != 1 {
		t.Fatalf("merged memory state mismatch: %+v", mergedMem)
	}

	// Verify old GPU memory is linked via superseded_by to new GPU memory and excluded from recall.
	oldGPU, ok, err := st.GetMemory(ctx, oldGPUID)
	if err != nil || !ok {
		t.Fatalf("GetMemory oldGPU: ok=%v err=%v", ok, err)
	}
	newGPUID := rep2.MemoryIDs[1]
	if oldGPU.SupersededBy != newGPUID {
		t.Fatalf("oldGPU.SupersededBy = %q, want %q", oldGPU.SupersededBy, newGPUID)
	}
	if eng.VectorCount() != 2 {
		t.Fatalf("expected 2 active vectors after merge+supersede, got %d", eng.VectorCount())
	}

	recallRes, err := eng.PassiveRecall(ctx, PassiveRecallRequest{
		RoomID:      "room_1",
		Query:       "老王现在用的是什么显卡",
		QueryVector: singleAxisVec(20),
	})
	if err != nil {
		t.Fatalf("PassiveRecall GPU: %v", err)
	}
	if len(recallRes.Hits) != 1 || recallRes.Hits[0].Memory.ID != newGPUID {
		t.Fatalf("expected only new GPU memory %q, got %+v", newGPUID, recallRes.Hits)
	}

	// Verify u_ming's new nickname ("香菜克星") was auto-synced to canonical "xiaoming".
	aliases, err := st.ListEntityAliases(ctx, "room_1")
	if err != nil {
		t.Fatalf("ListEntityAliases: %v", err)
	}
	foundNewAlias := false
	for _, a := range aliases {
		if a.Alias == "香菜克星" && a.CanonicalEntity == "xiaoming" && a.SenderID == "u_ming" {
			foundNewAlias = true
		}
	}
	if !foundNewAlias {
		t.Fatalf("expected auto-synced alias 香菜克星 -> xiaoming, got %+v", aliases)
	}
}

func TestRunDreamingClusterMergeAliasSyncAndBudgetArchiving(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	eng, err := NewEngine(ctx, st, nil, Config{
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Seed transitive alias chain (old_nick -> mid_nick -> alice) + recent timeline message with brand-new nickname.
	for _, a := range []store.EntityAlias{
		{RoomID: "room_1", Alias: "old_nick", CanonicalEntity: "mid_nick", SenderID: "u_alice"},
		{RoomID: "room_1", Alias: "mid_nick", CanonicalEntity: "alice", SenderID: "u_alice"},
	} {
		if err := st.UpsertEntityAlias(ctx, a); err != nil {
			t.Fatalf("upsert alias: %v", err)
		}
	}
	if _, err := st.AppendTimelineMessage(ctx, store.TimelineMessage{
		RoomID:       "room_1",
		SenderID:     "u_alice",
		SenderName:   "早睡新昵称",
		TimestampMs:  now.Add(-1 * time.Hour).UnixMilli(),
		RenderedText: "今天下班好早",
	}); err != nil {
		t.Fatalf("AppendTimelineMessage: %v", err)
	}

	// Seed a cluster of 3 near-duplicate episodic memories (cos >= 0.85) mimicking legacy night-shift duplication.
	clusterMems := []store.Memory{
		{
			ID:          "night_1",
			RoomID:      "room_1",
			Kind:        store.MemoryKindEpisodic,
			Content:     "大刘最近转为上夜班了",
			TEventMs:    now.Add(-4 * 24 * time.Hour).UnixMilli(),
			CreatedAtMs: now.Add(-4 * 24 * time.Hour).UnixMilli(),
			Importance:  4,
			RecallCount: 1,
			Entities:    []string{"daliu"},
			Embedding:   singleAxisVec(1),
		},
		{
			ID:          "night_2",
			RoomID:      "room_1",
			Kind:        store.MemoryKindEpisodic,
			Content:     "大刘这周都在值夜班，白天补觉",
			TEventMs:    now.Add(-2 * 24 * time.Hour).UnixMilli(),
			CreatedAtMs: now.Add(-2 * 24 * time.Hour).UnixMilli(),
			Importance:  7,
			RecallCount: 2,
			Entities:    []string{"daliu"},
			Embedding:   blendVec(1, 0.95, 2),
		},
		{
			ID:          "night_3",
			RoomID:      "room_1",
			Kind:        store.MemoryKindEpisodic,
			Content:     "大刘近期工作排班改成了夜班",
			TEventMs:    now.Add(-1 * 24 * time.Hour).UnixMilli(),
			CreatedAtMs: now.Add(-1 * 24 * time.Hour).UnixMilli(),
			Importance:  5,
			RecallCount: 3,
			Entities:    []string{"daliu"},
			Embedding:   blendVec(1, 0.93, 3),
		},
	}
	for _, m := range clusterMems {
		if err := eng.UpsertMemory(ctx, m); err != nil {
			t.Fatalf("upsert %s: %v", m.ID, err)
		}
	}

	// Seed 1 durable habit memory + 3 distinct episodic fragments (2 old low-importance, 1 recent higher-importance).
	extraMems := []store.Memory{
		{
			ID:          "habit_core",
			RoomID:      "room_1",
			Kind:        store.MemoryKindHabit,
			Content:     "Alice每天早上必喝一杯黑咖啡",
			TEventMs:    now.Add(-25 * 24 * time.Hour).UnixMilli(),
			CreatedAtMs: now.Add(-25 * 24 * time.Hour).UnixMilli(),
			Importance:  9,
			RecallCount: 4,
			Entities:    []string{"alice"},
			Embedding:   singleAxisVec(30),
		},
		{
			ID:          "ep_old_weak_1",
			RoomID:      "room_1",
			Kind:        store.MemoryKindEpisodic,
			Content:     "十月某天快递放菜鸟驿站差点忘拿",
			TEventMs:    now.Add(-45 * 24 * time.Hour).UnixMilli(),
			CreatedAtMs: now.Add(-45 * 24 * time.Hour).UnixMilli(),
			Importance:  2,
			Entities:    []string{"daliu"},
			Embedding:   singleAxisVec(40),
		},
		{
			ID:          "ep_old_weak_2",
			RoomID:      "room_1",
			Kind:        store.MemoryKindEpisodic,
			Content:     "十月某天上班路上遇到下雨忘带伞",
			TEventMs:    now.Add(-40 * 24 * time.Hour).UnixMilli(),
			CreatedAtMs: now.Add(-40 * 24 * time.Hour).UnixMilli(),
			Importance:  2,
			Entities:    []string{"alice"},
			Embedding:   singleAxisVec(50),
		},
		{
			ID:          "ep_recent_strong",
			RoomID:      "room_1",
			Kind:        store.MemoryKindEpisodic,
			Content:     "群友约好下个月一起去崇礼滑雪",
			TEventMs:    now.Add(-1 * 24 * time.Hour).UnixMilli(),
			CreatedAtMs: now.Add(-1 * 24 * time.Hour).UnixMilli(),
			Importance:  8,
			Entities:    []string{"alice", "daliu"},
			Embedding:   singleAxisVec(60),
		},
	}
	for _, m := range extraMems {
		if err := eng.UpsertMemory(ctx, m); err != nil {
			t.Fatalf("upsert %s: %v", m.ID, err)
		}
	}

	// After cluster collapse (3 -> 1), there are 5 active memories; ActiveBudget=3 should archive the 2 weakest expired episodic rows.
	rep, err := eng.RunDreaming(ctx, "room_1", DreamingOptions{
		MergeCosineThreshold: 0.85,
		ActiveBudget:         3,
	})
	if err != nil {
		t.Fatalf("RunDreaming: %v", err)
	}
	if rep.MergedClusters != 1 || rep.SupersededCount != 2 {
		t.Fatalf("unexpected cluster merge counts: %+v", rep)
	}
	if rep.ArchivedCount != 2 || rep.ActiveRemaining != 3 {
		t.Fatalf("unexpected archive budget counts: %+v", rep)
	}
	if rep.AliasesSynced < 2 {
		t.Fatalf("expected >= 2 alias syncs (chain flatten + new nickname), got %+v", rep)
	}

	// Survivor of night-shift cluster should be night_2 (highest importance=7), promoted to semantic, with summed recall_count=6.
	survivor, ok, err := st.GetMemory(ctx, "night_2")
	if err != nil || !ok {
		t.Fatalf("GetMemory survivor: ok=%v err=%v", ok, err)
	}
	if survivor.Kind != store.MemoryKindSemantic || survivor.Importance != 7 || survivor.RecallCount != 6 {
		t.Fatalf("survivor mismatch: kind=%s imp=%d rc=%d", survivor.Kind, survivor.Importance, survivor.RecallCount)
	}
	for _, oldID := range []string{"night_1", "night_3"} {
		m, ok, err := st.GetMemory(ctx, oldID)
		if err != nil || !ok {
			t.Fatalf("GetMemory %s: ok=%v err=%v", oldID, ok, err)
		}
		if m.SupersededBy != "night_2" {
			t.Fatalf("%s.SupersededBy = %q, want night_2", oldID, m.SupersededBy)
		}
	}

	// Verify the two weakest episodic fragments were archived while durable habit & recent episodic stayed active.
	for _, archivedID := range []string{"ep_old_weak_1", "ep_old_weak_2"} {
		m, ok, err := st.GetMemory(ctx, archivedID)
		if err != nil || !ok || !m.Archived {
			t.Fatalf("expected %s archived=true, got archived=%v ok=%v err=%v", archivedID, m.Archived, ok, err)
		}
	}
	for _, activeID := range []string{"night_2", "habit_core", "ep_recent_strong"} {
		m, ok, err := st.GetMemory(ctx, activeID)
		if err != nil || !ok || m.Archived {
			t.Fatalf("expected %s active, got archived=%v ok=%v err=%v", activeID, m.Archived, ok, err)
		}
	}

	// Verify archived episodic fragment remains discoverable via ActiveRecall cold search.
	coldRes, err := eng.ActiveRecall(ctx, ActiveRecallRequest{
		RoomID:          "room_1",
		Query:           "菜鸟驿站快递",
		QueryVector:     singleAxisVec(40),
		IncludeArchived: true,
		Limit:           4,
	})
	if err != nil {
		t.Fatalf("ActiveRecall archived fragment: %v", err)
	}
	if len(coldRes.Hits) == 0 || coldRes.Hits[0].Memory.ID != "ep_old_weak_1" {
		t.Fatalf("expected archived ep_old_weak_1 via cold search, got %+v", coldRes.Hits)
	}

	// Verify aliases: transitive old_nick -> alice and timeline-discovered 早睡新昵称 -> alice.
	aliases, err := st.ListEntityAliases(ctx, "room_1")
	if err != nil {
		t.Fatalf("ListEntityAliases: %v", err)
	}
	aliasMap := make(map[string]string, len(aliases))
	for _, a := range aliases {
		aliasMap[a.Alias] = a.CanonicalEntity
	}
	if aliasMap["old_nick"] != "alice" || aliasMap["早睡新昵称"] != "alice" {
		t.Fatalf("expected normalized aliases pointing to alice, got %+v", aliasMap)
	}
}
