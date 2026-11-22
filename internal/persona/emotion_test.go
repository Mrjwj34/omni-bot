package persona

import (
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"omni-bot/internal/ai"
	"omni-bot/internal/persona/pad"
	"omni-bot/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "persona.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("store.Close: %v", err)
		}
	})
	return st
}

func TestEmotionEngine_PersistDecayAndDeadzone(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)

	t0 := time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC)
	eng := NewEmotionEngine(st, WithClock(func() time.Time { return t0 }))

	// Fresh room starts at calibrated bk101 baseline inside neutral deadzone.
	status, err := eng.CurrentFeeling(ctx, "group:1001", t0)
	if err != nil {
		t.Fatalf("CurrentFeeling initial: %v", err)
	}
	defTemp := pad.DefaultTemperament()
	if status.State.Value != defTemp.Baseline {
		t.Fatalf("initial PAD = %+v, want baseline %+v", status.State.Value, defTemp.Baseline)
	}
	if status.Active || status.InnerFeeling != "" || status.PrototypeID != "" {
		t.Fatalf("expected neutral deadzone at baseline, got %+v", status)
	}

	// Small stimulus stays inside deadzone (offset < 0.18).
	tr1, err := eng.ApplyStimulus(ctx, "group:1001", "run-1", Stimulus{P: 0.1, A: 0.1, D: 0.0}, t0.Add(30*time.Second))
	if err != nil {
		t.Fatalf("ApplyStimulus small: %v", err)
	}
	if tr1.Active || tr1.InnerFeeling != "" || tr1.PrototypeID != "" {
		t.Fatalf("expected small stimulus to stay in deadzone, got %+v", tr1)
	}
	if tr1.HistoryID <= 0 {
		t.Fatalf("expected positive history id, got %d", tr1.HistoryID)
	}

	// Repeated strong positive high-arousal stimuli push outside deadzone and activate a prototype.
	t1 := t0.Add(60 * time.Second)
	if _, err := eng.ApplyStimulus(ctx, "group:1001", "run-2", Stimulus{P: 0.9, A: 0.9, D: 0.5}, t1); err != nil {
		t.Fatalf("ApplyStimulus boost 1: %v", err)
	}
	t2 := t1.Add(20 * time.Second)
	tr3, err := eng.ApplyStimulus(ctx, "group:1001", "run-3", Stimulus{P: 0.9, A: 0.9, D: 0.6}, t2)
	if err != nil {
		t.Fatalf("ApplyStimulus boost 2: %v", err)
	}
	if !tr3.Active || tr3.PrototypeID == "" || tr3.PrototypeLabel == "" || tr3.InnerFeeling == "" {
		t.Fatalf("expected active prototype after strong stimuli, got %+v", tr3)
	}
	if tr3.NewPAD.P <= tr3.OldPAD.P || tr3.NewPAD.A <= tr3.OldPAD.A {
		t.Fatalf("expected P and A to increase, old=%+v new=%+v", tr3.OldPAD, tr3.NewPAD)
	}
	if tr3.Delta != tr3.NewPAD.Sub(tr3.OldPAD) {
		t.Fatalf("delta = %+v, want %+v", tr3.Delta, tr3.NewPAD.Sub(tr3.OldPAD))
	}

	// Verify persistence in pad_states and pad_history.
	row, ok, err := st.GetPADState(ctx, "group:1001")
	if err != nil || !ok {
		t.Fatalf("GetPADState = (%+v, %v, %v)", row, ok, err)
	}
	if math.Abs(row.P-tr3.NewPAD.P) > 1e-9 || row.PrototypeID != tr3.PrototypeID {
		t.Fatalf("persisted state %+v does not match transition %+v", row, tr3)
	}
	hist, err := st.ListPADHistory(ctx, "group:1001", 10)
	if err != nil || len(hist) != 3 {
		t.Fatalf("ListPADHistory = (len %d, %v), want 3", len(hist), err)
	}
	if hist[0].RunID != "run-3" || hist[0].DeltaP != 0.9 || hist[0].PrototypeID != tr3.PrototypeID {
		t.Fatalf("latest history entry = %+v", hist[0])
	}

	// Read-only CurrentFeeling after 10 minutes decays significantly toward baseline without mutating DB.
	after10m, err := eng.CurrentFeeling(ctx, "group:1001", t2.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("CurrentFeeling after 10m: %v", err)
	}
	if after10m.State.Value.A >= tr3.NewPAD.A {
		t.Fatalf("expected A to decay over 10m: before=%f after=%f", tr3.NewPAD.A, after10m.State.Value.A)
	}
	rowAfterRead, ok, err := st.GetPADState(ctx, "group:1001")
	if err != nil || !ok || rowAfterRead.UpdatedAtMs != t2.UnixMilli() {
		t.Fatalf("CurrentFeeling must not mutate persisted state, got %+v", rowAfterRead)
	}

	// After 2 hours of silence, CurrentFeeling fully relaxes back inside the neutral deadzone.
	after2h, err := eng.CurrentFeeling(ctx, "group:1001", t2.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("CurrentFeeling after 2h: %v", err)
	}
	if after2h.Active || after2h.InnerFeeling != "" {
		t.Fatalf("expected relaxation back to neutral deadzone after 2h, got %+v", after2h)
	}
}

func TestEmotionEngine_BuildGateDecisionRequest(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)

	now := time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)
	eng := NewEmotionEngine(st, WithClock(func() time.Time { return now }))

	// Seed custom persona core to verify custom beliefs are reflected in <persona_self>.
	customCore := store.PersonaCore{
		RoomID:      "group:42",
		Beliefs:     "我是常驻在测试群里的熟人朋友，说话自然不端着。",
		Temperament: TemperamentToStore(pad.DefaultTemperament()),
		UpdatedAtMs: now.UnixMilli(),
	}
	if err := st.SetPersonaCore(ctx, customCore, ""); err != nil {
		t.Fatalf("SetPersonaCore: %v", err)
	}

	// Case 1: Neutral deadzone -> NO <inner_feeling>, split <earlier_context> and <latest_message>, isAtBot=false.
	reqNeutral, stNeutral, err := eng.BuildGateDecisionRequest(
		ctx,
		"group:42",
		[]string{"[20:58 阿杰] 刚下班到家", "   "},
		[]string{"[21:00 小周] 今晚吃烤冷面"},
		false,
	)
	if err != nil {
		t.Fatalf("BuildGateDecisionRequest neutral: %v", err)
	}
	if stNeutral.Value != pad.DefaultTemperament().Baseline {
		t.Fatalf("returned state = %+v, want baseline", stNeutral.Value)
	}
	if strings.Contains(reqNeutral.Input, "<inner_feeling>") {
		t.Fatalf("deadzone input must omit <inner_feeling>, got:\n%s", reqNeutral.Input)
	}
	if !strings.Contains(reqNeutral.Input, "<persona_self>\n"+customCore.Beliefs+"\n</persona_self>") {
		t.Fatalf("missing custom beliefs in input:\n%s", reqNeutral.Input)
	}
	earlierIdx := strings.Index(reqNeutral.Input, "<earlier_context>")
	latestIdx := strings.Index(reqNeutral.Input, "<latest_message>")
	if earlierIdx < 0 || latestIdx < 0 || earlierIdx >= latestIdx {
		t.Fatalf("expected <earlier_context> before <latest_message>, got:\n%s", reqNeutral.Input)
	}
	if !strings.Contains(reqNeutral.Input, "刚下班到家") || !strings.Contains(reqNeutral.Input, "今晚吃烤冷面") {
		t.Fatalf("missing message lines in input:\n%s", reqNeutral.Input)
	}
	if strings.Contains(reqNeutral.Input, "<mention_signal>") {
		t.Fatalf("unexpected <mention_signal> when isAtBot=false")
	}

	// Validate exact 4 parallel questions (1 noul + 3x5-point score with anti-double-counting instructions).
	if len(reqNeutral.Questions) != 4 {
		t.Fatalf("questions count = %d, want 4", len(reqNeutral.Questions))
	}
	qReply := reqNeutral.Questions[DecisionKeyShouldReply]
	if qReply.Kind != ai.DecisionNoul || qReply.TrueDescription == "" || qReply.FalseDescription == "" {
		t.Fatalf("invalid should_reply question: %+v", qReply)
	}
	for _, key := range []string{DecisionKeyDeltaP, DecisionKeyDeltaA, DecisionKeyDeltaD} {
		q := reqNeutral.Questions[key]
		if q.Kind != ai.DecisionScore || len(q.Criteria) != 5 {
			t.Fatalf("question %q must be 5-point score, got kind=%q len=%d", key, q.Kind, len(q.Criteria))
		}
		if !strings.Contains(q.Instructions, "<latest_message>") || !strings.Contains(q.Instructions, "<inner_feeling>") {
			t.Fatalf("question %q must explicitly isolate <latest_message> and ignore <inner_feeling>: %s", key, q.Instructions)
		}
	}

	// Case 2: Push emotion outside deadzone, test <inner_feeling> injection + isAtBot=true + omitted <earlier_context>.
	for i := range 3 {
		if _, err := eng.ApplyStimulus(ctx, "group:42", "boost", Stimulus{P: 0.9, A: 0.9, D: 0.7}, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("ApplyStimulus: %v", err)
		}
	}
	now = now.Add(5 * time.Second)
	reqActive, _, err := eng.BuildGateDecisionRequest(ctx, "group:42", nil, []string{"[21:00 老林] @bk101 快看我双黄！"}, true)
	if err != nil {
		t.Fatalf("BuildGateDecisionRequest active: %v", err)
	}
	if !strings.Contains(reqActive.Input, "<inner_feeling>") {
		t.Fatalf("active PAD state must inject <inner_feeling>, got:\n%s", reqActive.Input)
	}
	if strings.Contains(reqActive.Input, "<earlier_context>") {
		t.Fatalf("empty earlierMessages should omit <earlier_context>, got:\n%s", reqActive.Input)
	}
	if !strings.Contains(reqActive.Input, "<mention_signal>") {
		t.Fatalf("isAtBot=true must include <mention_signal>, got:\n%s", reqActive.Input)
	}

	// Case 3: Empty latestMessages rejected.
	if _, _, err := eng.BuildGateDecisionRequest(ctx, "group:42", []string{"old"}, []string{"   "}, false); err == nil {
		t.Fatal("expected error when latestMessages has no non-empty content")
	}
}

func TestParseGateDecision(t *testing.T) {
	cases := []struct {
		name    string
		res     ai.DecisionResult
		want    GateOutcome
		wantErr bool
	}{
		{
			name: "valid parallel response",
			res: ai.DecisionResult{
				Answers: map[string]ai.DecisionAnswer{
					DecisionKeyShouldReply: {Kind: ai.DecisionNoul, Probability: 0.92, Confidence: 0.88},
					DecisionKeyDeltaP:      {Kind: ai.DecisionScore, NormalizedScore: 0.65},
					DecisionKeyDeltaA:      {Kind: ai.DecisionScore, NormalizedScore: 0.40},
					DecisionKeyDeltaD:      {Kind: ai.DecisionScore, NormalizedScore: -0.55},
				},
			},
			want: GateOutcome{
				ShouldReply:           0.92,
				ShouldReplyConfidence: 0.88,
				Stimulus:              Stimulus{P: 0.65, A: 0.40, D: -0.55},
			},
		},
		{
			name: "clamps out-of-range scores",
			res: ai.DecisionResult{
				Answers: map[string]ai.DecisionAnswer{
					DecisionKeyShouldReply: {Kind: ai.DecisionNoul, Probability: 0.5, Confidence: 0.5},
					DecisionKeyDeltaP:      {Kind: ai.DecisionScore, NormalizedScore: 2.5},
					DecisionKeyDeltaA:      {Kind: ai.DecisionScore, NormalizedScore: -3.0},
					DecisionKeyDeltaD:      {Kind: ai.DecisionScore, NormalizedScore: 0.0},
				},
			},
			want: GateOutcome{
				ShouldReply:           0.5,
				ShouldReplyConfidence: 0.5,
				Stimulus:              Stimulus{P: 1, A: -1, D: 0},
			},
		},
		{
			name: "missing axis returns error",
			res: ai.DecisionResult{
				Answers: map[string]ai.DecisionAnswer{
					DecisionKeyShouldReply: {Kind: ai.DecisionNoul, Probability: 0.5},
					DecisionKeyDeltaP:      {Kind: ai.DecisionScore, NormalizedScore: 0.1},
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseGateDecision(tc.res)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ParseGateDecision = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestEmotionEngine_ConcurrentApplyStimulus(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	eng := NewEmotionEngine(st)

	base := time.Date(2026, 11, 22, 22, 0, 0, 0, time.UTC)
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := eng.ApplyStimulus(ctx, "group:race", "run", Stimulus{P: 0.2, A: 0.2, D: 0.1}, base.Add(time.Duration(idx)*time.Second))
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent ApplyStimulus failed: %v", err)
	}

	hist, err := st.ListPADHistory(ctx, "group:race", 20)
	if err != nil {
		t.Fatalf("ListPADHistory: %v", err)
	}
	if len(hist) != workers {
		t.Fatalf("history count = %d, want %d", len(hist), workers)
	}
}

func TestEmotionEngine_TemperamentFallbackAndCustomCore(t *testing.T) {
	ctx := t.Context()
	st := openTestStore(t)
	now := time.Date(2026, 11, 22, 23, 0, 0, 0, time.UTC)

	defaultCore := store.PersonaCore{
		RoomID:  store.DefaultPersonaRoomID,
		Beliefs: "我是默认核心信念。",
		Temperament: store.TemperamentParams{
			InitialBaseline: store.PADTriple{P: 0.25, A: -0.10, D: 0.05},
			Baseline:        store.PADTriple{P: 0.25, A: -0.10, D: 0.05},
			Eta:             store.PADTriple{P: 0.20, A: 0.20, D: 0.20},
			TauSec:          store.PADTriple{P: 600, A: 300, D: 600},
			Deadzone:        0.15,
		},
		UpdatedAtMs: now.UnixMilli(),
	}
	if err := st.SetPersonaCore(ctx, defaultCore, ""); err != nil {
		t.Fatalf("SetPersonaCore default: %v", err)
	}

	eng := NewEmotionEngine(st, WithClock(func() time.Time { return now }))

	// Room without custom core falls back to "default" core temperament & beliefs.
	status, err := eng.CurrentFeeling(ctx, "group:inherits-default", now)
	if err != nil {
		t.Fatalf("CurrentFeeling fallback: %v", err)
	}
	wantBase := pad.Vec3{P: 0.25, A: -0.10, D: 0.05}
	if status.Baseline != wantBase || status.State.Value != wantBase {
		t.Fatalf("inherited baseline = %+v state=%+v, want %+v", status.Baseline, status.State.Value, wantBase)
	}

	// Partial/empty temperament JSON safely falls back to calibrated bk101 defaults.
	if got := TemperamentFromStore(store.TemperamentParams{}); got != pad.DefaultTemperament() {
		t.Fatalf("zero TemperamentFromStore = %+v, want default %+v", got, pad.DefaultTemperament())
	}
}
