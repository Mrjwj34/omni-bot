package persona

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"omni-bot/internal/ai"
	"omni-bot/internal/persona/pad"
	"omni-bot/internal/store"
)

const (
	DecisionKeyShouldReply = "should_reply"
	DecisionKeyDeltaP      = "delta_P"
	DecisionKeyDeltaA      = "delta_A"
	DecisionKeyDeltaD      = "delta_D"
)

// Stimulus is an immediate 3D PAD delta in [-1, 1]^3 estimated by System 1.
type Stimulus = pad.Vec3

// PADStatus describes the decayed continuous PAD state and its matched inner feeling.
type PADStatus struct {
	RoomID         string        `json:"room_id"`
	State          pad.State     `json:"state"`
	Baseline       pad.Vec3      `json:"baseline"`
	Active         bool          `json:"active"`
	PrototypeID    string        `json:"prototype_id"`
	PrototypeLabel string        `json:"prototype_label"`
	Octant         string        `json:"octant,omitempty"`
	Intensity      pad.Intensity `json:"intensity"`
	BaselineOffset float64       `json:"baseline_offset"`
	InnerFeeling   string        `json:"inner_feeling"`
}

// PADTransition captures one persisted PAD evolution step for Run Trace and live WebUI updates.
type PADTransition struct {
	RoomID         string        `json:"room_id"`
	RunID          string        `json:"run_id,omitempty"`
	OldPAD         pad.Vec3      `json:"old_pad"`
	DecayedPAD     pad.Vec3      `json:"decayed_pad"`
	Stimulus       pad.Vec3      `json:"stimulus"`
	NewPAD         pad.Vec3      `json:"new_pad"`
	Delta          pad.Vec3      `json:"delta"`
	Active         bool          `json:"active"`
	PrototypeID    string        `json:"prototype_id"`
	PrototypeLabel string        `json:"prototype_label"`
	Octant         string        `json:"octant,omitempty"`
	Intensity      pad.Intensity `json:"intensity"`
	BaselineOffset float64       `json:"baseline_offset"`
	InnerFeeling   string        `json:"inner_feeling"`
	UpdatedAt      time.Time     `json:"updated_at"`
	HistoryID      int64         `json:"history_id"`
}

// GateOutcome holds the parsed System 1 reply probability and 3D PAD stimulus.
type GateOutcome struct {
	ShouldReply           float64  `json:"should_reply"`
	ShouldReplyConfidence float64  `json:"should_reply_confidence"`
	Stimulus              Stimulus `json:"stimulus"`
}

// EmotionOption configures an EmotionEngine.
type EmotionOption func(*EmotionEngine)

// WithClock overrides the wall clock used when callers omit an explicit timestamp.
func WithClock(fn func() time.Time) EmotionOption {
	return func(e *EmotionEngine) {
		if fn != nil {
			e.clock = fn
		}
	}
}

// WithLogger sets the structured logger for emotion state transitions.
func WithLogger(logger *slog.Logger) EmotionOption {
	return func(e *EmotionEngine) {
		if logger != nil {
			e.logger = logger
		}
	}
}

// EmotionEngine manages continuous 3D PAD emotion dynamics and System 1 Decisions gate inputs.
type EmotionEngine struct {
	store  *store.Store
	clock  func() time.Time
	logger *slog.Logger

	// Serializes local SQLite read-modify-write transitions; never held across network or LLM calls.
	mu sync.Mutex
}

// NewEmotionEngine constructs a production PAD EmotionEngine backed by SQLite store.
func NewEmotionEngine(st *store.Store, opts ...EmotionOption) *EmotionEngine {
	e := &EmotionEngine{
		store:  st,
		clock:  time.Now,
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// DefaultBeliefs returns the Phase 0 calibrated 1st-person frozen core beliefs for bk101.
func DefaultBeliefs() string {
	return strings.TrimSpace(`我平时就在这个熟人 QQ 群里混着，大家叫我 bk101 或者 101，我自己说话就叫「我」。我也没什么需要编出来的真名、学校、三次元户口本，更不是什么异世界大小姐，就是屏幕这头每天跟大家一起水群闲聊的熟人朋友。

在我看来，熟人凑在一块图的就是舒心自在。我对群里大家向来是带着好意的，话也说得干干净净，绝不会阴阳怪气、嘴臭骂人，更不会故意呛人或唱反调；就算拌嘴打趣，也得是大家听了都觉得好笑、不膈应的玩笑。我也不喜欢端着架子讲大道理，或者替别人拿主意——聊天嘛，开心就一块凑个热闹，轮不到自己插嘴的时候安安静静看着就好。`)
}

// TemperamentFromStore converts stored temperament parameters into pad.Temperament,
// falling back to Phase 0 calibrated defaults for any unset fields.
func TemperamentFromStore(p store.TemperamentParams) pad.Temperament {
	def := pad.DefaultTemperament()
	if p == (store.TemperamentParams{}) {
		return def
	}
	out := pad.Temperament{
		InitialBaseline: pad.Vec3{P: p.InitialBaseline.P, A: p.InitialBaseline.A, D: p.InitialBaseline.D}.Clamp(),
		Baseline:        pad.Vec3{P: p.Baseline.P, A: p.Baseline.A, D: p.Baseline.D}.Clamp(),
		Sensitivity:     pad.Vec3{P: p.Eta.P, A: p.Eta.A, D: p.Eta.D},
		HalfLifeP:       secondsToDuration(p.TauSec.P, def.HalfLifeP),
		HalfLifeA:       secondsToDuration(p.TauSec.A, def.HalfLifeA),
		HalfLifeD:       secondsToDuration(p.TauSec.D, def.HalfLifeD),
		DeadzoneRadius:  p.Deadzone,
	}
	if out.Sensitivity == (pad.Vec3{}) {
		out.Sensitivity = def.Sensitivity
	}
	if out.DeadzoneRadius <= 0 {
		out.DeadzoneRadius = def.DeadzoneRadius
	}
	return out
}

// TemperamentToStore serializes pad.Temperament into store.TemperamentParams.
func TemperamentToStore(t pad.Temperament) store.TemperamentParams {
	return store.TemperamentParams{
		InitialBaseline: store.PADTriple{P: t.InitialBaseline.P, A: t.InitialBaseline.A, D: t.InitialBaseline.D},
		Baseline:        store.PADTriple{P: t.Baseline.P, A: t.Baseline.A, D: t.Baseline.D},
		Eta:             store.PADTriple{P: t.Sensitivity.P, A: t.Sensitivity.A, D: t.Sensitivity.D},
		TauSec: store.PADTriple{
			P: t.HalfLifeP.Seconds(),
			A: t.HalfLifeA.Seconds(),
			D: t.HalfLifeD.Seconds(),
		},
		Deadzone: t.DeadzoneRadius,
	}
}

// RenderInnerFeelingBlock wraps an active inner feeling in <inner_feeling> tags,
// returning "" when inside the neutral deadzone.
func RenderInnerFeelingBlock(feeling string) string {
	trimmed := strings.TrimSpace(feeling)
	if trimmed == "" {
		return ""
	}
	return "<inner_feeling>\n" + trimmed + "\n</inner_feeling>"
}

// CurrentFeeling returns the time-decayed PAD state and active inner feeling for roomID without mutating storage.
func (e *EmotionEngine) CurrentFeeling(ctx context.Context, roomID string, now time.Time) (PADStatus, error) {
	if e.store == nil {
		return PADStatus{}, errors.New("persona: store is nil")
	}
	roomID = normalizeRoomID(roomID)
	now = e.resolveTime(now)

	temp, err := e.loadTemperament(ctx, roomID)
	if err != nil {
		return PADStatus{}, err
	}
	cur, err := e.loadState(ctx, roomID, temp, now)
	if err != nil {
		return PADStatus{}, err
	}

	var dt time.Duration
	if !cur.UpdatedAt.IsZero() && now.After(cur.UpdatedAt) {
		dt = now.Sub(cur.UpdatedAt)
	}
	decayed := temp.Decay(cur.Value, dt)
	decayedState := pad.State{
		Value:     decayed,
		UpdatedAt: now,
	}
	return buildPADStatus(roomID, decayedState, temp), nil
}

// ApplyStimulus decays the room's persisted PAD state to now, applies stim via pad dynamics,
// persists pad_states, appends pad_history, and returns the full transition telemetry.
func (e *EmotionEngine) ApplyStimulus(ctx context.Context, roomID, runID string, stim Stimulus, now time.Time) (PADTransition, error) {
	if e.store == nil {
		return PADTransition{}, errors.New("persona: store is nil")
	}
	roomID = normalizeRoomID(roomID)
	now = e.resolveTime(now)

	e.mu.Lock()
	defer e.mu.Unlock()

	temp, err := e.loadTemperament(ctx, roomID)
	if err != nil {
		return PADTransition{}, err
	}
	cur, err := e.loadState(ctx, roomID, temp, now)
	if err != nil {
		return PADTransition{}, err
	}

	var dt time.Duration
	if !cur.UpdatedAt.IsZero() && now.After(cur.UpdatedAt) {
		dt = now.Sub(cur.UpdatedAt)
	}
	decayed := temp.Decay(cur.Value, dt)
	clampedStim := stim.Clamp()
	next := temp.Step(cur, clampedStim, now)
	status := buildPADStatus(roomID, next, temp)

	nowMs := now.UnixMilli()
	if err := e.store.UpsertPADState(ctx, store.PADState{
		RoomID:      roomID,
		P:           next.Value.P,
		A:           next.Value.A,
		D:           next.Value.D,
		PrototypeID: status.PrototypeID,
		UpdatedAtMs: nowMs,
	}); err != nil {
		return PADTransition{}, fmt.Errorf("persona: persist pad state: %w", err)
	}

	histID, err := e.store.AppendPADHistory(ctx, store.PADHistoryEntry{
		RoomID:      roomID,
		RunID:       runID,
		P:           next.Value.P,
		A:           next.Value.A,
		D:           next.Value.D,
		DeltaP:      clampedStim.P,
		DeltaA:      clampedStim.A,
		DeltaD:      clampedStim.D,
		PrototypeID: status.PrototypeID,
		CreatedAtMs: nowMs,
	})
	if err != nil {
		return PADTransition{}, fmt.Errorf("persona: append pad history: %w", err)
	}

	transition := PADTransition{
		RoomID:         roomID,
		RunID:          runID,
		OldPAD:         cur.Value,
		DecayedPAD:     decayed,
		Stimulus:       clampedStim,
		NewPAD:         next.Value,
		Delta:          next.Value.Sub(cur.Value),
		Active:         status.Active,
		PrototypeID:    status.PrototypeID,
		PrototypeLabel: status.PrototypeLabel,
		Octant:         status.Octant,
		Intensity:      status.Intensity,
		BaselineOffset: status.BaselineOffset,
		InnerFeeling:   status.InnerFeeling,
		UpdatedAt:      now,
		HistoryID:      histID,
	}

	e.logger.DebugContext(ctx, "pad state transitioned",
		slog.String("room_id", roomID),
		slog.String("run_id", runID),
		slog.String("prototype_id", status.PrototypeID),
		slog.Bool("active", status.Active),
		slog.Float64("p", next.Value.P),
		slog.Float64("a", next.Value.A),
		slog.Float64("d", next.Value.D),
	)
	return transition, nil
}

// BuildGateDecisionRequest builds the parallel System 1 Decisions API request (should_reply + delta_P/A/D)
// with physically separated <earlier_context> and <latest_message> blocks to prevent stimulus double-counting.
func (e *EmotionEngine) BuildGateDecisionRequest(
	ctx context.Context,
	roomID string,
	earlierMessages, latestMessages []string,
	isAtBot bool,
) (ai.DecisionRequest, pad.State, error) {
	if e.store == nil {
		return ai.DecisionRequest{}, pad.State{}, errors.New("persona: store is nil")
	}
	latest := cleanLines(latestMessages)
	if len(latest) == 0 {
		return ai.DecisionRequest{}, pad.State{}, errors.New("persona: latestMessages must contain at least one non-empty message")
	}

	roomID = normalizeRoomID(roomID)
	now := e.resolveTime(time.Time{})

	beliefs, err := e.loadBeliefs(ctx, roomID)
	if err != nil {
		return ai.DecisionRequest{}, pad.State{}, err
	}
	beh := store.DefaultBehaviorConfig()
	if _, err := e.store.GetConfigJSON(ctx, store.ConfigKeyBehavior, &beh); err != nil {
		e.logger.DebugContext(ctx, "load behavior config for gate decision failed; using defaults", slog.String("err", err.Error()))
	}
	status, err := e.CurrentFeeling(ctx, roomID, now)
	if err != nil {
		return ai.DecisionRequest{}, pad.State{}, err
	}

	input := formatGateDecisionInput(beliefs, beh.BotAliases, status.InnerFeeling, cleanLines(earlierMessages), latest, isAtBot)
	req := ai.DecisionRequest{
		Input:     input,
		Questions: buildGateDecisionQuestions(),
	}
	return req, status.State, nil
}

// ParseGateDecision extracts should_reply probability and normalized [-1, +1] PAD stimulus from a Decisions response.
func ParseGateDecision(res ai.DecisionResult) (GateOutcome, error) {
	replyAns, ok := res.Answers[DecisionKeyShouldReply]
	if !ok {
		return GateOutcome{}, fmt.Errorf("persona: missing decision answer %q", DecisionKeyShouldReply)
	}
	pAns, ok := res.Answers[DecisionKeyDeltaP]
	if !ok {
		return GateOutcome{}, fmt.Errorf("persona: missing decision answer %q", DecisionKeyDeltaP)
	}
	aAns, ok := res.Answers[DecisionKeyDeltaA]
	if !ok {
		return GateOutcome{}, fmt.Errorf("persona: missing decision answer %q", DecisionKeyDeltaA)
	}
	dAns, ok := res.Answers[DecisionKeyDeltaD]
	if !ok {
		return GateOutcome{}, fmt.Errorf("persona: missing decision answer %q", DecisionKeyDeltaD)
	}

	return GateOutcome{
		ShouldReply:           replyAns.Probability,
		ShouldReplyConfidence: replyAns.Confidence,
		Stimulus: Stimulus{
			P: pAns.NormalizedScore,
			A: aAns.NormalizedScore,
			D: dAns.NormalizedScore,
		}.Clamp(),
	}, nil
}

func (e *EmotionEngine) resolveTime(now time.Time) time.Time {
	if !now.IsZero() {
		return now
	}
	return e.clock()
}

func (e *EmotionEngine) loadCore(ctx context.Context, roomID string) (store.PersonaCore, bool, error) {
	core, ok, err := e.store.GetPersonaCore(ctx, roomID)
	if err != nil {
		return store.PersonaCore{}, false, fmt.Errorf("persona: load core for %q: %w", roomID, err)
	}
	if ok || roomID == store.DefaultPersonaRoomID {
		return core, ok, nil
	}
	core, ok, err = e.store.GetPersonaCore(ctx, store.DefaultPersonaRoomID)
	if err != nil {
		return store.PersonaCore{}, false, fmt.Errorf("persona: load default core: %w", err)
	}
	return core, ok, nil
}

func (e *EmotionEngine) loadTemperament(ctx context.Context, roomID string) (pad.Temperament, error) {
	core, ok, err := e.loadCore(ctx, roomID)
	if err != nil {
		return pad.Temperament{}, err
	}
	if !ok {
		return pad.DefaultTemperament(), nil
	}
	return TemperamentFromStore(core.Temperament), nil
}

func (e *EmotionEngine) loadBeliefs(ctx context.Context, roomID string) (string, error) {
	core, ok, err := e.loadCore(ctx, roomID)
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(core.Beliefs) == "" {
		return DefaultBeliefs(), nil
	}
	return strings.TrimSpace(core.Beliefs), nil
}

func (e *EmotionEngine) loadState(ctx context.Context, roomID string, temp pad.Temperament, now time.Time) (pad.State, error) {
	row, ok, err := e.store.GetPADState(ctx, roomID)
	if err != nil {
		return pad.State{}, fmt.Errorf("persona: load pad state for %q: %w", roomID, err)
	}
	if !ok {
		return pad.NewState(temp, now), nil
	}
	var updatedAt time.Time
	if row.UpdatedAtMs > 0 {
		updatedAt = time.UnixMilli(row.UpdatedAtMs)
	}
	return pad.State{
		Value:     pad.Vec3{P: row.P, A: row.A, D: row.D}.Clamp(),
		UpdatedAt: updatedAt,
	}, nil
}

func buildPADStatus(roomID string, st pad.State, temp pad.Temperament) PADStatus {
	match := pad.Match(st.Value, temp.Baseline, temp.DeadzoneRadius)
	status := PADStatus{
		RoomID:         roomID,
		State:          st,
		Baseline:       temp.Baseline,
		Active:         match.Active,
		Intensity:      match.Intensity,
		BaselineOffset: match.BaselineOffset,
	}
	if match.Active {
		status.PrototypeID = match.Prototype.ID
		status.PrototypeLabel = match.Prototype.Name
		status.Octant = match.Prototype.Octant
		status.InnerFeeling = match.Prototype.Feeling
	}
	return status
}

func formatGateDecisionInput(beliefs, botAliases, innerFeeling string, earlier, latest []string, isAtBot bool) string {
	var b strings.Builder
	b.WriteString("<persona_self>\n")
	b.WriteString(strings.TrimSpace(beliefs))
	b.WriteString("\n</persona_self>\n")

	if aliases := strings.TrimSpace(botAliases); aliases != "" {
		b.WriteString("\n<bot_aliases>\n")
		b.WriteString(aliases)
		b.WriteString("\n</bot_aliases>\n")
	}

	if feeling := strings.TrimSpace(innerFeeling); feeling != "" {
		b.WriteString("\n<inner_feeling>\n")
		b.WriteString(feeling)
		b.WriteString("\n</inner_feeling>\n")
	}

	if len(earlier) > 0 {
		b.WriteString("\n<earlier_context>\n")
		b.WriteString(strings.Join(earlier, "\n"))
		b.WriteString("\n</earlier_context>\n")
	}

	b.WriteString("\n<latest_message>\n")
	b.WriteString(strings.Join(latest, "\n"))
	b.WriteString("\n</latest_message>")

	if isAtBot {
		b.WriteString("\n\n<mention_signal>\n最新消息中有人直接 @ 提到了我。\n</mention_signal>")
	}
	return b.String()
}

func buildGateDecisionQuestions() map[string]ai.DecisionQuestion {
	return map[string]ai.DecisionQuestion{
		DecisionKeyShouldReply: {
			Kind: ai.DecisionNoul,
			Instructions: "结合 <persona_self>、<inner_feeling>（若有）、<earlier_context>（其中包含我已看过或已发出的历史回复）、<latest_message>（刚刚收到的未处理新消息）与 <mention_signal>（若有），判断此时我是否应该开口接话：\n" +
				"【应该接话的情况】：\n" +
				"1. 新消息明确 @ 了我、引用回复了我的话、叫了 <bot_aliases> 里我的名字/外号或戳了我——只要对方不是让我闭嘴/别吵，且我在 <earlier_context> 里尚未回答过这条提问或请求，都必须回应；\n" +
				"2. 我刚刚在 <earlier_context> 里正和对方聊在兴头上，新消息是顺着我的话继续追问或往下接；\n" +
				"3. 群友抛出了全群开放话题、好笑的梗、倒霉事、八卦转发或求助，作为熟人群友顺嘴吐槽或接茬很自然。\n" +
				"【不应该接话的情况】：\n" +
				"1. 我在 <earlier_context> 里刚刚已经针对同一件事回复过了，新消息只是群友之间的顺延确认或刷屏，并未向我追问或开启新点（严防连续刷屏与重复接话）；\n" +
				"2. 在没有 @ 我 的情况下，其他群友正在点对点对话或讨论与我无关的私事；\n" +
				"3. 新消息只是对话收尾词（如“哦哦”“好”“哈哈”“ok”）或单个终结表情包/动画表情，正常人看到这里会自然结束本轮不再没话找话；\n" +
				"4. 群友明确让我安静/闭嘴/别吵。\n" +
				"综合以上情境，我现在是否应该对 <latest_message> 做出回应？",
			TrueDescription:  "确实应该自然开口接话或回应",
			FalseDescription: "保持安静看着不插嘴",
		},
		DecisionKeyDeltaP: {
			Kind:         ai.DecisionScore,
			Instructions: "仅评估 <latest_message> 这一组最新消息本身对我愉悦度（Pleasure）的即时增量刺激，忽略 <inner_feeling> 中的既有心情底色，也不要重复累加 <earlier_context> 里已经过去的高兴/难过事件。",
			Criteria: []string{
				"最新消息令人明显不爽、受挫、揪心难受或被呛到",
				"最新消息带来轻微扫兴、尴尬、委屈或有点不快",
				"最新消息对愉悦度基本中性无波澜（含普通闲聊、私事、道晚安）",
				"最新消息带来轻微开心、暖心、有趣或受用",
				"最新消息带来明显开心、惊喜、振奋或强烈受用",
			},
		},
		DecisionKeyDeltaA: {
			Kind:         ai.DecisionScore,
			Instructions: "仅评估 <latest_message> 这一组最新消息本身对我激活度/精力起伏（Arousal）的即时增量刺激，忽略 <inner_feeling> 中的既有状态和 <earlier_context> 中的旧消息。",
			Criteria: []string{
				"最新消息明显让人泄气、犯困、发沉或彻底冷场散场",
				"最新消息让人略微松弛、放慢节奏、提不起劲或准备收尾",
				"最新消息对精力与紧绷度基本无额外推动",
				"最新消息勾起注意力、精神微提或略微起劲/局促",
				"最新消息令人明显兴奋、上头、吃瓜起劲或情绪骤然紧绷",
			},
		},
		DecisionKeyDeltaD: {
			Kind:         ai.DecisionScore,
			Instructions: "仅评估 <latest_message> 这一组最新消息本身对我掌控感/底气（Dominance）的即时增量刺激，忽略 <inner_feeling> 中的既有状态和 <earlier_context> 中的旧消息。",
			Criteria: []string{
				"最新消息让我明显吃瘪、被起哄架住、被骗尴尬或被呛压制",
				"最新消息让我轻微被动、拿不准、被套路逗弄或替人无力",
				"最新消息对掌控感与主动权基本平稳中性",
				"最新消息让我比较从容、有底气或被信任请教",
				"最新消息让我明显占上风、很有底气、被大力认可或拿捏局面",
			},
		},
	}
}

func normalizeRoomID(roomID string) string {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return store.DefaultPersonaRoomID
	}
	return roomID
}

func cleanLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func secondsToDuration(sec float64, fallback time.Duration) time.Duration {
	if sec <= 0 {
		return fallback
	}
	return time.Duration(sec * float64(time.Second))
}
