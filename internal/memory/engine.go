package memory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"omni-bot/internal/ai"
	"omni-bot/internal/store"
)

const (
	dayMillis = float64(24 * time.Hour / time.Millisecond)

	defaultHalfLifeDays             = 14.0
	defaultPowerLawAlpha            = 0.85
	defaultVectorWeight             = 0.75
	defaultPassiveTopK              = 4
	defaultPassiveMinVectorSim      = float32(0.50)
	defaultPassiveSurfaceThreshold  = 0.54
	defaultInWindowOverlapThreshold = 0.65
	defaultActiveLimit              = 8
	defaultActiveMinVectorSim       = float32(0.46)
	defaultActiveSurfaceThreshold   = 0.40
	defaultReinforceCooldown        = 30 * time.Minute
)

// Embedder produces 1024-dim embeddings for queries and distilled memory texts.
type Embedder interface {
	Embed(ctx context.Context, req ai.EmbedRequest) (ai.EmbedResponse, error)
}

// Config configures the production memory retrieval and reinforcement engine.
// Zero values fall back to the Phase 0 empirically validated KISS working points.
type Config struct {
	VectorDim                int
	VectorWorkers            int
	HalfLifeDays             float64
	PowerLawAlpha            float64
	VectorWeight             float64
	PassiveTopK              int
	PassiveMinVectorSim      float32
	PassiveSurfaceThreshold  float64
	InWindowOverlapThreshold float64
	ActiveDefaultLimit       int
	ActiveMinVectorSim       float32
	ActiveSurfaceThreshold   float64
	ReinforceCooldown        time.Duration
	Distiller                Distiller
	Location                 *time.Location
	Clock                    func() time.Time
	Logger                   *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.VectorDim <= 0 {
		c.VectorDim = DefaultVectorDim
	}
	if c.VectorWorkers <= 0 {
		c.VectorWorkers = DefaultVectorWorkers
	}
	if c.HalfLifeDays <= 0 {
		c.HalfLifeDays = defaultHalfLifeDays
	}
	if c.PowerLawAlpha <= 0 {
		c.PowerLawAlpha = defaultPowerLawAlpha
	}
	if c.VectorWeight <= 0 {
		c.VectorWeight = defaultVectorWeight
	}
	if c.PassiveTopK <= 0 {
		c.PassiveTopK = defaultPassiveTopK
	}
	if c.PassiveMinVectorSim <= 0 {
		c.PassiveMinVectorSim = defaultPassiveMinVectorSim
	}
	if c.PassiveSurfaceThreshold <= 0 {
		c.PassiveSurfaceThreshold = defaultPassiveSurfaceThreshold
	}
	if c.InWindowOverlapThreshold <= 0 {
		c.InWindowOverlapThreshold = defaultInWindowOverlapThreshold
	}
	if c.ActiveDefaultLimit <= 0 {
		c.ActiveDefaultLimit = defaultActiveLimit
	}
	if c.ActiveMinVectorSim <= 0 {
		c.ActiveMinVectorSim = defaultActiveMinVectorSim
	}
	if c.ActiveSurfaceThreshold <= 0 {
		c.ActiveSurfaceThreshold = defaultActiveSurfaceThreshold
	}
	if c.ReinforceCooldown <= 0 {
		c.ReinforceCooldown = defaultReinforceCooldown
	}
	if c.Location == nil {
		c.Location = time.UTC
	}
	if c.Clock == nil {
		c.Clock = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// PassiveRecallRequest configures always-on associative recall during OnAssemble.
// Callers may supply QueryVector precomputed concurrently with OnGate decisions.
type PassiveRecallRequest struct {
	RoomID       string
	Speaker      string
	SpeakerID    string
	Query        string
	QueryVector  []float32
	RecentWindow []string
	TopK         int
	NowMs        int64
}

// ActiveRecallRequest configures explicit tool-driven memory_recall queries.
type ActiveRecallRequest struct {
	RoomID          string
	Speaker         string
	SpeakerID       string
	Query           string
	QueryVector     []float32
	Entity          string
	TimeFrom        int64
	TimeTo          int64
	IncludeArchived bool
	Limit           int
	NowMs           int64
}

// RecallHit holds one surfaced memory along with its score breakdown and formatted line.
type RecallHit struct {
	Memory      store.Memory `json:"memory"`
	Score       float64      `json:"score"`
	Strength    float64      `json:"strength"`
	FTSScore    float64      `json:"fts_score,omitempty"`
	VectorSim   float32      `json:"vector_sim,omitempty"`
	EntityBoost float64      `json:"entity_boost,omitempty"`
	MatchedCues []string     `json:"matched_cues,omitempty"`
	Formatted   string       `json:"formatted"`
}

// HitTrace is the compact Run Trace payload serialized into store.RunTrace.MemoriesJSON.
type HitTrace struct {
	MemoryID    string   `json:"memory_id"`
	Kind        string   `json:"kind"`
	TEventDate  string   `json:"t_event_date"`
	Content     string   `json:"content"`
	Score       float64  `json:"score"`
	Strength    float64  `json:"strength"`
	FTSScore    float64  `json:"fts_score,omitempty"`
	VectorSim   float32  `json:"vector_sim,omitempty"`
	EntityBoost float64  `json:"entity_boost,omitempty"`
	MatchedCues []string `json:"matched_cues,omitempty"`
	Archived    bool     `json:"archived,omitempty"`
}

// RecallResult bundles surfaced hits, the prompt-ready <associated_memories> block,
// and structured Run Trace metadata.
type RecallResult struct {
	Hits           []RecallHit   `json:"hits"`
	FormattedBlock string        `json:"formatted_block"`
	Trace          []HitTrace    `json:"trace"`
	SkippedInWin   int           `json:"skipped_in_window,omitempty"`
	Latency        time.Duration `json:"latency"`
}

// Engine coordinates SQLite WAL persistence, the in-memory 1024d VectorCache,
// hybrid passive/active recall, anti-Matthew reinforcement, distillation, and Dreaming.
type Engine struct {
	store     *store.Store
	embedder  Embedder
	distiller Distiller
	vectors   *VectorCache
	cfg       Config
}

// NewEngine initializes the production memory engine and hydrates the 1024d hot vector cache from SQLite.
func NewEngine(ctx context.Context, st *store.Store, embedder Embedder, cfg Config) (*Engine, error) {
	if st == nil {
		return nil, errors.New("memory: store must not be nil")
	}
	cfg = cfg.withDefaults()
	e := &Engine{
		store:     st,
		embedder:  embedder,
		distiller: cfg.Distiller,
		vectors:   NewVectorCache(cfg.VectorDim),
		cfg:       cfg,
	}
	if err := e.ReloadVectors(ctx, ""); err != nil {
		return nil, err
	}
	return e, nil
}

// ReloadVectors refreshes the in-memory hot vector cache from SQLite active memories.
// When roomID is empty, all rooms are reloaded; otherwise only roomID is replaced.
func (e *Engine) ReloadVectors(ctx context.Context, roomID string) error {
	vecs, err := e.store.LoadActiveMemoryVectors(ctx, roomID)
	if err != nil {
		return fmt.Errorf("memory: load active vectors: %w", err)
	}
	if err := e.vectors.ReplaceRoom(roomID, vecs); err != nil {
		return fmt.Errorf("memory: hydrate vector cache: %w", err)
	}
	return nil
}

func (e *Engine) VectorCount() int {
	return e.vectors.Len()
}

// EmbedQuery computes a 1024d query embedding so OnGate can run embedding concurrently with Decisions API.
func (e *Engine) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if e.embedder == nil || strings.TrimSpace(text) == "" {
		return nil, nil
	}
	resp, err := e.embedder.Embed(ctx, ai.EmbedRequest{
		Texts:      []string{text},
		Dimensions: e.cfg.VectorDim,
	})
	if err != nil {
		return nil, fmt.Errorf("memory: embed query: %w", err)
	}
	if len(resp.Vectors) != 1 {
		return nil, fmt.Errorf("memory: embed query returned %d vectors, want 1", len(resp.Vectors))
	}
	if len(resp.Vectors[0]) != e.cfg.VectorDim {
		return nil, fmt.Errorf("memory: embed query dim %d != expected %d", len(resp.Vectors[0]), e.cfg.VectorDim)
	}
	return resp.Vectors[0], nil
}

// UpsertMemory writes mem to SQLite and keeps the hot 1024d vector cache synchronized.
func (e *Engine) UpsertMemory(ctx context.Context, mem store.Memory) error {
	nowMs := e.nowMs()
	if mem.CreatedAtMs == 0 {
		mem.CreatedAtMs = nowMs
	}
	if mem.UpdatedAtMs == 0 {
		mem.UpdatedAtMs = nowMs
	}
	if mem.TEventMs == 0 {
		mem.TEventMs = mem.CreatedAtMs
	}
	if mem.Importance <= 0 {
		mem.Importance = 5
	}
	if len(mem.Embedding) > 0 && len(mem.Embedding) != e.cfg.VectorDim {
		return fmt.Errorf("memory: embedding dim %d != expected %d", len(mem.Embedding), e.cfg.VectorDim)
	}
	if err := e.store.UpsertMemory(ctx, mem); err != nil {
		return fmt.Errorf("memory: upsert %q: %w", mem.ID, err)
	}

	if mem.Archived || mem.SupersededBy != "" || len(mem.Embedding) == 0 {
		e.vectors.Delete(mem.ID)
		return nil
	}
	return e.vectors.Upsert(VectorEntry{
		ID:             mem.ID,
		RoomID:         mem.RoomID,
		Kind:           mem.Kind,
		TEventMs:       mem.TEventMs,
		Importance:     mem.Importance,
		RecallCount:    mem.RecallCount,
		LastRecalledMs: mem.LastRecalledMs,
	}, mem.Embedding)
}

// SupersedeMemory marks oldID superseded by newID in SQLite and evicts oldID from the hot vector cache.
func (e *Engine) SupersedeMemory(ctx context.Context, oldID, newID string) error {
	if err := e.store.SupersedeMemory(ctx, oldID, newID, e.nowMs()); err != nil {
		return fmt.Errorf("memory: supersede %q -> %q: %w", oldID, newID, err)
	}
	e.vectors.Delete(oldID)
	return nil
}

// ArchiveMemory marks id archived in SQLite and evicts it from the hot vector cache.
func (e *Engine) ArchiveMemory(ctx context.Context, id string) error {
	if err := e.store.ArchiveMemory(ctx, id, e.nowMs()); err != nil {
		return fmt.Errorf("memory: archive %q: %w", id, err)
	}
	e.vectors.Delete(id)
	return nil
}

// RecordEffectiveRecall applies anti-Matthew gated reinforcement: memories are only boosted
// when botReplied && relevant, subject to the per-memory cooldown window (default 30 min).
func (e *Engine) RecordEffectiveRecall(ctx context.Context, roomID string, memoryIDs []string, botReplied, relevant bool) (int, error) {
	if !botReplied || !relevant || len(memoryIDs) == 0 {
		return 0, nil
	}
	nowMs := e.nowMs()
	cooldownMs := e.cfg.ReinforceCooldown.Milliseconds()
	seen := make(map[string]bool, len(memoryIDs))
	reinforcedCount := 0

	for _, rawID := range memoryIDs {
		id := strings.TrimSpace(rawID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true

		if roomID != "" {
			mem, ok, err := e.store.GetMemory(ctx, id)
			if err != nil {
				return reinforcedCount, fmt.Errorf("memory: verify room for reinforce %q: %w", id, err)
			}
			if !ok || mem.RoomID != roomID || mem.SupersededBy != "" || mem.Archived {
				continue
			}
		}

		updated, err := e.store.ReinforceMemory(ctx, id, nowMs, cooldownMs)
		if err != nil {
			return reinforcedCount, fmt.Errorf("memory: reinforce %q: %w", id, err)
		}
		if updated {
			reinforcedCount++
			e.vectors.UpdateMeta(id, func(entry *VectorEntry) {
				entry.RecallCount++
				entry.LastRecalledMs = nowMs
			})
		}
	}
	return reinforcedCount, nil
}

// PassiveRecall runs conservative noise-rejecting hybrid retrieval (FTS5 + alias/entity + 1024d vector)
// with power-law decay, superseded/archived exclusion, and recent-window deduplication.
func (e *Engine) PassiveRecall(ctx context.Context, req PassiveRecallRequest) (RecallResult, error) {
	t0 := time.Now()
	nowMs := req.NowMs
	if nowMs == 0 {
		nowMs = e.nowMs()
	}
	topK := req.TopK
	if topK <= 0 {
		topK = e.cfg.PassiveTopK
	}

	qvec, err := e.resolveQueryVector(ctx, req.Query, req.QueryVector)
	if err != nil {
		return RecallResult{}, err
	}

	cands, err := e.collectCandidates(ctx, recallParams{
		roomID:          req.RoomID,
		speaker:         req.Speaker,
		speakerID:       req.SpeakerID,
		query:           req.Query,
		queryVec:        qvec,
		minVectorSim:    e.cfg.PassiveMinVectorSim,
		includeArchived: false,
		ftsLimit:        40,
		vecLimit:        24,
	})
	if err != nil {
		return RecallResult{}, err
	}

	ranked := e.scoreCandidates(cands, nowMs, e.cfg.PassiveSurfaceThreshold, false)
	winData := buildWindowFilter(req.RecentWindow)

	selected := make([]RecallHit, 0, min(topK, len(ranked)))
	selectedVecs := make([][]float32, 0, min(topK, len(ranked)))
	skippedInWin := 0

	for _, hit := range ranked {
		if winData.covers(hit.Memory.Content, e.cfg.InWindowOverlapThreshold) {
			skippedInWin++
			continue
		}
		_, vec, hasVec := e.vectors.Get(hit.Memory.ID)
		if isNearDuplicateHit(hit.Memory.Content, vec, hasVec, selected, selectedVecs) {
			continue
		}
		selected = append(selected, hit)
		selectedVecs = append(selectedVecs, vec)
		if len(selected) >= topK {
			break
		}
	}

	return e.buildRecallResult(selected, skippedInWin, time.Since(t0)), nil
}

// ActiveRecall runs high-recall retrieval for the memory_recall tool, supporting optional
// time bounds, entity filtering, and cold-archive search.
func (e *Engine) ActiveRecall(ctx context.Context, req ActiveRecallRequest) (RecallResult, error) {
	t0 := time.Now()
	nowMs := req.NowMs
	if nowMs == 0 {
		nowMs = e.nowMs()
	}
	limit := req.Limit
	if limit <= 0 {
		limit = e.cfg.ActiveDefaultLimit
	}

	qvec, err := e.resolveQueryVector(ctx, req.Query, req.QueryVector)
	if err != nil {
		return RecallResult{}, err
	}

	cands, err := e.collectCandidates(ctx, recallParams{
		roomID:          req.RoomID,
		speaker:         req.Speaker,
		speakerID:       req.SpeakerID,
		query:           req.Query,
		queryVec:        qvec,
		explicitEntity:  req.Entity,
		timeFrom:        req.TimeFrom,
		timeTo:          req.TimeTo,
		minVectorSim:    e.cfg.ActiveMinVectorSim,
		includeArchived: req.IncludeArchived,
		ftsLimit:        max(40, limit*4),
		vecLimit:        max(32, limit*4),
	})
	if err != nil {
		return RecallResult{}, err
	}

	ranked := e.scoreCandidates(cands, nowMs, e.cfg.ActiveSurfaceThreshold, req.IncludeArchived)
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return e.buildRecallResult(ranked, 0, time.Since(t0)), nil
}

// ComputeStrength computes power-law decayed memory strength from importance, effective recall count,
// elapsed time since max(created_at, last_recalled), and superseded status.
func ComputeStrength(m store.Memory, nowMs int64, halfLifeDays, alpha float64) float64 {
	anchorMs := max(m.CreatedAtMs, m.LastRecalledMs)
	if anchorMs == 0 {
		anchorMs = m.TEventMs
	}
	dtDays := max(0.0, float64(nowMs-anchorMs)/dayMillis)

	if halfLifeDays <= 0 {
		halfLifeDays = defaultHalfLifeDays
	}
	if alpha <= 0 {
		alpha = defaultPowerLawAlpha
	}
	importance := float64(max(1, min(10, m.Importance)))
	rc := max(0, m.RecallCount)
	stability := halfLifeDays * (0.35 + 0.18*importance) * (1.0 + 0.45*math.Log1p(float64(rc)))
	if m.SupersededBy != "" {
		stability *= 0.5
	}
	base := min(1.2, 0.12+0.095*importance+0.07*math.Log1p(float64(rc)))
	factor := math.Pow(1.0+dtDays/stability, -alpha)
	return base * factor
}

// FormatMemoryLine formats a memory with its [YYYY-MM-DD] t_event date prefix.
func FormatMemoryLine(m store.Memory, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	date := time.UnixMilli(m.TEventMs).In(loc).Format("2006-01-02")
	return fmt.Sprintf("[%s] %s", date, strings.TrimSpace(m.Content))
}

// FormatAssociatedMemoriesBlock wraps surfaced hits in an <associated_memories> XML block.
func FormatAssociatedMemoriesBlock(hits []RecallHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<associated_memories>\n")
	for _, h := range hits {
		b.WriteString("- ")
		b.WriteString(h.Formatted)
		b.WriteByte('\n')
	}
	b.WriteString("</associated_memories>")
	return b.String()
}

type recallParams struct {
	roomID          string
	speaker         string
	speakerID       string
	query           string
	queryVec        []float32
	explicitEntity  string
	timeFrom        int64
	timeTo          int64
	minVectorSim    float32
	includeArchived bool
	ftsLimit        int
	vecLimit        int
}

type recallCandidate struct {
	mem         store.Memory
	ftsScore    float64
	vectorSim   float32
	vectorAct   float64
	entityBoost float64
	cues        []string
}

func (c *recallCandidate) addCue(cue string) {
	if !slices.Contains(c.cues, cue) {
		c.cues = append(c.cues, cue)
	}
}

func (e *Engine) resolveQueryVector(ctx context.Context, query string, precomputed []float32) ([]float32, error) {
	if len(precomputed) > 0 {
		if len(precomputed) != e.cfg.VectorDim {
			return nil, fmt.Errorf("memory: query vector dim %d != expected %d", len(precomputed), e.cfg.VectorDim)
		}
		return precomputed, nil
	}
	return e.EmbedQuery(ctx, query)
}

func (e *Engine) collectCandidates(ctx context.Context, p recallParams) (map[string]*recallCandidate, error) {
	var (
		ftsHits   []store.MemorySearchHit
		ftsErr    error
		aliases   []store.EntityAlias
		aliasErr  error
		vecHits   []VectorHit
		useVector = len(p.queryVec) == e.cfg.VectorDim
	)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if strings.TrimSpace(p.query) != "" {
			ftsHits, ftsErr = e.store.SearchMemoriesFTS(ctx, p.roomID, p.query, p.ftsLimit, p.includeArchived)
		}
		if ftsErr == nil && p.roomID != "" {
			aliases, aliasErr = e.store.ListEntityAliases(ctx, p.roomID)
		}
	}()

	if useVector {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vecHits = e.vectors.TopK(p.roomID, p.queryVec, p.vecLimit, p.minVectorSim, e.cfg.VectorWorkers)
		}()
	}
	wg.Wait()

	if ftsErr != nil {
		return nil, fmt.Errorf("memory: fts recall: %w", ftsErr)
	}
	if aliasErr != nil {
		return nil, fmt.Errorf("memory: load entity aliases: %w", aliasErr)
	}

	aliasIdx := newAliasIndex(aliases)
	cands := make(map[string]*recallCandidate, len(ftsHits)+len(vecHits))

	ensureCand := func(mem store.Memory) *recallCandidate {
		c, ok := cands[mem.ID]
		if !ok {
			c = &recallCandidate{mem: mem}
			cands[mem.ID] = c
		}
		return c
	}

	for _, hit := range ftsHits {
		if !isMemoryEligible(hit.Memory, p.includeArchived, p.timeFrom, p.timeTo) {
			continue
		}
		// SQLite FTS5 bm25() returns negative values where more negative means stronger match.
		rawPos := -hit.BM25
		if rawPos <= 0 {
			continue
		}
		normScore := rawPos / (rawPos + 14.0)
		c := ensureCand(hit.Memory)
		if normScore > c.ftsScore {
			c.ftsScore = normScore
		}
		c.addCue("fts")
	}

	var requiredEntity string
	if strings.TrimSpace(p.explicitEntity) != "" {
		requiredEntity = aliasIdx.resolveExact(p.explicitEntity)
		mems, err := e.store.ListMemoriesByEntity(ctx, p.roomID, requiredEntity, p.includeArchived)
		if err != nil {
			return nil, fmt.Errorf("memory: entity recall %q: %w", requiredEntity, err)
		}
		boost := 0.42
		if strings.TrimSpace(p.query) == "" {
			boost = 0.70
		}
		for _, m := range mems {
			if !isMemoryEligible(m, p.includeArchived, p.timeFrom, p.timeTo) {
				continue
			}
			c := ensureCand(m)
			c.entityBoost += boost
			c.addCue("entity:" + requiredEntity)
		}
	}

	speakerAnchors, textAnchors := aliasIdx.extractAnchors(p.speaker, p.speakerID, p.query)
	for _, anchor := range textAnchors {
		if anchor == requiredEntity {
			continue
		}
		mems, err := e.store.ListMemoriesByEntity(ctx, p.roomID, anchor, p.includeArchived)
		if err != nil {
			return nil, fmt.Errorf("memory: text anchor recall %q: %w", anchor, err)
		}
		for _, m := range mems {
			if !isMemoryEligible(m, p.includeArchived, p.timeFrom, p.timeTo) {
				continue
			}
			c := ensureCand(m)
			c.entityBoost += 0.42
			c.addCue("entity:" + anchor)
		}
	}
	for _, anchor := range speakerAnchors {
		mems, err := e.store.ListMemoriesByEntity(ctx, p.roomID, anchor, p.includeArchived)
		if err != nil {
			return nil, fmt.Errorf("memory: speaker anchor recall %q: %w", anchor, err)
		}
		for _, m := range mems {
			if !isMemoryEligible(m, p.includeArchived, p.timeFrom, p.timeTo) {
				continue
			}
			c := ensureCand(m)
			// Gentle speaker prior stays below threshold alone and resonates when paired with another cue.
			c.entityBoost += 0.18
			c.addCue("speaker:" + anchor)
		}
	}

	if useVector {
		for _, vh := range vecHits {
			act := e.vectorActivation(vh.Similarity, p.minVectorSim)
			if act <= 0 {
				continue
			}
			c, exists := cands[vh.ID]
			if !exists {
				m, ok, err := e.store.GetMemory(ctx, vh.ID)
				if err != nil {
					return nil, fmt.Errorf("memory: fetch vector hit %q: %w", vh.ID, err)
				}
				if !ok || !isMemoryEligible(m, p.includeArchived, p.timeFrom, p.timeTo) {
					continue
				}
				c = ensureCand(m)
			}
			if vh.Similarity > c.vectorSim {
				c.vectorSim = vh.Similarity
				c.vectorAct = act
			}
			c.addCue("vector")
		}

		// Score archived candidates discovered via FTS/entity against queryVec since archived rows live outside hot VectorCache.
		if p.includeArchived {
			for _, c := range cands {
				if !c.mem.Archived || c.vectorSim > 0 {
					continue
				}
				full, ok, err := e.store.GetMemory(ctx, c.mem.ID)
				if err != nil {
					return nil, fmt.Errorf("memory: fetch archived vector %q: %w", c.mem.ID, err)
				}
				if !ok || len(full.Embedding) != e.cfg.VectorDim {
					continue
				}
				sim := CosineSimilarity(p.queryVec, full.Embedding)
				if act := e.vectorActivation(sim, p.minVectorSim); act > 0 {
					c.vectorSim = sim
					c.vectorAct = act
					c.addCue("vector")
				}
			}
		}
	}

	// Support time-range-only ActiveRecall queries when both Query and Entity are empty.
	if strings.TrimSpace(p.query) == "" && requiredEntity == "" && (p.timeFrom > 0 || p.timeTo > 0) {
		for _, entry := range e.vectors.ListByRoom(p.roomID) {
			if p.timeFrom > 0 && entry.TEventMs < p.timeFrom {
				continue
			}
			if p.timeTo > 0 && entry.TEventMs > p.timeTo {
				continue
			}
			if _, exists := cands[entry.ID]; exists {
				continue
			}
			m, ok, err := e.store.GetMemory(ctx, entry.ID)
			if err != nil {
				return nil, fmt.Errorf("memory: fetch time-range memory %q: %w", entry.ID, err)
			}
			if !ok || !isMemoryEligible(m, p.includeArchived, p.timeFrom, p.timeTo) {
				continue
			}
			c := ensureCand(m)
			c.entityBoost = 0.65
			c.addCue("time_range")
		}
	}

	if requiredEntity != "" {
		for id, c := range cands {
			if !memoryHasEntity(c.mem, requiredEntity) {
				delete(cands, id)
			}
		}
	}

	return cands, nil
}

func (e *Engine) vectorActivation(sim, minSim float32) float64 {
	if sim < minSim {
		return 0
	}
	denom := float64(0.78 - minSim)
	if denom < 0.05 {
		denom = 0.05
	}
	norm := float64(sim-minSim) / denom
	if norm > 1.0 {
		norm = 1.0
	}
	if norm <= 0 {
		return 0
	}
	return e.cfg.VectorWeight * norm
}

func (e *Engine) scoreCandidates(cands map[string]*recallCandidate, nowMs int64, threshold float64, coldSearch bool) []RecallHit {
	hits := make([]RecallHit, 0, len(cands))
	for _, c := range cands {
		act := c.ftsScore + c.vectorAct + c.entityBoost
		if act <= 0 {
			continue
		}
		strength := ComputeStrength(c.mem, nowMs, e.cfg.HalfLifeDays, e.cfg.PowerLawAlpha)
		var mult float64
		if coldSearch && c.mem.Archived {
			// Archived memories have naturally decayed low strength; keep a softer floor during explicit cold recall.
			mult = 0.55 + 0.45*strength
		} else {
			mult = 0.20 + 0.80*strength
		}
		score := act * mult
		ageDays := max(0.0, float64(nowMs-c.mem.TEventMs)/dayMillis)
		score *= 1.0 + 0.10*math.Exp(-ageDays/14.0)
		if score < threshold {
			continue
		}
		hits = append(hits, RecallHit{
			Memory:      c.mem,
			Score:       score,
			Strength:    strength,
			FTSScore:    c.ftsScore,
			VectorSim:   c.vectorSim,
			EntityBoost: c.entityBoost,
			MatchedCues: c.cues,
			Formatted:   FormatMemoryLine(c.mem, e.cfg.Location),
		})
	}

	slices.SortFunc(hits, func(a, b RecallHit) int {
		return cmp.Or(
			cmp.Compare(b.Score, a.Score),
			cmp.Compare(b.Memory.TEventMs, a.Memory.TEventMs),
			cmp.Compare(a.Memory.ID, b.Memory.ID),
		)
	})
	return hits
}

func (e *Engine) buildRecallResult(hits []RecallHit, skippedInWin int, latency time.Duration) RecallResult {
	trace := make([]HitTrace, 0, len(hits))
	for _, h := range hits {
		trace = append(trace, HitTrace{
			MemoryID:    h.Memory.ID,
			Kind:        h.Memory.Kind,
			TEventDate:  time.UnixMilli(h.Memory.TEventMs).In(e.cfg.Location).Format("2006-01-02"),
			Content:     h.Memory.Content,
			Score:       h.Score,
			Strength:    h.Strength,
			FTSScore:    h.FTSScore,
			VectorSim:   h.VectorSim,
			EntityBoost: h.EntityBoost,
			MatchedCues: h.MatchedCues,
			Archived:    h.Memory.Archived,
		})
	}
	return RecallResult{
		Hits:           hits,
		FormattedBlock: FormatAssociatedMemoriesBlock(hits),
		Trace:          trace,
		SkippedInWin:   skippedInWin,
		Latency:        latency,
	}
}

func (e *Engine) nowMs() int64 {
	return e.cfg.Clock().UnixMilli()
}

func isMemoryEligible(m store.Memory, includeArchived bool, timeFrom, timeTo int64) bool {
	if m.SupersededBy != "" {
		return false
	}
	if !includeArchived && m.Archived {
		return false
	}
	if timeFrom > 0 && m.TEventMs < timeFrom {
		return false
	}
	if timeTo > 0 && m.TEventMs > timeTo {
		return false
	}
	return true
}

func memoryHasEntity(m store.Memory, canonical string) bool {
	for _, ent := range m.Entities {
		if strings.EqualFold(ent, canonical) {
			return true
		}
	}
	return false
}

type aliasEntry struct {
	lower     string
	canonical string
	pureASCII bool
}

type aliasIndex struct {
	byAlias  map[string]string
	bySender map[string]string
	sorted   []aliasEntry
}

func newAliasIndex(aliases []store.EntityAlias) aliasIndex {
	idx := aliasIndex{
		byAlias:  make(map[string]string, len(aliases)*2),
		bySender: make(map[string]string, len(aliases)),
	}
	seenLower := make(map[string]bool, len(aliases)*2)
	addEntry := func(raw, canonical string) {
		lower := strings.ToLower(strings.TrimSpace(raw))
		if lower == "" || seenLower[lower] {
			return
		}
		seenLower[lower] = true
		idx.byAlias[lower] = canonical
		idx.sorted = append(idx.sorted, aliasEntry{
			lower:     lower,
			canonical: canonical,
			pureASCII: isPureASCII(lower),
		})
	}

	for _, a := range aliases {
		canon := strings.TrimSpace(a.CanonicalEntity)
		if canon == "" {
			continue
		}
		addEntry(a.Alias, canon)
		addEntry(canon, canon)
		if sid := strings.TrimSpace(a.SenderID); sid != "" {
			if _, exists := idx.bySender[sid]; !exists {
				idx.bySender[sid] = canon
			}
		}
	}

	// Longest aliases first so specific nicknames match before shorter substrings.
	slices.SortFunc(idx.sorted, func(a, b aliasEntry) int {
		return cmp.Or(cmp.Compare(len(b.lower), len(a.lower)), cmp.Compare(a.lower, b.lower))
	})
	return idx
}

func (idx aliasIndex) resolveExact(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if canon, ok := idx.byAlias[strings.ToLower(trimmed)]; ok {
		return canon
	}
	if canon, ok := idx.bySender[trimmed]; ok {
		return canon
	}
	return trimmed
}

func (idx aliasIndex) extractAnchors(speaker, speakerID, text string) (speakerAnchors, textAnchors []string) {
	seenSpeaker := make(map[string]bool)
	addSpeaker := func(canon string) {
		if canon != "" && !seenSpeaker[canon] {
			seenSpeaker[canon] = true
			speakerAnchors = append(speakerAnchors, canon)
		}
	}
	if s := strings.TrimSpace(speaker); s != "" {
		if canon, ok := idx.byAlias[strings.ToLower(s)]; ok {
			addSpeaker(canon)
		}
	}
	if sid := strings.TrimSpace(speakerID); sid != "" {
		if canon, ok := idx.bySender[sid]; ok {
			addSpeaker(canon)
		}
	}

	lowerText := strings.ToLower(text)
	seenText := make(map[string]bool)
	for _, ae := range idx.sorted {
		if seenText[ae.canonical] {
			continue
		}
		if matchAlias(lowerText, ae) {
			seenText[ae.canonical] = true
			textAnchors = append(textAnchors, ae.canonical)
		}
	}
	return speakerAnchors, textAnchors
}

func matchAlias(lowerText string, ae aliasEntry) bool {
	if !ae.pureASCII {
		return strings.Contains(lowerText, ae.lower)
	}
	if len(ae.lower) == 0 {
		return false
	}
	offset := 0
	for {
		pos := strings.Index(lowerText[offset:], ae.lower)
		if pos < 0 {
			return false
		}
		start := offset + pos
		end := start + len(ae.lower)
		leftOK := start == 0 || !isASCIIAlphaNum(lowerText[start-1])
		rightOK := end == len(lowerText) || !isASCIIAlphaNum(lowerText[end])
		if leftOK && rightOK {
			return true
		}
		offset = start + 1
	}
}

func isPureASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func isASCIIAlphaNum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

type windowFilter struct {
	normMessages []string
	msgTokens    []map[string]bool
	unionTokens  map[string]bool
}

func buildWindowFilter(recent []string) windowFilter {
	if len(recent) == 0 {
		return windowFilter{}
	}
	wf := windowFilter{
		normMessages: make([]string, 0, len(recent)),
		msgTokens:    make([]map[string]bool, 0, len(recent)),
		unionTokens:  make(map[string]bool),
	}
	for _, raw := range recent {
		norm := normalizeCompact(raw)
		if norm == "" {
			continue
		}
		wf.normMessages = append(wf.normMessages, norm)
		toks := tokenSet(raw)
		wf.msgTokens = append(wf.msgTokens, toks)
		for tok := range toks {
			wf.unionTokens[tok] = true
		}
	}
	return wf
}

func (wf windowFilter) covers(memContent string, threshold float64) bool {
	if len(wf.normMessages) == 0 {
		return false
	}
	normMem := normalizeCompact(memContent)
	if len([]rune(normMem)) >= 4 {
		for _, msg := range wf.normMessages {
			if strings.Contains(msg, normMem) {
				return true
			}
		}
	}
	memToks := tokenSet(memContent)
	if len(memToks) == 0 {
		return false
	}
	for _, mt := range wf.msgTokens {
		if overlapRatio(memToks, mt) >= threshold {
			return true
		}
	}
	if len(memToks) >= 2 && overlapRatio(memToks, wf.unionTokens) >= max(threshold+0.10, 0.75) {
		return true
	}
	return false
}

func isNearDuplicateHit(content string, vec []float32, hasVec bool, selected []RecallHit, selectedVecs [][]float32) bool {
	norm := normalizeCompact(content)
	toks := tokenSet(content)
	for i, prev := range selected {
		if norm != "" && norm == normalizeCompact(prev.Memory.Content) {
			return true
		}
		if hasVec && len(selectedVecs[i]) == len(vec) && len(vec) > 0 {
			if DotProduct(vec, selectedVecs[i]) >= 0.90 {
				return true
			}
		}
		if len(toks) >= 3 && overlapRatio(toks, tokenSet(prev.Memory.Content)) >= 0.85 {
			return true
		}
	}
	return false
}

func normalizeCompact(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func tokenSet(s string) map[string]bool {
	fields := strings.Fields(store.TokenizeForFTS(s))
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]bool, len(fields))
	for _, f := range fields {
		out[f] = true
	}
	return out
}

func overlapRatio(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	hits := 0
	for tok := range a {
		if b[tok] {
			hits++
		}
	}
	return float64(hits) / float64(len(a))
}
