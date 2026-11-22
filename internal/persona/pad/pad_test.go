package pad

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestTemperamentDecayConvergence(t *testing.T) {
	temp := DefaultTemperament()
	start := Vec3{P: 0.95, A: -0.85, D: 0.90}

	tests := []struct {
		name      string
		dt        time.Duration
		maxOffset float64
	}{
		{name: "zero_dt_unchanged", dt: 0, maxOffset: start.Distance(temp.Baseline) + 1e-9},
		{name: "negative_dt_clamped_safe", dt: -5 * time.Minute, maxOffset: start.Distance(temp.Baseline) + 1e-9},
		{name: "after_15m_partial", dt: 15 * time.Minute, maxOffset: 1.0},
		{name: "after_2h_near_baseline", dt: 2 * time.Hour, maxOffset: 0.02},
		{name: "after_12h_converged", dt: 12 * time.Hour, maxOffset: 1e-6},
	}

	prevDist := start.Distance(temp.Baseline)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := temp.Decay(start, tc.dt)
			dist := got.Distance(temp.Baseline)
			if dist > tc.maxOffset {
				t.Fatalf("Decay(%v) distance to baseline = %f, want <= %f (got %+v)", tc.dt, dist, tc.maxOffset, got)
			}
			if tc.dt > 0 && dist > prevDist+1e-9 {
				t.Fatalf("Decay(%v) increased distance from %f to %f", tc.dt, prevDist, dist)
			}
			if tc.dt > 0 {
				prevDist = dist
			}
		})
	}

	// Verify exact half-life property on axis P.
	halfP := temp.Decay(Vec3{P: temp.Baseline.P + 0.8, A: temp.Baseline.A, D: temp.Baseline.D}, temp.HalfLifeP)
	wantP := temp.Baseline.P + 0.4
	if math.Abs(halfP.P-wantP) > 1e-9 {
		t.Fatalf("after 1 half-life P = %.9f, want %.9f", halfP.P, wantP)
	}
}

func TestTemperamentStepBoundedness(t *testing.T) {
	temp := DefaultTemperament()
	now := time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		delta Vec3
		steps int
	}{
		{name: "repeated_max_positive", delta: Vec3{P: 5, A: 5, D: 5}, steps: 50},
		{name: "repeated_max_negative", delta: Vec3{P: -5, A: -5, D: -5}, steps: 50},
		{name: "nan_delta_sanitized", delta: Vec3{P: math.NaN(), A: math.NaN(), D: math.NaN()}, steps: 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := NewState(temp, now)
			for i := range tc.steps {
				state = temp.Step(state, tc.delta, now.Add(time.Duration(i+1)*10*time.Second))
				for axis, val := range map[string]float64{"P": state.Value.P, "A": state.Value.A, "D": state.Value.D} {
					if math.IsNaN(val) || val < -1.0 || val > 1.0 {
						t.Fatalf("step %d axis %s out of [-1, 1]: %v", i, axis, val)
					}
				}
			}
		})
	}
}

func TestNeutralDeadzoneSuppression(t *testing.T) {
	temp := DefaultTemperament()
	base := temp.Baseline

	tests := []struct {
		name       string
		state      Vec3
		wantActive bool
	}{
		{name: "exact_baseline", state: base, wantActive: false},
		{name: "inside_deadzone", state: base.Add(Vec3{P: 0.10, A: -0.08, D: 0.05}), wantActive: false},
		{name: "just_inside_deadzone", state: base.Add(Vec3{P: temp.DeadzoneRadius - 0.005}), wantActive: false},
		{name: "just_outside_deadzone", state: base.Add(Vec3{P: temp.DeadzoneRadius + 0.005}), wantActive: true},
		{name: "far_outside_deadzone", state: Vec3{P: -0.60, A: 0.55, D: -0.60}, wantActive: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := Match(tc.state, base, temp.DeadzoneRadius)
			if res.Active != tc.wantActive {
				t.Fatalf("Match(%+v).Active = %v, want %v (offset=%.4f, deadzone=%.4f)",
					tc.state, res.Active, tc.wantActive, res.BaselineOffset, temp.DeadzoneRadius)
			}
			feeling := RenderInnerFeeling(tc.state, temp)
			if tc.wantActive && feeling == "" {
				t.Fatalf("RenderInnerFeeling(%+v) returned empty string when active", tc.state)
			}
			if !tc.wantActive && feeling != "" {
				t.Fatalf("RenderInnerFeeling(%+v) = %q, want empty string inside deadzone", tc.state, feeling)
			}
		})
	}
}

func TestCatalogCoverageAndNoRigidScripts(t *testing.T) {
	catalog := Catalog()
	if len(catalog) < 28 || len(catalog) > 32 {
		t.Fatalf("Catalog() len = %d, want 28..32", len(catalog))
	}

	requiredOctants := map[string]bool{
		"+++": false, "++-": false, "+-+": false, "+--": false,
		"-++": false, "-+-": false, "--+": false, "---": false,
	}
	forbiddenBehavioralTerms := []string{
		"字以内", "气泡", "|||", "表情包", "必须", "不要回复", "禁止", "控制字数", "只发", "简短回复", "感叹号",
	}

	seenID := make(map[string]bool, len(catalog))
	for _, p := range catalog {
		if p.ID == "" || p.Name == "" || p.Feeling == "" {
			t.Fatalf("prototype %+v has empty required field", p)
		}
		if seenID[p.ID] {
			t.Fatalf("duplicate prototype ID %q", p.ID)
		}
		seenID[p.ID] = true

		if p.Coord.P < -1 || p.Coord.P > 1 || p.Coord.A < -1 || p.Coord.A > 1 || p.Coord.D < -1 || p.Coord.D > 1 {
			t.Fatalf("prototype %q coord %+v out of [-1, 1]^3", p.ID, p.Coord)
		}
		if _, ok := requiredOctants[p.Octant]; ok {
			requiredOctants[p.Octant] = true
		}
		if !strings.Contains(p.Feeling, "我") {
			t.Fatalf("prototype %q feeling %q must use 1st-person perspective ('我')", p.ID, p.Feeling)
		}
		for _, term := range forbiddenBehavioralTerms {
			if strings.Contains(p.Feeling, term) {
				t.Fatalf("prototype %q feeling %q contains forbidden rigid behavioral script term %q", p.ID, p.Feeling, term)
			}
		}
	}

	for oct, covered := range requiredOctants {
		if !covered {
			t.Fatalf("octant %s has no prototype coverage", oct)
		}
	}
}

func TestNearestPrototypeDeterministic(t *testing.T) {
	base := Vec3{}
	deadzone := 0.15

	tests := []struct {
		name   string
		state  Vec3
		wantID string
	}{
		{name: "exhilarated", state: Vec3{P: 0.90, A: 0.85, D: 0.50}, wantID: "exhilarated_ride"},
		{name: "smug_delight", state: Vec3{P: 0.68, A: 0.42, D: 0.88}, wantID: "smug_delight"},
		{name: "flattered_shy", state: Vec3{P: 0.65, A: 0.45, D: -0.55}, wantID: "flattered_shy"},
		{name: "indignant_tricked", state: Vec3{P: -0.55, A: 0.65, D: -0.75}, wantID: "indignant_tricked"},
		{name: "weary_detached", state: Vec3{P: -0.60, A: -0.75, D: 0.55}, wantID: "weary_detached"},
		{name: "empathic_ache", state: Vec3{P: -0.65, A: -0.45, D: -0.60}, wantID: "empathic_ache"},
		{name: "drowsy_low_battery", state: Vec3{P: 0.02, A: -0.58, D: -0.04}, wantID: "drowsy_low_battery"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first := Match(tc.state, base, deadzone)
			if !first.Active || first.Prototype.ID != tc.wantID {
				t.Fatalf("Match(%+v) = %q (active=%v), want %q", tc.state, first.Prototype.ID, first.Active, tc.wantID)
			}
			for range 10 {
				again := Match(tc.state, base, deadzone)
				if again.Prototype.ID != first.Prototype.ID {
					t.Fatalf("non-deterministic match: got %q, want %q", again.Prototype.ID, first.Prototype.ID)
				}
			}
		})
	}
}

func TestDriftBaselineEnvelopeClamping(t *testing.T) {
	temp := DefaultTemperament()
	cur := temp
	for range 200 {
		cur = cur.DriftBaseline(Vec3{P: 1, A: -1, D: 1}, 0.02, 0.15)
	}
	if cur.Baseline.P > temp.InitialBaseline.P+0.15+1e-9 {
		t.Fatalf("drifted P = %.4f exceeds initial+0.15 (%.4f)", cur.Baseline.P, temp.InitialBaseline.P+0.15)
	}
	if cur.Baseline.A < temp.InitialBaseline.A-0.15-1e-9 {
		t.Fatalf("drifted A = %.4f below initial-0.15 (%.4f)", cur.Baseline.A, temp.InitialBaseline.A-0.15)
	}
	if cur.Baseline.D > temp.InitialBaseline.D+0.15+1e-9 {
		t.Fatalf("drifted D = %.4f exceeds initial+0.15 (%.4f)", cur.Baseline.D, temp.InitialBaseline.D+0.15)
	}
}
