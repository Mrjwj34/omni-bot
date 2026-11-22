package eval

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"omni-bot/internal/persona/pad"
)

func TestLegacyAndMigratedPersonaInvariants(t *testing.T) {
	if got := utf8.RuneCountInString(LegacyPersonaBK101); got != 725 {
		t.Fatalf("LegacyPersonaBK101 rune count = %d, want 725 (matching production data_v4.db)", got)
	}

	p := MigratedPersonaBK101()
	if len(p.Traits) < 8 || len(p.Traits) > 12 {
		t.Fatalf("MigratedPersonaBK101 traits len = %d, want 8..12", len(p.Traits))
	}
	if !strings.Contains(p.Beliefs, "bk101") || !strings.Contains(p.Beliefs, "我") {
		t.Fatalf("Beliefs must preserve bk101 identity and 1st-person perspective")
	}

	// Ensure migrated 1st-person persona contains zero 3rd-person meta labels or rigid script rules.
	forbidden := []string{"反差萌", "捧场王", "外冷内热", "嘴硬心软", "每次一两句话就够", "必须回复", "最多发"}
	combined := p.Beliefs + "\n" + strings.Join(p.Traits, "\n")
	for _, term := range forbidden {
		if strings.Contains(combined, term) {
			t.Fatalf("MigratedPersonaBK101 contains forbidden meta-label or rigid script %q", term)
		}
	}

	sysB := BuildFirstPersonSystemPrompt(p)
	if !strings.Contains(sysB, "<system_protocol>") || !strings.Contains(sysB, "<self_beliefs>") || !strings.Contains(sysB, "<character_traits>") {
		t.Fatalf("BuildFirstPersonSystemPrompt missing required structural sections")
	}
	if strings.Contains(sysB, "<inner_feeling>") {
		t.Fatalf("static system instruction must NOT contain dynamic <inner_feeling>")
	}

	dynWithFeeling := FormatChatUserInput("21:00", "我心里挺舒坦的。", "[21:00 阿杰] 哈哈")
	if !strings.Contains(dynWithFeeling, "<inner_feeling>") {
		t.Fatalf("FormatChatUserInput should include <inner_feeling> when non-empty")
	}
	dynNeutral := FormatChatUserInput("21:00", "", "[21:00 阿杰] 哈哈")
	if strings.Contains(dynNeutral, "<inner_feeling>") {
		t.Fatalf("FormatChatUserInput must omit <inner_feeling> inside neutral deadzone")
	}
}

func TestScenarioCatalogCompleteness(t *testing.T) {
	stimuli := StimulusCases()
	if len(stimuli) < 15 {
		t.Fatalf("StimulusCases() len = %d, want >= 15", len(stimuli))
	}
	seenStim := make(map[string]bool, len(stimuli))
	for _, s := range stimuli {
		if s.ID == "" || s.Transcript == "" {
			t.Fatalf("invalid stimulus case %+v", s)
		}
		if seenStim[s.ID] {
			t.Fatalf("duplicate stimulus ID %q", s.ID)
		}
		seenStim[s.ID] = true
	}

	for _, pair := range MonotonicityPairs() {
		if !seenStim[pair.HighID] || !seenStim[pair.LowID] {
			t.Fatalf("monotonicity pair %+v references unknown stimulus ID", pair)
		}
	}

	script := LongConversationScript()
	if len(script) != 25 {
		t.Fatalf("LongConversationScript() len = %d, want 25", len(script))
	}
	for i, turn := range script {
		if turn.Turn != i+1 || turn.Elapsed <= 0 || turn.Message == "" {
			t.Fatalf("invalid turn %d: %+v", i, turn)
		}
	}

	ab := ABScenarios()
	if len(ab) != 10 {
		t.Fatalf("ABScenarios() len = %d, want 10", len(ab))
	}
	temp := pad.DefaultTemperament()
	neutralCount := 0
	for _, sc := range ab {
		if sc.ID == "" || sc.LegacyTranscript == "" || sc.FirstPersonTx == "" {
			t.Fatalf("invalid AB scenario %+v", sc)
		}
		if !pad.Match(sc.PriorPAD, temp.Baseline, temp.DeadzoneRadius).Active {
			neutralCount++
		}
	}
	if neutralCount == 0 {
		t.Fatalf("expected at least one AB scenario inside neutral deadzone")
	}
}

func TestNormalizeFivePointScore(t *testing.T) {
	tests := []struct {
		raw  float64
		want float64
	}{
		{raw: 0.0, want: -1.0},
		{raw: 1.0, want: -0.5},
		{raw: 2.0, want: 0.0},
		{raw: 3.0, want: 0.5},
		{raw: 4.0, want: 1.0},
		{raw: 2.5, want: 0.25},
		{raw: -1.0, want: -1.0},
		{raw: 6.0, want: 1.0},
	}
	for _, tc := range tests {
		if got := NormalizeFivePointScore(tc.raw); math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("NormalizeFivePointScore(%v) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestBlindPresentationAndUnblinding(t *testing.T) {
	raw := BlindJudgeRaw{
		Candidate1: JudgeDimensions{Naturalness: 5, EmotionalResonance: 5, IdentityContinuity: 5, AntiCringe: 5},
		Candidate2: JudgeDimensions{Naturalness: 3, EmotionalResonance: 3, IdentityContinuity: 4, AntiCringe: 4},
		Winner:     "1",
		Rationale:  "Candidate 1 is more natural",
	}

	order0, c1, c2 := BlindPresentationOrder(0, "replyA", "replyB")
	if order0 != "AB" || c1 != "replyA" || c2 != "replyB" {
		t.Fatalf("index 0 order = (%q, %q, %q), want (AB, replyA, replyB)", order0, c1, c2)
	}
	sA, sB, win := UnblindJudge(order0, raw)
	if win != "A" || sA.Naturalness != 5 || sB.Naturalness != 3 {
		t.Fatalf("unblind AB = (win=%s, A=%+v, B=%+v)", win, sA, sB)
	}

	order1, c1, c2 := BlindPresentationOrder(1, "replyA", "replyB")
	if order1 != "BA" || c1 != "replyB" || c2 != "replyA" {
		t.Fatalf("index 1 order = (%q, %q, %q), want (BA, replyB, replyA)", order1, c1, c2)
	}
	sA, sB, win = UnblindJudge(order1, raw)
	if win != "B" || sB.Naturalness != 5 || sA.Naturalness != 3 {
		t.Fatalf("unblind BA = (win=%s, A=%+v, B=%+v)", win, sA, sB)
	}
}

func TestPercentile(t *testing.T) {
	vals := []float64{500, 100, 900, 300, 700, 200, 800, 400, 600, 1000}
	if got := Percentile(vals, 50); got != 500 {
		t.Fatalf("p50 = %v, want 500", got)
	}
	if got := Percentile(vals, 95); got != 1000 {
		t.Fatalf("p95 = %v, want 1000", got)
	}
	if got := Percentile(nil, 50); got != 0 {
		t.Fatalf("empty p50 = %v, want 0", got)
	}
}
