package persona

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"omni-bot/internal/ai"
	"omni-bot/internal/store"
)

// TraitOpKind enumerates the atomic operations emitted by a trait reflection pass.
type TraitOpKind string

const (
	TraitOpKeep   TraitOpKind = "keep"
	TraitOpAdd    TraitOpKind = "add"
	TraitOpRevise TraitOpKind = "revise"
	TraitOpDrop   TraitOpKind = "drop"
)

// TraitOperation is a single atomic edit to the character trait list.
type TraitOperation struct {
	Op       TraitOpKind `json:"op"`
	TargetID string      `json:"target_id,omitempty"`
	Content  string      `json:"content,omitempty"`
}

// ReflectionResult summarizes one single-pass strong-model trait reflection.
type ReflectionResult struct {
	NoChange   bool                 `json:"no_change"`
	Rationale  string               `json:"rationale"`
	Operations []TraitOperation     `json:"operations,omitempty"`
	Traits     []store.PersonaTrait `json:"traits"`
	SnapshotID int64                `json:"snapshot_id,omitempty"`
	Usage      ai.Usage             `json:"usage"`
}

type reflectionPlan struct {
	NoChange   bool             `json:"no_change"`
	Rationale  string           `json:"rationale"`
	Operations []TraitOperation `json:"operations"`
}

var forbiddenTraitPatterns = []string{
	"你是",
	"你会",
	"你平时",
	"你喜欢",
	"你总是",
	"你性格",
	"你习惯",
	"你应该",
	"你必须",
	"你不能",
	"气泡",
	"字数",
	"不要用 |||",
	"只发一个表情",
}

// ValidateFirstPersonTrait checks that a character trait uses true 1st-person perspective
// and contains no 2nd-person framing or rigid formatting rules.
func ValidateFirstPersonTrait(content string) error {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return fmt.Errorf("%w: empty content", ErrInvalidTraitPerspective)
	}
	if !strings.Contains(trimmed, "我") {
		return fmt.Errorf("%w: missing 1st-person pronoun \"我\"", ErrInvalidTraitPerspective)
	}
	if strings.HasPrefix(trimmed, "你") {
		return fmt.Errorf("%w: starts with 2nd-person pronoun", ErrInvalidTraitPerspective)
	}
	for _, pat := range forbiddenTraitPatterns {
		if strings.Contains(trimmed, pat) {
			return fmt.Errorf("%w: contains disallowed pattern %q", ErrInvalidTraitPerspective, pat)
		}
	}
	return nil
}

// ReflectTraits runs a single strong-model reflection pass over recent high-strength memories,
// applies atomic keep/add/revise/drop edits enforcing [8, 12] traits and 1st-person perspective,
// and atomically updates persona_traits plus persona_snapshots.
func (e *PersonaEngine) ReflectTraits(
	ctx context.Context,
	roomID string,
	recentHighlights []string,
	llm ai.LanguageModel,
) (ReflectionResult, error) {
	if e.store == nil {
		return ReflectionResult{}, errors.New("persona: store is nil")
	}
	if llm == nil {
		return ReflectionResult{}, errors.New("persona: language model is nil")
	}
	roomID = normalizeRoomID(roomID)

	highlights := cleanLines(recentHighlights)
	if len(highlights) == 0 {
		current, err := e.store.ListPersonaTraits(ctx)
		if err != nil {
			return ReflectionResult{}, fmt.Errorf("persona: list traits: %w", err)
		}
		return ReflectionResult{NoChange: true, Traits: current}, nil
	}

	beliefs, err := e.loadBeliefs(ctx, roomID)
	if err != nil {
		return ReflectionResult{}, err
	}
	currentTraits, err := e.store.ListPersonaTraits(ctx)
	if err != nil {
		return ReflectionResult{}, fmt.Errorf("persona: list traits: %w", err)
	}

	req := buildReflectionRequest(beliefs, currentTraits, highlights)
	resp, err := llm.Generate(ctx, req)
	if err != nil {
		return ReflectionResult{}, fmt.Errorf("persona: reflect generate: %w", err)
	}

	plan, err := parseReflectionPlan(resp.Text)
	if err != nil {
		return ReflectionResult{}, err
	}

	if plan.NoChange || !hasMutatingOp(plan.Operations) {
		return ReflectionResult{
			NoChange:   true,
			Rationale:  strings.TrimSpace(plan.Rationale),
			Operations: plan.Operations,
			Traits:     currentTraits,
			Usage:      resp.Usage,
		}, nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	latestTraits, err := e.store.ListPersonaTraits(ctx)
	if err != nil {
		return ReflectionResult{}, fmt.Errorf("persona: reload traits: %w", err)
	}

	nowMs := e.clock().UnixMilli()
	nextTraits, err := applyTraitOperations(latestTraits, plan.Operations, nowMs)
	if err != nil {
		return ReflectionResult{}, err
	}

	reason := formatReflectionReason(plan.Rationale)
	snapID, err := e.store.ReplacePersonaTraits(ctx, roomID, nextTraits, reason)
	if err != nil {
		return ReflectionResult{}, fmt.Errorf("persona: save reflected traits: %w", err)
	}

	e.logger.InfoContext(ctx, "reflected persona traits",
		slog.String("room_id", roomID),
		slog.Int64("snapshot_id", snapID),
		slog.Int("operations", len(plan.Operations)),
		slog.Int("trait_count", len(nextTraits)),
	)

	return ReflectionResult{
		NoChange:   false,
		Rationale:  strings.TrimSpace(plan.Rationale),
		Operations: plan.Operations,
		Traits:     nextTraits,
		SnapshotID: snapID,
		Usage:      resp.Usage,
	}, nil
}

func buildReflectionRequest(beliefs string, traits []store.PersonaTrait, highlights []string) ai.GenerateRequest {
	system := strings.TrimSpace(`你负责对熟人群聊角色的慢变「性格条目」做极度克制的周期性原子反思。
严格遵守以下原则：
1. <self_beliefs> 是绝对冻结的价值锚石，任何新条目或修订都不得违背其中的底线与身份边界。
2. 默认优先返回 no_change=true 且 operations=[]；只有当近期高强度记忆中反复出现跨越时间的真实互动习惯或态度沉淀时，才执行至多 1~2 条原子操作（keep / add / revise / drop）。
3. 未被触碰的条目必须保持原样（零改写漂移）。
4. 所有 add / revise 的条目内容必须是自然、具体、生活化的真第一人称（「我」），严禁第二人称（「你是/你会…」）、上帝视角心理学标签或字数/气泡/格式控制规则。
5. 操作完成后条目总数必须严格保持在 8 到 12 条之间；若已达 12 条上限又确实需要新增，必须同时 drop 一条较弱或重叠的旧条目。
只输出严格符合 JSON Schema 的 JSON 对象。`)

	var user strings.Builder
	user.WriteString("<self_beliefs>\n")
	user.WriteString(strings.TrimSpace(beliefs))
	user.WriteString("\n</self_beliefs>\n\n<character_traits>\n")
	for _, tr := range traits {
		fmt.Fprintf(&user, "- [%s] %s\n", tr.ID, strings.TrimSpace(tr.Content))
	}
	user.WriteString("</character_traits>\n\n<recent_high_strength_memories>\n")
	for _, h := range highlights {
		user.WriteString("- ")
		user.WriteString(h)
		user.WriteByte('\n')
	}
	user.WriteString("</recent_high_strength_memories>\n\n请审视近期高强度记忆，输出 JSON 反思决策。")

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"no_change": map[string]any{"type": "boolean"},
			"rationale": map[string]any{"type": "string"},
			"operations": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"op":        map[string]any{"type": "string", "enum": []string{"keep", "add", "revise", "drop"}},
						"target_id": map[string]any{"type": "string"},
						"content":   map[string]any{"type": "string"},
					},
					"required": []string{"op"},
				},
			},
		},
		"required": []string{"no_change", "rationale", "operations"},
	}

	return ai.GenerateRequest{
		System: system,
		Messages: []ai.Message{
			{
				Role:  ai.RoleUser,
				Parts: []ai.Part{ai.TextPart(user.String())},
			},
		},
		ResponseSchema:  schema,
		ThinkingLevel:   "low",
		MaxOutputTokens: 2048,
	}
}

func parseReflectionPlan(raw string) (reflectionPlan, error) {
	cleaned := strings.TrimSpace(raw)
	if strings.HasPrefix(cleaned, "```") {
		if idx := strings.IndexByte(cleaned, '\n'); idx >= 0 {
			cleaned = cleaned[idx+1:]
		}
		cleaned = strings.TrimSuffix(cleaned, "```")
		cleaned = strings.TrimSpace(cleaned)
	}
	var plan reflectionPlan
	if err := json.Unmarshal([]byte(cleaned), &plan); err != nil {
		return reflectionPlan{}, fmt.Errorf("persona: decode reflection json: %w", err)
	}
	for i, op := range plan.Operations {
		switch op.Op {
		case TraitOpKeep, TraitOpAdd, TraitOpRevise, TraitOpDrop:
		default:
			return reflectionPlan{}, fmt.Errorf("persona: operation %d has unknown op %q", i, op.Op)
		}
	}
	return plan, nil
}

func hasMutatingOp(ops []TraitOperation) bool {
	for _, op := range ops {
		if op.Op == TraitOpAdd || op.Op == TraitOpRevise || op.Op == TraitOpDrop {
			return true
		}
	}
	return false
}

// applyTraitOperations applies drops/revises before adds so a drop+add swap at MaxTraits stays valid.
func applyTraitOperations(current []store.PersonaTrait, ops []TraitOperation, nowMs int64) ([]store.PersonaTrait, error) {
	next := slices.Clone(current)
	usedIDs := make(map[string]struct{}, len(next)+len(ops))
	for _, tr := range next {
		usedIDs[tr.ID] = struct{}{}
	}

	indexByID := func(id string) int {
		return slices.IndexFunc(next, func(tr store.PersonaTrait) bool {
			return tr.ID == id
		})
	}

	var adds []TraitOperation
	for i, op := range ops {
		switch op.Op {
		case TraitOpKeep:
			if op.TargetID != "" && indexByID(op.TargetID) < 0 {
				return nil, fmt.Errorf("persona: keep operation %d references unknown trait %q", i, op.TargetID)
			}
		case TraitOpDrop:
			if strings.TrimSpace(op.TargetID) == "" {
				return nil, fmt.Errorf("persona: drop operation %d requires target_id", i)
			}
			idx := indexByID(op.TargetID)
			if idx < 0 {
				return nil, fmt.Errorf("persona: drop operation %d references unknown trait %q", i, op.TargetID)
			}
			next = slices.Delete(next, idx, idx+1)
		case TraitOpRevise:
			if strings.TrimSpace(op.TargetID) == "" {
				return nil, fmt.Errorf("persona: revise operation %d requires target_id", i)
			}
			if err := ValidateFirstPersonTrait(op.Content); err != nil {
				return nil, fmt.Errorf("persona: revise operation %d: %w", i, err)
			}
			idx := indexByID(op.TargetID)
			if idx < 0 {
				return nil, fmt.Errorf("persona: revise operation %d references unknown trait %q", i, op.TargetID)
			}
			next[idx].Content = strings.TrimSpace(op.Content)
			next[idx].UpdatedAtMs = nowMs
		case TraitOpAdd:
			if err := ValidateFirstPersonTrait(op.Content); err != nil {
				return nil, fmt.Errorf("persona: add operation %d: %w", i, err)
			}
			adds = append(adds, op)
		default:
			return nil, fmt.Errorf("persona: operation %d has unsupported op %q", i, op.Op)
		}
	}

	for seq, op := range adds {
		id := nextTraitID(usedIDs, nowMs, seq+1)
		usedIDs[id] = struct{}{}
		next = append(next, store.PersonaTrait{
			ID:          id,
			Content:     strings.TrimSpace(op.Content),
			CreatedAtMs: nowMs,
			UpdatedAtMs: nowMs,
		})
	}

	if len(next) < MinTraits || len(next) > MaxTraits {
		return nil, fmt.Errorf("%w: got %d", ErrTraitCountOutOfBounds, len(next))
	}

	for i := range next {
		next[i].Position = i + 1
	}
	return next, nil
}

func nextTraitID(used map[string]struct{}, nowMs int64, seq int) string {
	for attempt := seq; ; attempt++ {
		id := fmt.Sprintf("trait_%d_%d", nowMs, attempt)
		if _, exists := used[id]; !exists {
			return id
		}
	}
}

func formatReflectionReason(rationale string) string {
	r := strings.TrimSpace(rationale)
	if r == "" {
		return "reflection"
	}
	runes := []rune(r)
	if len(runes) > 80 {
		r = string(runes[:80]) + "…"
	}
	return "reflection: " + r
}
