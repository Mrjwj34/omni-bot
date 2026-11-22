package eval

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type StageConfig struct {
	Stage              Stage
	TopK               int
	Decay              DecayModel
	HalfLifeDays       float64
	PowerLawAlpha      float64
	Hops               int
	HopDecay           float64
	SurfaceThreshold   float64
	MinVectorSim       float32
	VectorWeight       float64
	VectorWorkers      int
	NaiveReinforcement bool
}

func DefaultStageConfig(stage Stage) StageConfig {
	cfg := StageConfig{
		Stage:            stage,
		TopK:             4,
		Decay:            DecayExponential,
		HalfLifeDays:     14.0,
		PowerLawAlpha:    0.85,
		Hops:             1,
		HopDecay:         0.35,
		SurfaceThreshold: 0.32,
		MinVectorSim:     0.46,
		VectorWeight:     0.75,
		VectorWorkers:    1,
	}
	switch stage {
	case StageB0:
		// B0 represents an uncalibrated pure FTS5 BM25 baseline.
		cfg.SurfaceThreshold = 0
	case StageB1, StageB2:
		cfg.SurfaceThreshold = 0.28
	case StageB3, StageB4:
		// Multi-cue seeding + spreading raises baseline activation, so threshold is calibrated higher.
		cfg.SurfaceThreshold = 0.42
	}
	return cfg
}

type neighbor struct {
	id     string
	weight float64
}

type aliasEntry struct {
	alias     string
	canonical string
}

// Engine backs the 5-stage ablation pipeline with real SQLite FTS5 storage and in-memory hot indexes.
type Engine struct {
	db       *sql.DB
	now      int64
	memories map[string]Memory
	entities map[string][]string
	adj      map[string][]neighbor
	aliases  []aliasEntry
	aliasMap map[string]string
	vectors  *VectorIndex
	// Precomputed strengths for anti-Matthew vs naive reinforcement modes.
	recallEvents []RecallEvent
}

func NewEngine(ctx context.Context, dbPath string, ds Dataset) (*Engine, error) {
	dsn := dbPath
	if dsn == "" {
		dsn = ":memory:"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Single connection prevents SQLITE_BUSY on in-memory DBs and keeps WAL writes deterministic.
	db.SetMaxOpenConns(1)

	if err := initSchema(ctx, db, dbPath != "" && dbPath != ":memory:"); err != nil {
		_ = db.Close()
		return nil, err
	}

	e := &Engine{
		db:           db,
		now:          ds.Now,
		memories:     make(map[string]Memory, len(ds.Memories)),
		entities:     make(map[string][]string),
		adj:          make(map[string][]neighbor),
		aliasMap:     make(map[string]string, len(ds.Aliases)),
		recallEvents: slices.Clone(ds.RecallEvents),
	}

	for alias, canonical := range ds.Aliases {
		lower := strings.ToLower(alias)
		e.aliasMap[lower] = canonical
		e.aliases = append(e.aliases, aliasEntry{alias: lower, canonical: canonical})
	}
	// Longest aliases first so specific nicknames match before shorter substrings.
	slices.SortFunc(e.aliases, func(a, b aliasEntry) int {
		return cmp.Or(cmp.Compare(len(b.alias), len(a.alias)), cmp.Compare(a.alias, b.alias))
	})

	if err := e.loadDataset(ctx, ds); err != nil {
		_ = db.Close()
		return nil, err
	}
	return e, nil
}

func (e *Engine) Close() error {
	if e.db == nil {
		return nil
	}
	return e.db.Close()
}

func (e *Engine) SetVectors(idx *VectorIndex) {
	e.vectors = idx
}

// HistoryChain follows superseded_by pointers in SQLite from an older memory to its latest state.
func (e *Engine) HistoryChain(ctx context.Context, startID string) ([]string, error) {
	chain := []string{startID}
	cur := startID
	for range 16 {
		var next string
		err := e.db.QueryRowContext(ctx, `SELECT superseded_by FROM memories WHERE id = ?`, cur).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) || next == "" {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("history chain %q: %w", cur, err)
		}
		chain = append(chain, next)
		cur = next
	}
	return chain, nil
}

func (e *Engine) Search(ctx context.Context, q Query, queryVec []float32, cfg StageConfig) ([]Hit, error) {
	topK := cfg.TopK
	if topK <= 0 {
		topK = 4
	}
	now := q.Timestamp
	if now == 0 {
		now = e.now
	}

	useDecay := cfg.Stage != StageB0
	useTemporal := cfg.Stage == StageB2 || cfg.Stage == StageB3 || cfg.Stage == StageB4
	useGraph := cfg.Stage == StageB3 || cfg.Stage == StageB4
	useVector := cfg.Stage == StageB4 && e.vectors != nil && len(queryVec) == e.vectors.Dim()

	var (
		lexScores map[string]float64
		lexErr    error
		vecHits   []VectorHit
	)

	if useVector {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			lexScores, lexErr = e.searchFTS(ctx, q.Text, 40)
		}()
		go func() {
			defer wg.Done()
			vecHits = e.vectors.TopK(queryVec, 24, cfg.MinVectorSim, cfg.VectorWorkers)
		}()
		wg.Wait()
	} else {
		lexScores, lexErr = e.searchFTS(ctx, q.Text, 40)
	}
	if lexErr != nil {
		return nil, lexErr
	}

	activations := make(map[string]float64, len(lexScores)+len(vecHits))
	for id, score := range lexScores {
		if !e.isEligible(id, useTemporal) {
			continue
		}
		activations[id] += score
	}

	if useGraph {
		speakerAnchors, textAnchors := e.extractAnchors(q.Speaker, q.Text)
		for _, anchor := range textAnchors {
			for _, id := range e.entities[anchor] {
				if !e.isEligible(id, useTemporal) {
					continue
				}
				activations[id] += 0.42
			}
		}
		for _, anchor := range speakerAnchors {
			for _, id := range e.entities[anchor] {
				if !e.isEligible(id, useTemporal) {
					continue
				}
				// Gentle speaker prior: stays below threshold alone, resonates when paired with another cue.
				activations[id] += 0.18
			}
		}
	}

	if useVector {
		denom := float64(0.78 - cfg.MinVectorSim)
		if denom < 0.05 {
			denom = 0.05
		}
		for _, vh := range vecHits {
			if !e.isEligible(vh.ID, useTemporal) {
				continue
			}
			norm := float64(vh.Similarity-cfg.MinVectorSim) / denom
			if norm > 1.0 {
				norm = 1.0
			}
			if norm > 0 {
				activations[vh.ID] += cfg.VectorWeight * norm
			}
		}
	}

	if useGraph && cfg.Hops > 0 && len(activations) > 0 {
		e.spreadActivation(activations, cfg.Hops, cfg.HopDecay, useTemporal)
	}

	recallStats := e.computeRecallStats(now, !cfg.NaiveReinforcement)

	scored := make([]Hit, 0, len(activations))
	for id, act := range activations {
		m, ok := e.memories[id]
		if !ok {
			continue
		}
		score := act
		if useDecay {
			st := recallStats[id]
			strength := ComputeStrength(m, st.count, st.lastRecalled, now, cfg, useTemporal)
			score = act * (0.20 + 0.80*strength)
		}
		if useTemporal {
			ageDays := max(0.0, float64(now-m.TEvent)/float64(daySec))
			score *= 1.0 + 0.10*math.Exp(-ageDays/14.0)
		}
		if score < cfg.SurfaceThreshold {
			continue
		}
		scored = append(scored, Hit{MemoryID: id, Score: score})
	}

	slices.SortFunc(scored, func(a, b Hit) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(a.MemoryID, b.MemoryID))
	})
	if len(scored) > topK {
		scored = scored[:topK]
	}
	for i := range scored {
		scored[i].Rank = i + 1
	}
	return scored, nil
}

// EvaluateStage runs every query in ds through the configured stage and returns exact metrics.
func (e *Engine) EvaluateStage(ctx context.Context, ds Dataset, queryVecs map[string][]float32, cfg StageConfig) (Metrics, []QueryOutcome, error) {
	outcomes := make([]QueryOutcome, len(ds.Queries))
	for i, q := range ds.Queries {
		var qv []float32
		if queryVecs != nil {
			qv = queryVecs[q.ID]
		}
		t0 := time.Now()
		hits, err := e.Search(ctx, q, qv, cfg)
		dt := time.Since(t0)
		if err != nil {
			return Metrics{}, nil, fmt.Errorf("search %s (%s): %w", q.ID, cfg.Stage, err)
		}
		outcomes[i] = QueryOutcome{
			Query:   q,
			Hits:    hits,
			Latency: dt,
		}
	}
	topK := cfg.TopK
	if topK <= 0 {
		topK = 4
	}
	return Evaluate(outcomes, topK), outcomes, nil
}

func (e *Engine) isEligible(id string, useTemporal bool) bool {
	m, ok := e.memories[id]
	if !ok || m.Archived {
		return false
	}
	if useTemporal && (m.SupersededBy != "" || m.MergedSource) {
		return false
	}
	return true
}

func (e *Engine) searchFTS(ctx context.Context, text string, limit int) (map[string]float64, error) {
	matchExpr := BuildFTSQuery(text)
	if matchExpr == "" {
		return map[string]float64{}, nil
	}
	rows, err := e.db.QueryContext(ctx,
		`SELECT memory_id, bm25(memories_fts) AS rank
		 FROM memories_fts
		 WHERE memories_fts MATCH ?
		 ORDER BY rank
		 LIMIT ?`,
		matchExpr, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("fts5 query: %w", err)
	}
	defer rows.Close()

	scores := make(map[string]float64, limit)
	for rows.Next() {
		var id string
		var bm25Val float64
		if err := rows.Scan(&id, &bm25Val); err != nil {
			return nil, fmt.Errorf("fts5 scan: %w", err)
		}
		// SQLite FTS5 bm25() returns negative scores where more negative means stronger match.
		rawPos := -bm25Val
		if rawPos <= 0 {
			continue
		}
		scores[id] = rawPos / (rawPos + 14.0)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fts5 rows: %w", err)
	}
	return scores, nil
}

func (e *Engine) extractAnchors(speaker, text string) (speakerAnchors []string, textAnchors []string) {
	seenSpeaker := make(map[string]bool)
	if canon, ok := e.aliasMap[strings.ToLower(strings.TrimSpace(speaker))]; ok {
		seenSpeaker[canon] = true
		speakerAnchors = append(speakerAnchors, canon)
	}

	lowerText := strings.ToLower(text)
	seenText := make(map[string]bool)
	for _, ae := range e.aliases {
		if strings.Contains(lowerText, ae.alias) && !seenText[ae.canonical] {
			seenText[ae.canonical] = true
			textAnchors = append(textAnchors, ae.canonical)
		}
	}
	return speakerAnchors, textAnchors
}

func (e *Engine) spreadActivation(activations map[string]float64, hops int, hopDecay float64, useTemporal bool) {
	type seed struct {
		id  string
		act float64
	}
	frontier := make([]seed, 0, len(activations))
	for id, act := range activations {
		if act >= 0.32 {
			frontier = append(frontier, seed{id: id, act: min(act, 1.0)})
		}
	}
	slices.SortFunc(frontier, func(a, b seed) int {
		return cmp.Or(cmp.Compare(b.act, a.act), cmp.Compare(a.id, b.id))
	})
	if len(frontier) > 16 {
		frontier = frontier[:16]
	}

	added := make(map[string]float64)
	decay := hopDecay
	for range hops {
		nextMap := make(map[string]float64)
		for _, src := range frontier {
			for _, nb := range e.adj[src.id] {
				if !e.isEligible(nb.id, useTemporal) {
					continue
				}
				delta := src.act * nb.weight * decay
				if delta < 0.02 {
					continue
				}
				nextMap[nb.id] += delta
			}
		}
		frontier = frontier[:0]
		for id, delta := range nextMap {
			added[id] += delta
			frontier = append(frontier, seed{id: id, act: min(delta, 0.8)})
		}
		slices.SortFunc(frontier, func(a, b seed) int {
			return cmp.Or(cmp.Compare(b.act, a.act), cmp.Compare(a.id, b.id))
		})
		if len(frontier) > 16 {
			frontier = frontier[:16]
		}
		decay *= hopDecay
	}

	for id, delta := range added {
		// Cap associative spill so dense nodes cannot surface ungrounded memories alone.
		activations[id] += min(delta, 0.35)
	}
}

type recallStat struct {
	count        int
	lastRecalled int64
}

func (e *Engine) computeRecallStats(now int64, antiMatthew bool) map[string]recallStat {
	const cooldownSec int64 = 1800
	stats := make(map[string]recallStat)
	for _, ev := range e.recallEvents {
		if ev.Timestamp > now {
			continue
		}
		if antiMatthew {
			if !ev.BotReplied || !ev.Relevant {
				continue
			}
			cur := stats[ev.MemoryID]
			if cur.lastRecalled > 0 && ev.Timestamp-cur.lastRecalled < cooldownSec {
				continue
			}
			cur.count++
			if ev.Timestamp > cur.lastRecalled {
				cur.lastRecalled = ev.Timestamp
			}
			stats[ev.MemoryID] = cur
		} else {
			cur := stats[ev.MemoryID]
			cur.count++
			if ev.Timestamp > cur.lastRecalled {
				cur.lastRecalled = ev.Timestamp
			}
			stats[ev.MemoryID] = cur
		}
	}
	return stats
}

// ComputeStrength calculates decayed memory retrieval strength from importance,
// effective recall count, elapsed time since max(t_ingest, last_recalled), and superseded status.
func ComputeStrength(m Memory, recallCount int, lastRecalled int64, now int64, cfg StageConfig, accelerateSuperseded bool) float64 {
	anchorTS := max(m.TIngest, lastRecalled)
	dtDays := max(0.0, float64(now-anchorTS)/float64(daySec))

	halfLife := cfg.HalfLifeDays
	if halfLife <= 0 {
		halfLife = 14.0
	}
	importance := float64(max(1, min(10, m.Importance)))
	stability := halfLife * (0.35 + 0.18*importance) * (1.0 + 0.45*math.Log1p(float64(recallCount)))
	if accelerateSuperseded && m.SupersededBy != "" {
		stability *= 0.5
	}

	base := min(1.2, 0.12+0.095*importance+0.07*math.Log1p(float64(recallCount)))
	var factor float64
	switch cfg.Decay {
	case DecayPowerLaw:
		alpha := cfg.PowerLawAlpha
		if alpha <= 0 {
			alpha = 0.85
		}
		factor = math.Pow(1.0+dtDays/stability, -alpha)
	default:
		factor = math.Exp(-math.Ln2 * dtDays / stability)
	}
	return base * factor
}

func initSchema(ctx context.Context, db *sql.DB, enableWAL bool) error {
	if enableWAL {
		if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
			return fmt.Errorf("sqlite pragma: %w", err)
		}
	}
	ddl := `
	CREATE TABLE memories (
		id            TEXT PRIMARY KEY,
		kind          TEXT NOT NULL,
		text          TEXT NOT NULL,
		t_event       INTEGER NOT NULL,
		t_ingest      INTEGER NOT NULL,
		superseded_by TEXT NOT NULL DEFAULT '',
		importance    INTEGER NOT NULL,
		strength      REAL NOT NULL,
		last_recalled INTEGER NOT NULL DEFAULT 0,
		recall_count  INTEGER NOT NULL DEFAULT 0,
		archived      INTEGER NOT NULL DEFAULT 0,
		provenance    TEXT NOT NULL DEFAULT '[]'
	);
	CREATE TABLE memory_entities (
		memory_id TEXT NOT NULL,
		entity    TEXT NOT NULL,
		PRIMARY KEY (memory_id, entity)
	);
	CREATE INDEX idx_memory_entities_entity ON memory_entities(entity);
	CREATE TABLE memory_assoc (
		a          TEXT NOT NULL,
		b          TEXT NOT NULL,
		weight     REAL NOT NULL,
		last_fired INTEGER NOT NULL,
		PRIMARY KEY (a, b)
	);
	CREATE VIRTUAL TABLE memories_fts USING fts5(
		memory_id UNINDEXED,
		tokens,
		tokenize = 'unicode61'
	);`
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}

func (e *Engine) loadDataset(ctx context.Context, ds Dataset) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	memStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO memories (id, kind, text, t_event, t_ingest, superseded_by, importance, strength, last_recalled, recall_count, archived)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer memStmt.Close()

	ftsStmt, err := tx.PrepareContext(ctx, `INSERT INTO memories_fts (memory_id, tokens) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer ftsStmt.Close()

	entStmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO memory_entities (memory_id, entity) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer entStmt.Close()

	for _, m := range ds.Memories {
		archivedInt := 0
		if m.Archived {
			archivedInt = 1
		}
		if _, err := memStmt.ExecContext(ctx, m.ID, string(m.Kind), m.Text, m.TEvent, m.TIngest, m.SupersededBy, m.Importance, m.Strength, m.LastRecalled, m.RecallCount, archivedInt); err != nil {
			return fmt.Errorf("insert memory %q: %w", m.ID, err)
		}
		if _, err := ftsStmt.ExecContext(ctx, m.ID, TokenizeForFTS(m.Text)); err != nil {
			return fmt.Errorf("insert fts %q: %w", m.ID, err)
		}
		e.memories[m.ID] = m
		for _, ent := range m.Entities {
			if _, err := entStmt.ExecContext(ctx, m.ID, ent); err != nil {
				return fmt.Errorf("insert entity (%q,%q): %w", m.ID, ent, err)
			}
			e.entities[ent] = append(e.entities[ent], m.ID)
		}
	}

	edgeStmt, err := tx.PrepareContext(ctx, `INSERT INTO memory_assoc (a, b, weight, last_fired) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer edgeStmt.Close()

	for _, edge := range ds.Edges {
		if _, err := edgeStmt.ExecContext(ctx, edge.A, edge.B, edge.Weight, edge.LastFired); err != nil {
			return fmt.Errorf("insert assoc (%q,%q): %w", edge.A, edge.B, err)
		}
		e.adj[edge.A] = append(e.adj[edge.A], neighbor{id: edge.B, weight: edge.Weight})
		e.adj[edge.B] = append(e.adj[edge.B], neighbor{id: edge.A, weight: edge.Weight})
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit dataset: %w", err)
	}
	return nil
}
