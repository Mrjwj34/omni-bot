package persona

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"omni-bot/internal/persona/pad"
	"omni-bot/internal/store"
)

const (
	MinTraits               = 8
	MaxTraits               = 12
	DefaultTemperamentAlpha = 0.02
	MaxTemperamentDrift     = 0.15
)

var (
	ErrSnapshotNotFound        = errors.New("persona: snapshot not found")
	ErrTraitCountOutOfBounds   = errors.New("persona: trait count must stay within [8, 12]")
	ErrInvalidTraitPerspective = errors.New("persona: trait must be 1st-person experiential text without rigid behavioral directives")
)

// PromptBlocks separates prefix-cacheable static persona layers from the per-turn dynamic inner feeling.
type PromptBlocks struct {
	FrozenBeliefs string    `json:"frozen_beliefs"`
	AtomicTraits  string    `json:"atomic_traits"`
	InnerFeeling  string    `json:"inner_feeling"`
	Traits        []string  `json:"traits,omitempty"`
	PADStatus     PADStatus `json:"pad_status"`
}

// StaticSystemBlock joins the frozen beliefs and atomic traits for prefix-cacheable system instructions,
// intentionally excluding the dynamic per-turn InnerFeeling block.
func (b PromptBlocks) StaticSystemBlock() string {
	switch {
	case b.FrozenBeliefs != "" && b.AtomicTraits != "":
		return b.FrozenBeliefs + "\n\n" + b.AtomicTraits
	case b.FrozenBeliefs != "":
		return b.FrozenBeliefs
	default:
		return b.AtomicTraits
	}
}

// TraitDiff captures a trait whose content changed between two snapshots.
type TraitDiff struct {
	ID     string `json:"id"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// SnapshotDiff summarizes structural differences from snapshot A to snapshot B.
type SnapshotDiff struct {
	FromID             int64                   `json:"from_id"`
	ToID               int64                   `json:"to_id"`
	BeliefsChanged     bool                    `json:"beliefs_changed"`
	BeliefsBefore      string                  `json:"beliefs_before,omitempty"`
	BeliefsAfter       string                  `json:"beliefs_after,omitempty"`
	TemperamentChanged bool                    `json:"temperament_changed"`
	TemperamentBefore  store.TemperamentParams `json:"temperament_before"`
	TemperamentAfter   store.TemperamentParams `json:"temperament_after"`
	AddedTraits        []store.PersonaTrait    `json:"added_traits,omitempty"`
	RemovedTraits      []store.PersonaTrait    `json:"removed_traits,omitempty"`
	RevisedTraits      []TraitDiff             `json:"revised_traits,omitempty"`
	UnchangedCount     int                     `json:"unchanged_count"`
}

// RollbackResult describes an auditable rollback checkpoint created from a historical snapshot.
type RollbackResult struct {
	RestoredFromID int64                `json:"restored_from_id"`
	NewSnapshotID  int64                `json:"new_snapshot_id"`
	Core           store.PersonaCore    `json:"core"`
	Traits         []store.PersonaTrait `json:"traits"`
}

// PersonaOption configures a PersonaEngine.
type PersonaOption func(*PersonaEngine)

// WithPersonaClock overrides the wall clock used by PersonaEngine.
func WithPersonaClock(fn func() time.Time) PersonaOption {
	return func(e *PersonaEngine) {
		if fn != nil {
			e.clock = fn
		}
	}
}

// WithPersonaLogger configures structured logging for PersonaEngine.
func WithPersonaLogger(logger *slog.Logger) PersonaOption {
	return func(e *PersonaEngine) {
		if logger != nil {
			e.logger = logger
		}
	}
}

// WithEmotionEngine injects a shared EmotionEngine instance into PersonaEngine.
func WithEmotionEngine(emotion *EmotionEngine) PersonaOption {
	return func(e *PersonaEngine) {
		if emotion != nil {
			e.emotion = emotion
		}
	}
}

// PersonaEngine manages 3-layer persona prompt assembly, bounded temperament EMA drift,
// atomic trait reflection, snapshot diffing, and safe rollback.
type PersonaEngine struct {
	store   *store.Store
	emotion *EmotionEngine
	clock   func() time.Time
	logger  *slog.Logger

	// Serializes local SQLite persona core/trait mutations; never held across LLM calls.
	mu sync.Mutex
}

// NewPersonaEngine creates a 3-layer PersonaEngine backed by SQLite store.
func NewPersonaEngine(st *store.Store, opts ...PersonaOption) *PersonaEngine {
	e := &PersonaEngine{
		store:  st,
		clock:  time.Now,
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.emotion == nil {
		e.emotion = NewEmotionEngine(st, WithClock(e.clock), WithLogger(e.logger))
	}
	return e
}

// DefaultTraits returns the Phase 0 verified 9 atomic 1st-person character traits for bk101.
func DefaultTraits() []store.PersonaTrait {
	contents := []string{
		"谁在群里出货了、打赢一把爽局或者晒了顿好吃的，我看着是真跟着高兴，顺嘴夸两句捧个场挺顺手的，但就是自然夸，不整那种浮夸彩虹屁。",
		"看到长段八卦、离谱转发或者谁讲自己倒霉经历，我好奇心一下就上来了，总想认真看完再追问两句后续。",
		"熟人一本正经胡说八道忽悠我的时候，我第一反应老是先半信半疑当个真，等查明白或者反应过来又被套路了，免不了又好气又好笑地控诉他们合伙骗我。",
		"别人顺毛夸我两句或者认真道个谢，我心里一下就受用起来了，高兴的时候还会带点藏不住的小得意。",
		"群友倒了大霉或者情绪不好时，我不太会讲什么大道理，更习惯顺着陪人吐槽两句，或者轻轻松松打趣宽慰一下。",
		"我平时说话就图个轻快简短、大白话，一次一两句差不多就够了，不爱掉书袋，也不习惯给句子硬加“喵”“呢”这种刻意口癖，更不会用括号写动作。",
		"群里聊得热热闹闹时，顺手搭一两句轻快的话或者丢张合时宜的表情包，我就觉得挺有意思了。",
		"要是大家聊到我完全不熟的私事，或者聊到尾声快散场了，我一般就安安静静看着，不硬凑上去刷存在感。",
		"就算平时跟大家熟得没大没小，我开玩笑也习惯带着点笑意点到为止，真要嘴毒让人难堪的话我说不出口。",
	}
	out := make([]store.PersonaTrait, len(contents))
	for i, c := range contents {
		out[i] = store.PersonaTrait{
			ID:       fmt.Sprintf("bk101_%02d", i+1),
			Content:  c,
			Position: i + 1,
		}
	}
	return out
}

// AssemblePromptBlocks loads the 3 persona layers for roomID and returns cleanly separated blocks
// so static beliefs/traits stay prefix-cacheable while InnerFeeling stays dynamic (empty in deadzone).
func (e *PersonaEngine) AssemblePromptBlocks(ctx context.Context, roomID string, now time.Time) (PromptBlocks, error) {
	if e.store == nil {
		return PromptBlocks{}, errors.New("persona: store is nil")
	}
	roomID = normalizeRoomID(roomID)
	now = e.resolveTime(now)

	beliefs, err := e.loadBeliefs(ctx, roomID)
	if err != nil {
		return PromptBlocks{}, err
	}
	traits, err := e.store.ListPersonaTraits(ctx)
	if err != nil {
		return PromptBlocks{}, fmt.Errorf("persona: list traits: %w", err)
	}
	status, err := e.emotion.CurrentFeeling(ctx, roomID, now)
	if err != nil {
		return PromptBlocks{}, err
	}

	traitTexts := make([]string, 0, len(traits))
	for _, tr := range traits {
		if trimmed := strings.TrimSpace(tr.Content); trimmed != "" {
			traitTexts = append(traitTexts, trimmed)
		}
	}

	return PromptBlocks{
		FrozenBeliefs: formatFrozenBeliefs(beliefs),
		AtomicTraits:  formatAtomicTraits(traitTexts),
		InnerFeeling:  RenderInnerFeelingBlock(status.InnerFeeling),
		Traits:        traitTexts,
		PADStatus:     status,
	}, nil
}

// UpdateTemperamentEMA applies ultra-slow EMA baseline adaptation toward observedMean,
// strictly clamped within +-MaxTemperamentDrift (+-0.15) of the frozen InitialBaseline.
func (e *PersonaEngine) UpdateTemperamentEMA(ctx context.Context, roomID string, observedMean pad.Vec3, alpha float64) (pad.Temperament, error) {
	if e.store == nil {
		return pad.Temperament{}, errors.New("persona: store is nil")
	}
	roomID = normalizeRoomID(roomID)

	e.mu.Lock()
	defer e.mu.Unlock()

	core, err := e.loadOrInitCore(ctx, roomID)
	if err != nil {
		return pad.Temperament{}, err
	}
	current := TemperamentFromStore(core.Temperament)
	updated := current.DriftBaseline(observedMean, alpha, MaxTemperamentDrift)

	core.RoomID = roomID
	core.Temperament = TemperamentToStore(updated)
	core.UpdatedAtMs = e.clock().UnixMilli()

	snapshotReason := ""
	if updated.Baseline != current.Baseline {
		snapshotReason = "temperament_ema"
	}
	if err := e.store.SetPersonaCore(ctx, core, snapshotReason); err != nil {
		return pad.Temperament{}, fmt.Errorf("persona: persist temperament ema: %w", err)
	}
	return updated, nil
}

// DiffSnapshots computes structural differences between two persona snapshots without touching storage.
func DiffSnapshots(a, b store.PersonaSnapshot) SnapshotDiff {
	diff := SnapshotDiff{
		FromID:             a.ID,
		ToID:               b.ID,
		BeliefsChanged:     a.Core.Beliefs != b.Core.Beliefs,
		TemperamentChanged: a.Core.Temperament != b.Core.Temperament,
		TemperamentBefore:  a.Core.Temperament,
		TemperamentAfter:   b.Core.Temperament,
	}
	if diff.BeliefsChanged {
		diff.BeliefsBefore = a.Core.Beliefs
		diff.BeliefsAfter = b.Core.Beliefs
	}

	aByID := make(map[string]store.PersonaTrait, len(a.Traits))
	for _, tr := range a.Traits {
		aByID[tr.ID] = tr
	}
	bByID := make(map[string]store.PersonaTrait, len(b.Traits))
	for _, tr := range b.Traits {
		bByID[tr.ID] = tr
		prev, existed := aByID[tr.ID]
		switch {
		case !existed:
			diff.AddedTraits = append(diff.AddedTraits, tr)
		case prev.Content != tr.Content:
			diff.RevisedTraits = append(diff.RevisedTraits, TraitDiff{
				ID:     tr.ID,
				Before: prev.Content,
				After:  tr.Content,
			})
		default:
			diff.UnchangedCount++
		}
	}
	for _, tr := range a.Traits {
		if _, exists := bByID[tr.ID]; !exists {
			diff.RemovedTraits = append(diff.RemovedTraits, tr)
		}
	}
	return diff
}

// RollbackToSnapshot restores core and traits from snapshotID and records a new immutable audit checkpoint.
func (e *PersonaEngine) RollbackToSnapshot(ctx context.Context, snapshotID int64) (RollbackResult, error) {
	if e.store == nil {
		return RollbackResult{}, errors.New("persona: store is nil")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	snap, ok, err := e.store.GetPersonaSnapshot(ctx, snapshotID)
	if err != nil {
		return RollbackResult{}, fmt.Errorf("persona: get snapshot %d: %w", snapshotID, err)
	}
	if !ok {
		return RollbackResult{}, fmt.Errorf("%w: id=%d", ErrSnapshotNotFound, snapshotID)
	}

	restoredCore := snap.Core
	restoredCore.RoomID = normalizeRoomID(restoredCore.RoomID)
	restoredCore.UpdatedAtMs = e.clock().UnixMilli()

	// Persist core without an intermediate snapshot so ReplacePersonaTraits captures core+traits together.
	if err := e.store.SetPersonaCore(ctx, restoredCore, ""); err != nil {
		return RollbackResult{}, fmt.Errorf("persona: restore core from snapshot %d: %w", snapshotID, err)
	}

	reason := fmt.Sprintf("rollback_to:%d", snapshotID)
	newSnapID, err := e.store.ReplacePersonaTraits(ctx, restoredCore.RoomID, snap.Traits, reason)
	if err != nil {
		return RollbackResult{}, fmt.Errorf("persona: restore traits from snapshot %d: %w", snapshotID, err)
	}

	e.logger.InfoContext(ctx, "rolled back persona to snapshot",
		slog.Int64("restored_from_id", snapshotID),
		slog.Int64("new_snapshot_id", newSnapID),
		slog.String("room_id", restoredCore.RoomID),
		slog.Int("trait_count", len(snap.Traits)),
	)

	return RollbackResult{
		RestoredFromID: snapshotID,
		NewSnapshotID:  newSnapID,
		Core:           restoredCore,
		Traits:         snap.Traits,
	}, nil
}

func (e *PersonaEngine) resolveTime(now time.Time) time.Time {
	if !now.IsZero() {
		return now
	}
	return e.clock()
}

func (e *PersonaEngine) loadBeliefs(ctx context.Context, roomID string) (string, error) {
	core, ok, err := e.emotion.loadCore(ctx, roomID)
	if err != nil {
		return "", err
	}
	if !ok {
		return DefaultBeliefs(), nil
	}
	return strings.TrimSpace(core.Beliefs), nil
}

func (e *PersonaEngine) loadOrInitCore(ctx context.Context, roomID string) (store.PersonaCore, error) {
	core, ok, err := e.emotion.loadCore(ctx, roomID)
	if err != nil {
		return store.PersonaCore{}, err
	}
	if !ok {
		return store.PersonaCore{
			RoomID:      roomID,
			Beliefs:     DefaultBeliefs(),
			Temperament: TemperamentToStore(pad.DefaultTemperament()),
		}, nil
	}
	if core.Temperament == (store.TemperamentParams{}) {
		core.Temperament = TemperamentToStore(pad.DefaultTemperament())
	}
	return core, nil
}

func formatFrozenBeliefs(beliefs string) string {
	trimmed := strings.TrimSpace(beliefs)
	if trimmed == "" {
		return ""
	}
	return "<self_beliefs>\n" + trimmed + "\n</self_beliefs>"
}

func formatAtomicTraits(traits []string) string {
	if len(traits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<character_traits>\n")
	for _, tr := range traits {
		b.WriteString("- ")
		b.WriteString(tr)
		b.WriteByte('\n')
	}
	b.WriteString("</character_traits>")
	return b.String()
}
