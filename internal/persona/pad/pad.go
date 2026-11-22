package pad

import (
	"math"
	"time"
)

// Vec3 represents a point or displacement in 3D Pleasure-Arousal-Dominance space ([-1, 1]^3).
type Vec3 struct {
	P float64 `json:"p"`
	A float64 `json:"a"`
	D float64 `json:"d"`
}

func (v Vec3) Add(o Vec3) Vec3 {
	return Vec3{P: v.P + o.P, A: v.A + o.A, D: v.D + o.D}
}

func (v Vec3) Sub(o Vec3) Vec3 {
	return Vec3{P: v.P - o.P, A: v.A - o.A, D: v.D - o.D}
}

func (v Vec3) Norm() float64 {
	return math.Sqrt(v.P*v.P + v.A*v.A + v.D*v.D)
}

func (v Vec3) Distance(o Vec3) float64 {
	return v.Sub(o).Norm()
}

func (v Vec3) Clamp() Vec3 {
	return Vec3{
		P: clampUnit(v.P),
		A: clampUnit(v.A),
		D: clampUnit(v.D),
	}
}

// Temperament encodes congenital emotional dynamics for a persona.
type Temperament struct {
	InitialBaseline Vec3          `json:"initial_baseline"`
	Baseline        Vec3          `json:"baseline"`
	Sensitivity     Vec3          `json:"sensitivity"`
	HalfLifeP       time.Duration `json:"half_life_p"`
	HalfLifeA       time.Duration `json:"half_life_a"`
	HalfLifeD       time.Duration `json:"half_life_d"`
	DeadzoneRadius  float64       `json:"deadzone_radius"`
}

// DefaultTemperament returns calibrated baseline dynamics for a warm, lively close-knit group persona.
func DefaultTemperament() Temperament {
	base := Vec3{P: 0.14, A: 0.04, D: 0.02}
	return Temperament{
		InitialBaseline: base,
		Baseline:        base,
		Sensitivity:     Vec3{P: 0.32, A: 0.36, D: 0.30},
		HalfLifeP:       18 * time.Minute,
		HalfLifeA:       10 * time.Minute,
		HalfLifeD:       14 * time.Minute,
		DeadzoneRadius:  0.20,
	}
}

// State holds the current continuous PAD point and its last update timestamp.
type State struct {
	Value     Vec3      `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewState initializes a PAD state resting at the temperament baseline.
func NewState(t Temperament, now time.Time) State {
	return State{
		Value:     t.Baseline.Clamp(),
		UpdatedAt: now,
	}
}

// Decay returns the PAD vector after exponentially relaxing toward baseline over elapsed duration dt.
func (t Temperament) Decay(current Vec3, dt time.Duration) Vec3 {
	if dt <= 0 {
		return current.Clamp()
	}
	return Vec3{
		P: decayAxis(current.P, t.Baseline.P, dt, t.HalfLifeP),
		A: decayAxis(current.A, t.Baseline.A, dt, t.HalfLifeA),
		D: decayAxis(current.D, t.Baseline.D, dt, t.HalfLifeD),
	}.Clamp()
}

// Step decays the current state to now, applies a damped stimulus delta in [-1, 1]^3, and clamps to [-1, 1]^3.
func (t Temperament) Step(s State, delta Vec3, now time.Time) State {
	var dt time.Duration
	if !s.UpdatedAt.IsZero() && now.After(s.UpdatedAt) {
		dt = now.Sub(s.UpdatedAt)
	}
	decayed := t.Decay(s.Value, dt)
	clampedDelta := delta.Clamp()
	next := Vec3{
		P: decayed.P + t.Sensitivity.P*clampedDelta.P,
		A: decayed.A + t.Sensitivity.A*clampedDelta.A,
		D: decayed.D + t.Sensitivity.D*clampedDelta.D,
	}.Clamp()

	ts := now
	if ts.IsZero() {
		ts = s.UpdatedAt
	}
	return State{
		Value:     next,
		UpdatedAt: ts,
	}
}

// DriftBaseline applies a bounded slow EMA update toward recentMean, clamped within +-maxDrift of InitialBaseline.
func (t Temperament) DriftBaseline(recentMean Vec3, alpha, maxDrift float64) Temperament {
	if alpha <= 0 {
		return t
	}
	if alpha > 1 {
		alpha = 1
	}
	if maxDrift < 0 {
		maxDrift = 0
	}
	mean := recentMean.Clamp()
	next := Temperament(t)
	next.Baseline = Vec3{
		P: driftAxis(t.Baseline.P, t.InitialBaseline.P, mean.P, alpha, maxDrift),
		A: driftAxis(t.Baseline.A, t.InitialBaseline.A, mean.A, alpha, maxDrift),
		D: driftAxis(t.Baseline.D, t.InitialBaseline.D, mean.D, alpha, maxDrift),
	}
	return next
}

func decayAxis(cur, base float64, dt, halfLife time.Duration) float64 {
	if halfLife <= 0 {
		return base
	}
	factor := math.Exp(-math.Ln2 * (float64(dt) / float64(halfLife)))
	return base + (cur-base)*factor
}

func driftAxis(cur, initial, target, alpha, maxDrift float64) float64 {
	updated := cur + alpha*(target-cur)
	lo := math.Max(-1.0, initial-maxDrift)
	hi := math.Min(1.0, initial+maxDrift)
	return math.Max(lo, math.Min(hi, updated))
}

func clampUnit(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}
