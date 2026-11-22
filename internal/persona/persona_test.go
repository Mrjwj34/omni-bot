package persona

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"omni-bot/internal/ai"
	"omni-bot/internal/persona/pad"
	"omni-bot/internal/store"
)

type stubLanguageModel struct {
	reply    string
	err      error
	calls    int
	lastReq  ai.GenerateRequest
	response ai.GenerateResponse
}

func (m *stubLanguageModel) Generate(_ context.Context, req ai.GenerateRequest) (ai.GenerateResponse, error) {
	m.calls++
	m.lastReq = req
	if m.err != nil {
		return ai.GenerateResponse{}, m.err
	}
	res := m.response
	if res.Text == "" {
		res.Text = m.reply
	}
	return res, nil
}

func (m *stubLanguageModel) Capabilities() ai.Capabilities {
	return ai.Capabilities{Thinking: true}
}

func seedTestPersona(t *testing.T, st *store.Store, roomID string, traits []store.PersonaTrait, now time.Time) int64 {
	t.Helper()
	ctx := t.Context()
	core := store.PersonaCore{
		RoomID:      roomID,
		Beliefs:     DefaultBeliefs(),
		Temperament: TemperamentToStore(pad.DefaultTemperament()),
		UpdatedAtMs: now.UnixMilli(),
	}
	if err := st.SetPersonaCore(ctx, core, ""); err != nil {
		t.Fatalf("SetPersonaCore: %v", err)
	}
	snapID, err := st.ReplacePersonaTraits(ctx, roomID, traits, "seed")
	if err != nil {
		t.Fatalf("ReplacePersonaTraits: %v", err)
	}
	return snapID
}

func TestPersonaEngine_AssemblePromptBlocks(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	now := time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC)

	seedTestPersona(t, st, store.DefaultPersonaRoomID, DefaultTraits(), now)

	emotion := NewEmotionEngine(st, WithClock(func() time.Time { return now }))
	eng := NewPersonaEngine(st, WithPersonaClock(func() time.Time { return now }), WithEmotionEngine(emotion))

	// Neutral deadzone: FrozenBeliefs & AtomicTraits populated, InnerFeeling strictly empty.
	blocks, err := eng.AssemblePromptBlocks(ctx, "group:100", now)
	if err != nil {
		t.Fatalf("AssemblePromptBlocks neutral: %v", err)
	}
	if !strings.HasPrefix(blocks.FrozenBeliefs, "<self_beliefs>\n") || !strings.HasSuffix(blocks.FrozenBeliefs, "\n</self_beliefs>") {
		t.Fatalf("unexpected FrozenBeliefs format:\n%s", blocks.FrozenBeliefs)
	}
	if !strings.HasPrefix(blocks.AtomicTraits, "<character_traits>\n- ") || !strings.HasSuffix(blocks.AtomicTraits, "\n</character_traits>") {
		t.Fatalf("unexpected AtomicTraits format:\n%s", blocks.AtomicTraits)
	}
	if len(blocks.Traits) != 9 {
		t.Fatalf("traits count = %d, want 9", len(blocks.Traits))
	}
	if blocks.InnerFeeling != "" {
		t.Fatalf("expected empty InnerFeeling in deadzone, got %q", blocks.InnerFeeling)
	}
	if strings.Contains(blocks.StaticSystemBlock(), "<inner_feeling>") {
		t.Fatalf("StaticSystemBlock must never contain <inner_feeling>")
	}

	// Push emotion outside deadzone and verify dynamic <inner_feeling> appears without polluting StaticSystemBlock.
	for i := range 3 {
		if _, err := emotion.ApplyStimulus(ctx, "group:100", "r", Stimulus{P: 0.9, A: 0.85, D: 0.7}, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("ApplyStimulus: %v", err)
		}
	}
	activeBlocks, err := eng.AssemblePromptBlocks(ctx, "group:100", now.Add(5*time.Second))
	if err != nil {
		t.Fatalf("AssemblePromptBlocks active: %v", err)
	}
	if !strings.HasPrefix(activeBlocks.InnerFeeling, "<inner_feeling>\n") || !strings.HasSuffix(activeBlocks.InnerFeeling, "\n</inner_feeling>") {
		t.Fatalf("expected active <inner_feeling> block, got %q", activeBlocks.InnerFeeling)
	}
	if activeBlocks.StaticSystemBlock() != blocks.StaticSystemBlock() {
		t.Fatal("StaticSystemBlock must remain identical across emotion changes to preserve prefix caching")
	}
}

func TestPersonaEngine_UpdateTemperamentEMA_HardEnvelope(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	now := time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)

	seedTestPersona(t, st, store.DefaultPersonaRoomID, DefaultTraits(), now)
	eng := NewPersonaEngine(st, WithPersonaClock(func() time.Time { return now }))

	initial := pad.DefaultTemperament().InitialBaseline

	// Single ultra-slow step moves slightly toward observed mean without altering InitialBaseline.
	step1, err := eng.UpdateTemperamentEMA(ctx, store.DefaultPersonaRoomID, pad.Vec3{P: 1, A: 1, D: 1}, DefaultTemperamentAlpha)
	if err != nil {
		t.Fatalf("UpdateTemperamentEMA step 1: %v", err)
	}
	if step1.InitialBaseline != initial {
		t.Fatalf("InitialBaseline mutated: got %+v, want %+v", step1.InitialBaseline, initial)
	}
	if step1.Baseline.P <= initial.P || step1.Baseline.P-initial.P > 0.05 {
		t.Fatalf("unexpected small EMA step for P: initial=%f next=%f", initial.P, step1.Baseline.P)
	}

	// Hammer extreme positive observations even with alpha=1.0: strictly clamped to initial + 0.15.
	var pos pad.Temperament
	for range 50 {
		pos, err = eng.UpdateTemperamentEMA(ctx, store.DefaultPersonaRoomID, pad.Vec3{P: 1, A: 1, D: 1}, 1.0)
		if err != nil {
			t.Fatalf("UpdateTemperamentEMA extreme positive: %v", err)
		}
	}
	if pos.InitialBaseline != initial {
		t.Fatalf("InitialBaseline mutated under extreme positive drift")
	}
	for _, axis := range []struct {
		name string
		got  float64
		init float64
	}{
		{"P", pos.Baseline.P, initial.P},
		{"A", pos.Baseline.A, initial.A},
		{"D", pos.Baseline.D, initial.D},
	} {
		if math.Abs((axis.got-axis.init)-MaxTemperamentDrift) > 1e-9 {
			t.Fatalf("axis %s upper clamp = %f, want %f (init %f + %f)",
				axis.name, axis.got, axis.init+MaxTemperamentDrift, axis.init, MaxTemperamentDrift)
		}
	}

	// Hammer extreme negative observations: strictly clamped to initial - 0.15.
	var neg pad.Temperament
	for range 50 {
		neg, err = eng.UpdateTemperamentEMA(ctx, store.DefaultPersonaRoomID, pad.Vec3{P: -1, A: -1, D: -1}, 1.0)
		if err != nil {
			t.Fatalf("UpdateTemperamentEMA extreme negative: %v", err)
		}
	}
	for _, axis := range []struct {
		name string
		got  float64
		init float64
	}{
		{"P", neg.Baseline.P, initial.P},
		{"A", neg.Baseline.A, initial.A},
		{"D", neg.Baseline.D, initial.D},
	} {
		if math.Abs((axis.init-axis.got)-MaxTemperamentDrift) > 1e-9 {
			t.Fatalf("axis %s lower clamp = %f, want %f (init %f - %f)",
				axis.name, axis.got, axis.init-MaxTemperamentDrift, axis.init, MaxTemperamentDrift)
		}
	}

	// Persisted core matches final clamped temperament and frozen initial baseline.
	persisted, ok, err := st.GetPersonaCore(ctx, store.DefaultPersonaRoomID)
	if err != nil || !ok {
		t.Fatalf("GetPersonaCore: ok=%v err=%v", ok, err)
	}
	gotTemp := TemperamentFromStore(persisted.Temperament)
	if gotTemp.InitialBaseline != initial || gotTemp.Baseline != neg.Baseline {
		t.Fatalf("persisted temperament = %+v, want initial=%+v baseline=%+v", gotTemp, initial, neg.Baseline)
	}
}

func TestValidateFirstPersonTrait(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "valid 1st-person", content: "群里有人聊起深夜吃宵夜时，我总会跟着咽口水凑过去问点单攻略。", wantErr: false},
		{name: "empty", content: "   ", wantErr: true},
		{name: "missing 1st-person pronoun", content: "喜欢在群里热闹的时候顺手接梗。", wantErr: true},
		{name: "2nd-person prefix", content: "你平时说话轻快自然，我很喜欢这样。", wantErr: true},
		{name: "2nd-person description", content: "我觉得你是很会照顾气氛的人。", wantErr: true},
		{name: "rigid formatting rule bubble", content: "我说话时最多发两条气泡。", wantErr: true},
		{name: "rigid formatting rule length", content: "我每次回复都会把字数压在十个字以内。", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFirstPersonTrait(tc.content)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidTraitPerspective) {
					t.Fatalf("expected ErrInvalidTraitPerspective, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestPersonaEngine_ReflectTraits_OperationsAndCaps(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	now := time.Date(2026, 11, 22, 22, 0, 0, 0, time.UTC)

	seedTestPersona(t, st, store.DefaultPersonaRoomID, DefaultTraits(), now)
	eng := NewPersonaEngine(st, WithPersonaClock(func() time.Time { return now }))

	// 1. Empty highlights skips LLM call.
	llm := &stubLanguageModel{}
	resEmpty, err := eng.ReflectTraits(ctx, store.DefaultPersonaRoomID, []string{"  "}, llm)
	if err != nil || !resEmpty.NoChange || llm.calls != 0 {
		t.Fatalf("empty highlights = (%+v, %v, calls=%d)", resEmpty, err, llm.calls)
	}

	// 2. Explicit no_change skips DB write and snapshot creation.
	llmNoChange := &stubLanguageModel{
		reply: `{"no_change": true, "rationale": "近期记忆均为偶发玩梗，无需改动性格条目", "operations": []}`,
	}
	resNC, err := eng.ReflectTraits(ctx, store.DefaultPersonaRoomID, []string{"[11-20 ~ 11-21, recalls=2] 群里随便聊了句天气"}, llmNoChange)
	if err != nil || !resNC.NoChange || resNC.SnapshotID != 0 {
		t.Fatalf("no_change reflection = (%+v, %v)", resNC, err)
	}
	if llmNoChange.lastReq.ResponseSchema == nil || !strings.Contains(llmNoChange.lastReq.Messages[0].Parts[0].Text, "[bk101_01]") {
		t.Fatalf("reflection request missing schema or trait IDs: %+v", llmNoChange.lastReq)
	}

	// 3. Valid revise + add (wrapped in markdown fence) updates traits atomically & saves snapshot.
	llmEdit := &stubLanguageModel{
		reply: "```json\n" + `{
			"no_change": false,
			"rationale": "多次深夜一起蹲点看比赛后养成了新的凑热闹习惯",
			"operations": [
				{"op": "keep", "target_id": "bk101_01"},
				{"op": "revise", "target_id": "bk101_02", "content": "群友要是发了长段八卦、离谱转发或者赛事复盘，我好奇心一下就上来了，总想认真看完再追问两句后续。"},
				{"op": "add", "content": "碰到大家深夜一起蹲重要比赛或者发布会直播时，我很容易跟着精神起来，顺手和群友实时吐槽几句。"}
			]
		}` + "\n```",
	}
	resEdit, err := eng.ReflectTraits(ctx, store.DefaultPersonaRoomID, []string{"[11-10 ~ 11-22, recalls=9] 连续三周熬夜看比赛实时吐槽"}, llmEdit)
	if err != nil {
		t.Fatalf("ReflectTraits valid edit: %v", err)
	}
	if resEdit.NoChange || resEdit.SnapshotID <= 0 || len(resEdit.Traits) != 10 {
		t.Fatalf("unexpected edit result: %+v", resEdit)
	}
	// Untouched trait bk101_01 must remain byte-for-byte identical.
	if resEdit.Traits[0].Content != DefaultTraits()[0].Content {
		t.Fatalf("untouched trait mutated: got %q", resEdit.Traits[0].Content)
	}

	// 4. Reject 2nd-person trait without modifying DB.
	llmBadPerspective := &stubLanguageModel{
		reply: `{"no_change": false, "rationale": "bad", "operations": [{"op": "add", "content": "你是一个爱看比赛的活泼群友。"}]}`,
	}
	if _, err := eng.ReflectTraits(ctx, store.DefaultPersonaRoomID, []string{"highlight"}, llmBadPerspective); !errors.Is(err, ErrInvalidTraitPerspective) {
		t.Fatalf("expected ErrInvalidTraitPerspective, got %v", err)
	}

	// 5. Fill up to MaxTraits (12) and test cap enforcement + valid drop+add swap.
	twelveTraits := make([]store.PersonaTrait, MaxTraits)
	for i := range MaxTraits {
		twelveTraits[i] = store.PersonaTrait{
			ID:       fmt.Sprintf("tr_%02d", i+1),
			Content:  fmt.Sprintf("这是我的第 %d 条真实生活习惯。", i+1),
			Position: i + 1,
		}
	}
	if _, err := st.ReplacePersonaTraits(ctx, store.DefaultPersonaRoomID, twelveTraits, "fill_12"); err != nil {
		t.Fatalf("ReplacePersonaTraits 12: %v", err)
	}

	llmOverflow := &stubLanguageModel{
		reply: `{"no_change": false, "rationale": "add 13th", "operations": [{"op": "add", "content": "我最近特别喜欢在群里分享好听的歌。"}]}`,
	}
	if _, err := eng.ReflectTraits(ctx, store.DefaultPersonaRoomID, []string{"highlight"}, llmOverflow); !errors.Is(err, ErrTraitCountOutOfBounds) {
		t.Fatalf("expected ErrTraitCountOutOfBounds on 13th trait, got %v", err)
	}

	// Drop + add at 12 stays at 12 and succeeds!
	llmSwap := &stubLanguageModel{
		reply: `{"no_change": false, "rationale": "swap at cap", "operations": [
			{"op": "add", "content": "我最近特别喜欢在群里分享好听的纯音乐。"},
			{"op": "drop", "target_id": "tr_12"}
		]}`,
	}
	resSwap, err := eng.ReflectTraits(ctx, store.DefaultPersonaRoomID, []string{"highlight"}, llmSwap)
	if err != nil {
		t.Fatalf("drop+add swap at cap failed: %v", err)
	}
	if len(resSwap.Traits) != MaxTraits {
		t.Fatalf("trait count after swap = %d, want %d", len(resSwap.Traits), MaxTraits)
	}

	// 6. Drop below MinTraits (8) is rejected.
	eightTraits := twelveTraits[:MinTraits]
	if _, err := st.ReplacePersonaTraits(ctx, store.DefaultPersonaRoomID, eightTraits, "fill_8"); err != nil {
		t.Fatalf("ReplacePersonaTraits 8: %v", err)
	}
	llmUnderflow := &stubLanguageModel{
		reply: `{"no_change": false, "rationale": "drop to 7", "operations": [{"op": "drop", "target_id": "tr_08"}]}`,
	}
	if _, err := eng.ReflectTraits(ctx, store.DefaultPersonaRoomID, []string{"highlight"}, llmUnderflow); !errors.Is(err, ErrTraitCountOutOfBounds) {
		t.Fatalf("expected ErrTraitCountOutOfBounds on 7 traits, got %v", err)
	}
	afterUnderflow, err := st.ListPersonaTraits(ctx)
	if err != nil || len(afterUnderflow) != MinTraits {
		t.Fatalf("DB traits should remain untouched at %d, got %d (err=%v)", MinTraits, len(afterUnderflow), err)
	}
}

func TestPersonaEngine_DiffSnapshotsAndRollback(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	now := time.Date(2026, 11, 22, 23, 30, 0, 0, time.UTC)

	seedSnapID := seedTestPersona(t, st, store.DefaultPersonaRoomID, DefaultTraits(), now)
	eng := NewPersonaEngine(st, WithPersonaClock(func() time.Time { return now.Add(time.Hour) }))

	// Drift temperament and reflect traits to create evolved state.
	if _, err := eng.UpdateTemperamentEMA(ctx, store.DefaultPersonaRoomID, pad.Vec3{P: 0.9, A: 0.8, D: 0.5}, 0.5); err != nil {
		t.Fatalf("UpdateTemperamentEMA: %v", err)
	}
	llm := &stubLanguageModel{
		reply: `{"no_change": false, "rationale": "evolve traits", "operations": [
			{"op": "revise", "target_id": "bk101_01", "content": "群里谁出货了或者晒了宵夜，我总是第一个乐呵呵去捧场。"},
			{"op": "drop", "target_id": "bk101_09"},
			{"op": "add", "content": "大家聊起周末去哪儿逛吃时，我总爱凑过去记几家口碑好的小店。"}
		]}`,
	}
	refRes, err := eng.ReflectTraits(ctx, store.DefaultPersonaRoomID, []string{"多次一起讨论探店"}, llm)
	if err != nil {
		t.Fatalf("ReflectTraits: %v", err)
	}

	snapSeed, ok, err := st.GetPersonaSnapshot(ctx, seedSnapID)
	if err != nil || !ok {
		t.Fatalf("GetPersonaSnapshot seed: ok=%v err=%v", ok, err)
	}
	snapEvolved, ok, err := st.GetPersonaSnapshot(ctx, refRes.SnapshotID)
	if err != nil || !ok {
		t.Fatalf("GetPersonaSnapshot evolved: ok=%v err=%v", ok, err)
	}

	diff := DiffSnapshots(snapSeed, snapEvolved)
	if diff.FromID != seedSnapID || diff.ToID != refRes.SnapshotID {
		t.Fatalf("diff IDs = (%d, %d)", diff.FromID, diff.ToID)
	}
	if diff.BeliefsChanged {
		t.Fatal("beliefs should remain frozen")
	}
	if !diff.TemperamentChanged {
		t.Fatal("expected TemperamentChanged == true")
	}
	if len(diff.AddedTraits) != 1 || len(diff.RemovedTraits) != 1 || len(diff.RevisedTraits) != 1 || diff.UnchangedCount != 7 {
		t.Fatalf("unexpected trait diff: added=%d removed=%d revised=%d unchanged=%d",
			len(diff.AddedTraits), len(diff.RemovedTraits), len(diff.RevisedTraits), diff.UnchangedCount)
	}
	if diff.RemovedTraits[0].ID != "bk101_09" || diff.RevisedTraits[0].ID != "bk101_01" {
		t.Fatalf("unexpected diff IDs: removed=%+v revised=%+v", diff.RemovedTraits, diff.RevisedTraits)
	}

	// Rollback to seed snapshot restores both temperament and traits and writes an audit snapshot.
	rb, err := eng.RollbackToSnapshot(ctx, seedSnapID)
	if err != nil {
		t.Fatalf("RollbackToSnapshot: %v", err)
	}
	if rb.RestoredFromID != seedSnapID || rb.NewSnapshotID <= refRes.SnapshotID {
		t.Fatalf("unexpected rollback result: %+v", rb)
	}

	snapRollback, ok, err := st.GetPersonaSnapshot(ctx, rb.NewSnapshotID)
	if err != nil || !ok {
		t.Fatalf("GetPersonaSnapshot rollback: ok=%v err=%v", ok, err)
	}
	if snapRollback.Reason != fmt.Sprintf("rollback_to:%d", seedSnapID) {
		t.Fatalf("rollback reason = %q", snapRollback.Reason)
	}
	diffAfterRollback := DiffSnapshots(snapSeed, snapRollback)
	if diffAfterRollback.BeliefsChanged || diffAfterRollback.TemperamentChanged ||
		len(diffAfterRollback.AddedTraits) != 0 || len(diffAfterRollback.RemovedTraits) != 0 ||
		len(diffAfterRollback.RevisedTraits) != 0 || diffAfterRollback.UnchangedCount != len(DefaultTraits()) {
		t.Fatalf("expected zero diff after rollback, got %+v", diffAfterRollback)
	}

	// Non-existent snapshot returns ErrSnapshotNotFound.
	if _, err := eng.RollbackToSnapshot(ctx, 999999); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("expected ErrSnapshotNotFound, got %v", err)
	}
}
