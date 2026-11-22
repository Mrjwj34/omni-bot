package memory

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"omni-bot/internal/ai"
	"omni-bot/internal/store"
)

const (
	DistillDuplicateCosineThreshold = float32(0.88)
	DistillTemporalCosineThreshold  = float32(0.62)
	DefaultDreamingMergeThreshold   = float32(0.85)
	DefaultActiveBudget             = 2000
)

// DistilledFact represents one boilerplate-free fact extracted from a conversation batch.
type DistilledFact struct {
	Kind       string   `json:"kind"`
	Content    string   `json:"content"`
	TEventDate string   `json:"t_event_date,omitempty"`
	TEventMs   int64    `json:"t_event_ms,omitempty"`
	Importance int      `json:"importance"`
	Entities   []string `json:"entities,omitempty"`
	Updates    string   `json:"updates,omitempty"`
}

// AliasObservation binds an observed group nickname or handle to a canonical entity anchor.
type AliasObservation struct {
	Alias           string `json:"alias"`
	CanonicalEntity string `json:"canonical_entity"`
	SenderID        string `json:"sender_id,omitempty"`
}

// DistillOutput is the structured extraction payload returned by a Distiller.
type DistillOutput struct {
	Facts   []DistilledFact    `json:"facts"`
	Aliases []AliasObservation `json:"aliases,omitempty"`
}

// Distiller extracts clean facts and entity aliases from raw timeline messages.
type Distiller interface {
	Distill(ctx context.Context, roomID string, messages []store.TimelineMessage, existingAliases []store.EntityAlias) (DistillOutput, error)
}

// TextGenerator is the minimal consumer-side subset of ai.LanguageModel needed by LLMDistiller.
type TextGenerator interface {
	Generate(ctx context.Context, req ai.GenerateRequest) (ai.GenerateResponse, error)
}

// DistillReport summarizes the mutations applied by DistillBatch.
type DistillReport struct {
	Inserted      int
	Merged        int
	Superseded    int
	AliasesSynced int
	MemoryIDs     []string
}

// DreamingOptions controls periodic background consolidation in RunDreaming.
type DreamingOptions struct {
	MergeCosineThreshold float32
	ActiveBudget         int
	DisableBudgetArchive bool
	ObservedAliases      []AliasObservation
	NowMs                int64
}

// DreamingReport summarizes consolidation outcomes from RunDreaming.
type DreamingReport struct {
	AliasesSynced   int
	MergedClusters  int
	SupersededCount int
	ArchivedCount   int
	ActiveRemaining int
}

// LLMDistiller implements Distiller using a JSON-structured prompt against ai.LanguageModel.
type LLMDistiller struct {
	model TextGenerator
	loc   *time.Location
}

func NewLLMDistiller(model TextGenerator, loc *time.Location) *LLMDistiller {
	if loc == nil {
		loc = time.UTC
	}
	return &LLMDistiller{model: model, loc: loc}
}

const distillSystemPrompt = `你负责从熟人群聊记录中提取值得长期记忆的事实与昵称别名映射。
请严格输出 JSON 对象，包含 "facts" 与 "aliases" 两个数组，遵守以下规则：
1. 每条 fact 的 content 必须是简洁自然的陈述句，严禁包含“群聊成员‘XXX’(QQ:...)于...表示”等样板前缀或原始 QQ 号。
2. 将所有相对时间（“今天”“昨天”“下周五”“上个月”等）依据每条消息的真实时间戳换算为绝对日期，写入 t_event_date (YYYY-MM-DD) 并在正文中避免悬空相对时间词。
3. 忽略一次性水群废话、纯表情或无信息量寒暄，只提取人物偏好、习惯、关系、身份变化或重要事件。
4. kind 仅限 "episodic"（具体时间事件）、"semantic"（稳定事实/关系/当前状态）、"habit"（固定偏好/习惯）；importance 为 1~10 整数。
5. entities 使用规范化实体锚点（优先复用已知别名表中的 canonical_entity）；若某人更换群昵称或出现新外号，写入 aliases。
6. 若某条事实明确替代或更新了某实体此前的状态（如换工作、换显卡、搬家、改变偏好），在 updates 字段填写被取代旧主题或旧记忆 ID，否则留空。`

var distillResponseSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"facts": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kind":         map[string]any{"type": "string", "enum": []string{"episodic", "semantic", "habit"}},
					"content":      map[string]any{"type": "string"},
					"t_event_date": map[string]any{"type": "string"},
					"importance":   map[string]any{"type": "integer"},
					"entities":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"updates":      map[string]any{"type": "string"},
				},
				"required": []string{"kind", "content", "t_event_date", "importance"},
			},
		},
		"aliases": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"alias":            map[string]any{"type": "string"},
					"canonical_entity": map[string]any{"type": "string"},
					"sender_id":        map[string]any{"type": "string"},
				},
				"required": []string{"alias", "canonical_entity"},
			},
		},
	},
	"required": []string{"facts", "aliases"},
}

func (d *LLMDistiller) Distill(ctx context.Context, roomID string, messages []store.TimelineMessage, existingAliases []store.EntityAlias) (DistillOutput, error) {
	if d == nil || d.model == nil {
		return DistillOutput{}, errors.New("memory: llm distiller requires non-nil language model")
	}
	if len(messages) == 0 {
		return DistillOutput{}, nil
	}

	var b strings.Builder
	b.WriteString("Room: ")
	b.WriteString(roomID)
	b.WriteString("\n\nKnown entity aliases:\n")
	if len(existingAliases) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, a := range existingAliases {
			fmt.Fprintf(&b, "- %s -> %s", a.Alias, a.CanonicalEntity)
			if a.SenderID != "" {
				fmt.Fprintf(&b, " (sender_id=%s)", a.SenderID)
			}
			b.WriteByte('\n')
		}
	}
	b.WriteString("\nMessages:\n")
	for _, m := range messages {
		ts := time.UnixMilli(m.TimestampMs).In(d.loc).Format("2006-01-02 15:04")
		fmt.Fprintf(&b, "[%s] %s", ts, strings.TrimSpace(m.SenderName))
		if m.SenderID != "" {
			fmt.Fprintf(&b, " (sender_id=%s)", m.SenderID)
		}
		fmt.Fprintf(&b, ": %s\n", strings.TrimSpace(m.RenderedText))
	}

	resp, err := d.model.Generate(ctx, ai.GenerateRequest{
		System: distillSystemPrompt,
		Messages: []ai.Message{
			{Role: ai.RoleUser, Parts: []ai.Part{ai.TextPart(b.String())}},
		},
		ResponseSchema:  distillResponseSchema,
		MaxOutputTokens: 2048,
	})
	if err != nil {
		return DistillOutput{}, fmt.Errorf("memory: llm distill generate: %w", err)
	}

	rawJSON := extractJSONObject(resp.Text)
	if rawJSON == "" {
		return DistillOutput{}, fmt.Errorf("memory: llm distill returned non-JSON payload")
	}
	var out DistillOutput
	if err := json.Unmarshal([]byte(rawJSON), &out); err != nil {
		return DistillOutput{}, fmt.Errorf("memory: decode llm distill json: %w", err)
	}
	return out, nil
}

// DistillBatch extracts clean, boilerplate-free facts from messages using Engine's configured Distiller,
// embeds them at 1024d, merges near-duplicates (cos >= 0.88), and links temporal superseding chains.
func (e *Engine) DistillBatch(ctx context.Context, roomID string, messages []store.TimelineMessage) (DistillReport, error) {
	return e.DistillBatchWith(ctx, roomID, messages, e.distiller)
}

// DistillBatchWith runs shallow distillation using an explicit Distiller implementation.
func (e *Engine) DistillBatchWith(ctx context.Context, roomID string, messages []store.TimelineMessage, distiller Distiller) (DistillReport, error) {
	if strings.TrimSpace(roomID) == "" {
		return DistillReport{}, errors.New("memory: distill requires non-empty room_id")
	}
	if distiller == nil {
		return DistillReport{}, errors.New("memory: distill requires non-nil distiller")
	}
	if len(messages) == 0 {
		return DistillReport{}, nil
	}

	existingAliases, err := e.store.ListEntityAliases(ctx, roomID)
	if err != nil {
		return DistillReport{}, fmt.Errorf("memory: load existing aliases: %w", err)
	}

	// Run LLM distillation outside any internal lock.
	out, err := distiller.Distill(ctx, roomID, messages, existingAliases)
	if err != nil {
		return DistillReport{}, fmt.Errorf("memory: distill batch: %w", err)
	}

	nowMs := e.nowMs()
	var report DistillReport

	// Sync LLM-reported aliases plus any observed sender_id -> new sender_name drift in this batch.
	synced, aliasIdx, err := e.syncBatchAliases(ctx, roomID, messages, existingAliases, out.Aliases, nowMs)
	if err != nil {
		return DistillReport{}, err
	}
	report.AliasesSynced = synced

	fallbackTEventMs := latestMessageTimestamp(messages, nowMs)
	type preparedFact struct {
		kind       string
		content    string
		tEventMs   int64
		importance int
		entities   []string
		updates    string
	}

	prepared := make([]preparedFact, 0, len(out.Facts))
	textsToEmbed := make([]string, 0, len(out.Facts))
	for _, raw := range out.Facts {
		clean := sanitizeFactContent(raw.Content)
		if clean == "" {
			continue
		}
		tEventMs := resolveFactTimeMs(raw, fallbackTEventMs, e.cfg.Location)
		entities := canonicalizeEntities(raw.Entities, aliasIdx)
		prepared = append(prepared, preparedFact{
			kind:       normalizeMemoryKind(raw.Kind),
			content:    clean,
			tEventMs:   tEventMs,
			importance: clampImportance(raw.Importance),
			entities:   entities,
			updates:    strings.TrimSpace(raw.Updates),
		})
		textsToEmbed = append(textsToEmbed, clean)
	}

	var vectors [][]float32
	if len(textsToEmbed) > 0 && e.embedder != nil {
		resp, err := e.embedder.Embed(ctx, ai.EmbedRequest{
			Texts:      textsToEmbed,
			Dimensions: e.cfg.VectorDim,
		})
		if err != nil {
			return DistillReport{}, fmt.Errorf("memory: embed distilled facts: %w", err)
		}
		if len(resp.Vectors) != len(textsToEmbed) {
			return DistillReport{}, fmt.Errorf("memory: embed distilled facts returned %d vectors, want %d", len(resp.Vectors), len(textsToEmbed))
		}
		vectors = resp.Vectors
	}

	for i, pf := range prepared {
		var vec []float32
		if i < len(vectors) {
			if len(vectors[i]) != e.cfg.VectorDim {
				return DistillReport{}, fmt.Errorf("memory: distilled embedding dim %d != expected %d", len(vectors[i]), e.cfg.VectorDim)
			}
			vec = vectors[i]
		}

		// Temporal update: insert the newer state and link older conflicting memories via superseded_by.
		if pf.updates != "" {
			oldIDs, err := e.findSupersededTargets(ctx, roomID, pf.updates, pf.content, pf.entities, pf.tEventMs, vec)
			if err != nil {
				return report, err
			}
			newMem := store.Memory{
				ID:          newMemoryID(),
				RoomID:      roomID,
				Kind:        pf.kind,
				Content:     pf.content,
				TEventMs:    pf.tEventMs,
				CreatedAtMs: nowMs,
				UpdatedAtMs: nowMs,
				Importance:  pf.importance,
				Entities:    pf.entities,
				Embedding:   vec,
			}
			if err := e.UpsertMemory(ctx, newMem); err != nil {
				return report, err
			}
			report.Inserted++
			report.MemoryIDs = append(report.MemoryIDs, newMem.ID)
			for _, oldID := range oldIDs {
				if oldID == newMem.ID {
					continue
				}
				if err := e.SupersedeMemory(ctx, oldID, newMem.ID); err != nil {
					return report, err
				}
				report.Superseded++
			}
			continue
		}

		// Near-duplicate check (cos >= 0.88 or exact normalized content fallback).
		dup, found, err := e.findNearDuplicate(ctx, roomID, pf.content, vec, DistillDuplicateCosineThreshold)
		if err != nil {
			return report, err
		}
		if found {
			dup.Importance = max(dup.Importance, pf.importance)
			dup.TEventMs = max(dup.TEventMs, pf.tEventMs)
			dup.UpdatedAtMs = nowMs
			dup.Entities = mergeEntities(dup.Entities, pf.entities)
			if dup.Kind == store.MemoryKindEpisodic && pf.kind != store.MemoryKindEpisodic {
				dup.Kind = pf.kind
			}
			if len([]rune(pf.content)) > len([]rune(dup.Content)) {
				dup.Content = pf.content
				if len(vec) == e.cfg.VectorDim {
					dup.Embedding = vec
				}
			}
			if err := e.UpsertMemory(ctx, dup); err != nil {
				return report, err
			}
			report.Merged++
			report.MemoryIDs = append(report.MemoryIDs, dup.ID)
			continue
		}

		newMem := store.Memory{
			ID:          newMemoryID(),
			RoomID:      roomID,
			Kind:        pf.kind,
			Content:     pf.content,
			TEventMs:    pf.tEventMs,
			CreatedAtMs: nowMs,
			UpdatedAtMs: nowMs,
			Importance:  pf.importance,
			Entities:    pf.entities,
			Embedding:   vec,
		}
		if err := e.UpsertMemory(ctx, newMem); err != nil {
			return report, err
		}
		report.Inserted++
		report.MemoryIDs = append(report.MemoryIDs, newMem.ID)
	}

	var seqIDs []int64
	for _, msg := range messages {
		if msg.SeqID > 0 {
			seqIDs = append(seqIDs, msg.SeqID)
		}
	}
	if len(seqIDs) > 0 {
		if err := e.store.MarkTimelineArchived(ctx, seqIDs); err != nil {
			return report, fmt.Errorf("memory: mark distilled timeline archived: %w", err)
		}
	}

	return report, nil
}

// RunDreaming performs periodic background consolidation for roomID:
// 1) normalizes nickname aliases into entity_aliases,
// 2) collapses near-duplicate clusters (cos >= 0.85) preserving max importance & summed recall count,
// 3) archives low-strength expired episodic fragments beyond ActiveBudget (keeping them cold-searchable).
func (e *Engine) RunDreaming(ctx context.Context, roomID string, opts DreamingOptions) (DreamingReport, error) {
	if strings.TrimSpace(roomID) == "" {
		return DreamingReport{}, errors.New("memory: dreaming requires non-empty room_id")
	}
	nowMs := opts.NowMs
	if nowMs == 0 {
		nowMs = e.nowMs()
	}
	mergeThreshold := opts.MergeCosineThreshold
	if mergeThreshold <= 0 {
		mergeThreshold = DefaultDreamingMergeThreshold
	}
	activeBudget := opts.ActiveBudget
	if activeBudget == 0 && !opts.DisableBudgetArchive {
		activeBudget = DefaultActiveBudget
	}

	var report DreamingReport

	// Step 1: Normalize nickname aliases and resolve transitive chains.
	synced, aliasIdx, err := e.normalizeRoomAliases(ctx, roomID, opts.ObservedAliases, nowMs)
	if err != nil {
		return report, err
	}
	report.AliasesSynced = synced

	// Step 2: Refresh active vectors for roomID and collapse near-duplicate clusters lock-free.
	if err := e.ReloadVectors(ctx, roomID); err != nil {
		return report, err
	}
	entries, data := e.vectors.SnapshotVectors(roomID)
	dim := e.vectors.Dim()

	order := make([]int, len(entries))
	for i := range entries {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		ea, eb := entries[a], entries[b]
		return cmp.Or(
			cmp.Compare(kindPriority(eb.Kind), kindPriority(ea.Kind)),
			cmp.Compare(eb.Importance, ea.Importance),
			cmp.Compare(eb.RecallCount, ea.RecallCount),
			cmp.Compare(eb.TEventMs, ea.TEventMs),
			cmp.Compare(ea.ID, eb.ID),
		)
	})

	visited := make([]bool, len(entries))
	for _, anchorIdx := range order {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if visited[anchorIdx] {
			continue
		}
		visited[anchorIdx] = true
		anchorVec := data[anchorIdx*dim : (anchorIdx+1)*dim]

		clusterIndices := []int{anchorIdx}
		for _, candIdx := range order {
			if visited[candIdx] {
				continue
			}
			candVec := data[candIdx*dim : (candIdx+1)*dim]
			if DotProduct(anchorVec, candVec) >= mergeThreshold {
				visited[candIdx] = true
				clusterIndices = append(clusterIndices, candIdx)
			}
		}
		if len(clusterIndices) < 2 {
			continue
		}

		members := make([]store.Memory, 0, len(clusterIndices))
		for _, idx := range clusterIndices {
			m, ok, err := e.store.GetMemory(ctx, entries[idx].ID)
			if err != nil {
				return report, fmt.Errorf("memory: dreaming load cluster member %q: %w", entries[idx].ID, err)
			}
			if !ok || m.Archived || m.SupersededBy != "" {
				continue
			}
			members = append(members, m)
		}
		if len(members) < 2 {
			continue
		}

		survivor := members[0]
		maxImportance := survivor.Importance
		sumRecall := survivor.RecallCount
		maxLastRecalled := survivor.LastRecalledMs
		maxTEvent := survivor.TEventMs
		entities := canonicalizeEntities(survivor.Entities, aliasIdx)
		mergedKind := survivor.Kind
		allEpisodic := survivor.Kind == store.MemoryKindEpisodic

		for _, other := range members[1:] {
			maxImportance = max(maxImportance, other.Importance)
			sumRecall += other.RecallCount
			maxLastRecalled = max(maxLastRecalled, other.LastRecalledMs)
			maxTEvent = max(maxTEvent, other.TEventMs)
			entities = mergeEntities(entities, canonicalizeEntities(other.Entities, aliasIdx))
			if other.Kind != store.MemoryKindEpisodic {
				allEpisodic = false
			}
			if kindPriority(other.Kind) > kindPriority(mergedKind) {
				mergedKind = other.Kind
			}
		}
		// Repeated episodic fragments consolidate into a semantic final fact.
		if allEpisodic {
			mergedKind = store.MemoryKindSemantic
		}

		survivor.Kind = mergedKind
		survivor.Importance = maxImportance
		survivor.RecallCount = sumRecall
		survivor.LastRecalledMs = maxLastRecalled
		survivor.TEventMs = maxTEvent
		survivor.Entities = entities
		survivor.UpdatedAtMs = nowMs

		if err := e.UpsertMemory(ctx, survivor); err != nil {
			return report, fmt.Errorf("memory: dreaming upsert survivor %q: %w", survivor.ID, err)
		}
		for _, other := range members[1:] {
			if err := e.SupersedeMemory(ctx, other.ID, survivor.ID); err != nil {
				return report, fmt.Errorf("memory: dreaming supersede %q -> %q: %w", other.ID, survivor.ID, err)
			}
			report.SupersededCount++
		}
		report.MergedClusters++
	}

	// Step 3: Archive low-strength expired episodic fragments beyond ActiveBudget.
	active := e.vectors.ListByRoom(roomID)
	report.ActiveRemaining = len(active)
	if !opts.DisableBudgetArchive && activeBudget > 0 && len(active) > activeBudget {
		type rankedEntry struct {
			entry    VectorEntry
			strength float64
		}
		scored := make([]rankedEntry, len(active))
		for i, entry := range active {
			st := ComputeStrength(store.Memory{
				TEventMs:       entry.TEventMs,
				CreatedAtMs:    entry.TEventMs,
				LastRecalledMs: entry.LastRecalledMs,
				Importance:     entry.Importance,
				RecallCount:    entry.RecallCount,
			}, nowMs, e.cfg.HalfLifeDays, e.cfg.PowerLawAlpha)
			scored[i] = rankedEntry{entry: entry, strength: st}
		}

		// Evict expired episodic fragments before semantic/habit memories, weakest and oldest first.
		slices.SortFunc(scored, func(a, b rankedEntry) int {
			aEp := 1
			if a.entry.Kind == store.MemoryKindEpisodic {
				aEp = 0
			}
			bEp := 1
			if b.entry.Kind == store.MemoryKindEpisodic {
				bEp = 0
			}
			return cmp.Or(
				cmp.Compare(aEp, bEp),
				cmp.Compare(a.strength, b.strength),
				cmp.Compare(a.entry.Importance, b.entry.Importance),
				cmp.Compare(a.entry.TEventMs, b.entry.TEventMs),
				cmp.Compare(a.entry.ID, b.entry.ID),
			)
		})

		excess := len(active) - activeBudget
		for i := range excess {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			if err := e.ArchiveMemory(ctx, scored[i].entry.ID); err != nil {
				return report, fmt.Errorf("memory: dreaming archive %q: %w", scored[i].entry.ID, err)
			}
			report.ArchivedCount++
			report.ActiveRemaining--
		}
	}

	return report, nil
}

func (e *Engine) syncBatchAliases(
	ctx context.Context,
	roomID string,
	messages []store.TimelineMessage,
	existing []store.EntityAlias,
	observed []AliasObservation,
	nowMs int64,
) (int, aliasIndex, error) {
	allObs := slices.Clone(observed)
	senderCanon := make(map[string]string, len(existing)+len(observed))
	for _, a := range existing {
		if a.SenderID != "" && a.CanonicalEntity != "" {
			if _, ok := senderCanon[a.SenderID]; !ok {
				senderCanon[a.SenderID] = a.CanonicalEntity
			}
		}
	}
	for _, o := range observed {
		if sid := strings.TrimSpace(o.SenderID); sid != "" && strings.TrimSpace(o.CanonicalEntity) != "" {
			if _, ok := senderCanon[sid]; !ok {
				senderCanon[sid] = strings.TrimSpace(o.CanonicalEntity)
			}
		}
	}
	for _, msg := range messages {
		sid := strings.TrimSpace(msg.SenderID)
		name := strings.TrimSpace(msg.SenderName)
		if sid == "" || name == "" {
			continue
		}
		if canon, ok := senderCanon[sid]; ok {
			allObs = append(allObs, AliasObservation{
				Alias:           name,
				CanonicalEntity: canon,
				SenderID:        sid,
			})
		}
	}
	return e.applyAliasUpserts(ctx, roomID, existing, allObs, nowMs)
}

func (e *Engine) normalizeRoomAliases(ctx context.Context, roomID string, observed []AliasObservation, nowMs int64) (int, aliasIndex, error) {
	existing, err := e.store.ListEntityAliases(ctx, roomID)
	if err != nil {
		return 0, aliasIndex{}, fmt.Errorf("memory: list aliases for dreaming: %w", err)
	}
	recentMsgs, err := e.store.ListRecentTimeline(ctx, roomID, 500)
	if err != nil {
		return 0, aliasIndex{}, fmt.Errorf("memory: list recent timeline for alias normalization: %w", err)
	}
	return e.syncBatchAliases(ctx, roomID, recentMsgs, existing, observed, nowMs)
}

func (e *Engine) applyAliasUpserts(
	ctx context.Context,
	roomID string,
	existing []store.EntityAlias,
	observations []AliasObservation,
	nowMs int64,
) (int, aliasIndex, error) {
	existingByLower := make(map[string]store.EntityAlias, len(existing))
	canonGraph := make(map[string]string, len(existing)+len(observations))
	for _, a := range existing {
		low := strings.ToLower(strings.TrimSpace(a.Alias))
		if low != "" {
			existingByLower[low] = a
			canonGraph[low] = strings.TrimSpace(a.CanonicalEntity)
		}
	}
	for _, o := range observations {
		low := strings.ToLower(strings.TrimSpace(o.Alias))
		canon := strings.TrimSpace(o.CanonicalEntity)
		if low != "" && canon != "" {
			canonGraph[low] = canon
		}
	}

	resolveRoot := func(entity string) string {
		cur := strings.TrimSpace(entity)
		for range 8 {
			next, ok := canonGraph[strings.ToLower(cur)]
			if !ok || next == "" || strings.EqualFold(next, cur) {
				break
			}
			cur = next
		}
		return cur
	}

	synced := 0
	// Collapse any transitive chains already present in entity_aliases.
	for _, a := range existing {
		root := resolveRoot(a.CanonicalEntity)
		if root != "" && root != a.CanonicalEntity {
			updated := a
			updated.CanonicalEntity = root
			updated.UpdatedAtMs = nowMs
			if err := e.store.UpsertEntityAlias(ctx, updated); err != nil {
				return synced, aliasIndex{}, fmt.Errorf("memory: normalize alias chain %q: %w", a.Alias, err)
			}
			existingByLower[strings.ToLower(strings.TrimSpace(a.Alias))] = updated
			synced++
		}
	}

	for _, o := range observations {
		alias := strings.TrimSpace(o.Alias)
		canon := resolveRoot(o.CanonicalEntity)
		sid := strings.TrimSpace(o.SenderID)
		if alias == "" || canon == "" {
			continue
		}
		low := strings.ToLower(alias)
		if prev, ok := existingByLower[low]; ok && prev.CanonicalEntity == canon && (sid == "" || prev.SenderID == sid) {
			continue
		}
		entry := store.EntityAlias{
			RoomID:          roomID,
			Alias:           alias,
			CanonicalEntity: canon,
			SenderID:        sid,
			UpdatedAtMs:     nowMs,
		}
		if prev, ok := existingByLower[low]; ok && entry.SenderID == "" {
			entry.SenderID = prev.SenderID
		}
		if err := e.store.UpsertEntityAlias(ctx, entry); err != nil {
			return synced, aliasIndex{}, fmt.Errorf("memory: upsert alias %q: %w", alias, err)
		}
		existingByLower[low] = entry
		synced++
	}

	refreshed, err := e.store.ListEntityAliases(ctx, roomID)
	if err != nil {
		return synced, aliasIndex{}, fmt.Errorf("memory: reload aliases: %w", err)
	}
	return synced, newAliasIndex(refreshed), nil
}

func (e *Engine) findNearDuplicate(ctx context.Context, roomID, content string, vec []float32, threshold float32) (store.Memory, bool, error) {
	if len(vec) == e.cfg.VectorDim {
		hits := e.vectors.TopK(roomID, vec, 3, threshold, e.cfg.VectorWorkers)
		for _, h := range hits {
			m, ok, err := e.store.GetMemory(ctx, h.ID)
			if err != nil {
				return store.Memory{}, false, fmt.Errorf("memory: load near-dup candidate %q: %w", h.ID, err)
			}
			if ok && !m.Archived && m.SupersededBy == "" {
				return m, true, nil
			}
		}
	}

	norm := normalizeCompact(content)
	if norm == "" {
		return store.Memory{}, false, nil
	}
	ftsHits, err := e.store.SearchMemoriesFTS(ctx, roomID, content, 5, false)
	if err != nil {
		return store.Memory{}, false, fmt.Errorf("memory: fts near-dup check: %w", err)
	}
	for _, h := range ftsHits {
		if h.Memory.Archived || h.Memory.SupersededBy != "" {
			continue
		}
		if normalizeCompact(h.Memory.Content) == norm {
			return h.Memory, true, nil
		}
	}
	return store.Memory{}, false, nil
}

func (e *Engine) findSupersededTargets(
	ctx context.Context,
	roomID, updatesHint, content string,
	entities []string,
	newTEventMs int64,
	vec []float32,
) ([]string, error) {
	// Direct ID reference takes precedence.
	if m, ok, err := e.store.GetMemory(ctx, updatesHint); err != nil {
		return nil, fmt.Errorf("memory: lookup update target %q: %w", updatesHint, err)
	} else if ok && m.RoomID == roomID && !m.Archived && m.SupersededBy == "" {
		return []string{m.ID}, nil
	}

	candIDs := make(map[string]bool)
	var targets []string

	matchesTimeAndEntity := func(m store.Memory) bool {
		if m.RoomID != roomID || m.Archived || m.SupersededBy != "" {
			return false
		}
		if newTEventMs > 0 && m.TEventMs > newTEventMs {
			return false
		}
		if len(entities) == 0 {
			return true
		}
		for _, ent := range entities {
			if memoryHasEntity(m, ent) {
				return true
			}
		}
		return false
	}

	if len(vec) == e.cfg.VectorDim {
		for _, vh := range e.vectors.TopK(roomID, vec, 10, DistillTemporalCosineThreshold, e.cfg.VectorWorkers) {
			m, ok, err := e.store.GetMemory(ctx, vh.ID)
			if err != nil {
				return nil, fmt.Errorf("memory: load temporal vector candidate %q: %w", vh.ID, err)
			}
			if ok && matchesTimeAndEntity(m) && !candIDs[m.ID] {
				candIDs[m.ID] = true
				targets = append(targets, m.ID)
			}
		}
	}

	if len(targets) == 0 {
		ftsHits, err := e.store.SearchMemoriesFTS(ctx, roomID, updatesHint+" "+content, 10, false)
		if err != nil {
			return nil, fmt.Errorf("memory: search temporal fts candidates: %w", err)
		}
		for _, h := range ftsHits {
			if matchesTimeAndEntity(h.Memory) && !candIDs[h.Memory.ID] {
				candIDs[h.Memory.ID] = true
				targets = append(targets, h.Memory.ID)
			}
		}
	}
	return targets, nil
}

var (
	legacyBoilerplateRE = regexp.MustCompile(
		`^群(?:聊)?成员\s*[“"「]?([^”"」()（）]+?)[”"」]?\s*[（(]\s*(?:QQ|qq)\s*[:：]?\s*\d+\s*[）)]\s*(?:于\s*\d{4}\s*[-年/]\s*\d{1,2}\s*[-月/]\s*\d{1,2}\s*日?\s*)?(?:表示|提到|称|说|发消息称|吐槽|透露)?[：:，,\s]*`,
	)
	leadingDateBracketRE = regexp.MustCompile(`^\[\d{4}-\d{2}-\d{2}\]\s*`)
)

// sanitizeFactContent strips legacy QQ-ID boilerplate prefixes and redundant date tags.
func sanitizeFactContent(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = leadingDateBracketRE.ReplaceAllString(s, "")
	if m := legacyBoilerplateRE.FindStringSubmatch(s); m != nil {
		name := strings.TrimSpace(m[1])
		rest := strings.TrimSpace(s[len(m[0]):])
		if rest == "" {
			return ""
		}
		if name != "" && !strings.Contains(rest, name) {
			s = name + rest
		} else {
			s = rest
		}
	}
	return strings.TrimSpace(s)
}

func extractJSONObject(text string) string {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		if idx := strings.IndexByte(s, '\n'); idx >= 0 {
			s = s[idx+1:]
		}
		if end := strings.LastIndex(s, "```"); end >= 0 {
			s = s[:end]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

func resolveFactTimeMs(fact DistilledFact, fallbackMs int64, loc *time.Location) int64 {
	if fact.TEventMs > 0 {
		return fact.TEventMs
	}
	if loc == nil {
		loc = time.UTC
	}
	raw := strings.TrimSpace(fact.TEventDate)
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006/01/02"} {
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return t.UnixMilli()
		}
	}
	return fallbackMs
}

func latestMessageTimestamp(messages []store.TimelineMessage, fallbackMs int64) int64 {
	var latest int64
	for _, m := range messages {
		if m.TimestampMs > latest {
			latest = m.TimestampMs
		}
	}
	if latest > 0 {
		return latest
	}
	return fallbackMs
}

func normalizeMemoryKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case store.MemoryKindSemantic:
		return store.MemoryKindSemantic
	case store.MemoryKindHabit:
		return store.MemoryKindHabit
	default:
		return store.MemoryKindEpisodic
	}
}

func clampImportance(v int) int {
	if v <= 0 {
		return 5
	}
	return min(10, max(1, v))
}

func kindPriority(kind string) int {
	switch kind {
	case store.MemoryKindHabit:
		return 3
	case store.MemoryKindSemantic:
		return 2
	default:
		return 1
	}
}

func canonicalizeEntities(entities []string, aliasIdx aliasIndex) []string {
	var out []string
	for _, raw := range entities {
		canon := aliasIdx.resolveExact(raw)
		if canon == "" || slices.Contains(out, canon) {
			continue
		}
		out = append(out, canon)
	}
	return out
}

func mergeEntities(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, raw := range list {
			s := strings.TrimSpace(raw)
			if s == "" || slices.Contains(out, s) {
				continue
			}
			out = append(out, s)
		}
	}
	return out
}

func newMemoryID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "mem_" + hex.EncodeToString(buf[:])
}
