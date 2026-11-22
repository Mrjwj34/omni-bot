package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	MemoryKindEpisodic = "episodic"
	MemoryKindSemantic = "semantic"
	MemoryKindHabit    = "habit"
)

// Memory represents a distilled episodic, semantic, or habit memory with optional 1024d embedding.
type Memory struct {
	ID             string    `json:"id"`
	RoomID         string    `json:"room_id"`
	Kind           string    `json:"kind"`
	Content        string    `json:"content"`
	TEventMs       int64     `json:"t_event_ms"`
	CreatedAtMs    int64     `json:"created_at_ms"`
	UpdatedAtMs    int64     `json:"updated_at_ms"`
	LastRecalledMs int64     `json:"last_recalled_ms"`
	RecallCount    int       `json:"recall_count"`
	Importance     int       `json:"importance"`
	SupersededBy   string    `json:"superseded_by"`
	Archived       bool      `json:"archived"`
	Embedding      []float32 `json:"embedding,omitempty"`
	Entities       []string  `json:"entities,omitempty"`
}

// MemoryVector is the lightweight hot-path projection loaded into in-memory vector scan indexes.
type MemoryVector struct {
	ID             string
	RoomID         string
	Kind           string
	TEventMs       int64
	Importance     int
	RecallCount    int
	LastRecalledMs int64
	Embedding      []float32
}

// MemorySearchHit pairs a matched Memory with its SQLite FTS5 BM25 score (more negative = stronger match).
type MemorySearchHit struct {
	Memory Memory
	BM25   float64
}

// EntityAlias maps a group nickname, handle, or alias to a canonical entity anchor.
type EntityAlias struct {
	RoomID          string `json:"room_id"`
	Alias           string `json:"alias"`
	CanonicalEntity string `json:"canonical_entity"`
	SenderID        string `json:"sender_id"`
	UpdatedAtMs     int64  `json:"updated_at_ms"`
}

// UpsertMemory atomically writes a memory row, its canonical entity anchors, and its synchronized FTS5 entry.
func (s *Store) UpsertMemory(ctx context.Context, mem Memory) error {
	if strings.TrimSpace(mem.ID) == "" {
		return errors.New("store: memory id must not be empty")
	}
	if mem.Kind == "" {
		mem.Kind = MemoryKindEpisodic
	}
	nowMs := time.Now().UnixMilli()
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

	embBlob := EncodeFloat32LE(mem.Embedding)
	embDim := len(mem.Embedding)
	entities := dedupeNonEmpty(mem.Entities)

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin upsert memory: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO memories (
			id, room_id, kind, content, t_event_ms, created_at_ms, updated_at_ms,
			last_recalled_ms, recall_count, importance, superseded_by, archived,
			embedding, embedding_dim
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			room_id = excluded.room_id,
			kind = excluded.kind,
			content = excluded.content,
			t_event_ms = excluded.t_event_ms,
			updated_at_ms = excluded.updated_at_ms,
			last_recalled_ms = excluded.last_recalled_ms,
			recall_count = excluded.recall_count,
			importance = excluded.importance,
			superseded_by = excluded.superseded_by,
			archived = excluded.archived,
			embedding = excluded.embedding,
			embedding_dim = excluded.embedding_dim`,
		mem.ID, mem.RoomID, mem.Kind, mem.Content, mem.TEventMs, mem.CreatedAtMs, mem.UpdatedAtMs,
		mem.LastRecalledMs, mem.RecallCount, mem.Importance, mem.SupersededBy, boolToInt(mem.Archived),
		embBlob, embDim,
	)
	if err != nil {
		return fmt.Errorf("store: upsert memory %q: %w", mem.ID, err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_entities WHERE memory_id = ?`, mem.ID); err != nil {
		return fmt.Errorf("store: clear memory entities %q: %w", mem.ID, err)
	}
	for _, ent := range entities {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO memory_entities (memory_id, canonical_entity) VALUES (?, ?)`,
			mem.ID, ent,
		); err != nil {
			return fmt.Errorf("store: insert memory entity %q/%q: %w", mem.ID, ent, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM memories_fts WHERE memory_id = ?`, mem.ID); err != nil {
		return fmt.Errorf("store: delete old memories_fts %q: %w", mem.ID, err)
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO memories_fts (memory_id, room_id, content_tokens, entities_tokens) VALUES (?, ?, ?, ?)`,
		mem.ID, mem.RoomID, TokenizeForFTS(mem.Content), TokenizeForFTS(strings.Join(entities, " ")),
	)
	if err != nil {
		return fmt.Errorf("store: insert memories_fts %q: %w", mem.ID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit upsert memory %q: %w", mem.ID, err)
	}
	return nil
}

// GetMemory fetches a memory and its associated canonical entities by ID.
func (s *Store) GetMemory(ctx context.Context, id string) (Memory, bool, error) {
	var m Memory
	var archived int
	var embBlob []byte
	var embDim int
	err := s.readDB.QueryRowContext(
		ctx,
		`SELECT id, room_id, kind, content, t_event_ms, created_at_ms, updated_at_ms,
		        last_recalled_ms, recall_count, importance, superseded_by, archived,
		        embedding, embedding_dim
		 FROM memories WHERE id = ?`,
		id,
	).Scan(
		&m.ID, &m.RoomID, &m.Kind, &m.Content, &m.TEventMs, &m.CreatedAtMs, &m.UpdatedAtMs,
		&m.LastRecalledMs, &m.RecallCount, &m.Importance, &m.SupersededBy, &archived,
		&embBlob, &embDim,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, false, nil
	}
	if err != nil {
		return Memory{}, false, fmt.Errorf("store: get memory %q: %w", id, err)
	}
	m.Archived = archived != 0
	if embDim > 0 && len(embBlob) > 0 {
		vec, err := DecodeFloat32LE(embBlob)
		if err != nil {
			return Memory{}, false, err
		}
		m.Embedding = vec
	}
	entities, err := s.loadMemoryEntities(ctx, id)
	if err != nil {
		return Memory{}, false, err
	}
	m.Entities = entities
	return m, true, nil
}

// SupersedeMemory links oldID to newID so oldID is excluded from default active recall.
func (s *Store) SupersedeMemory(ctx context.Context, oldID, newID string, updatedAtMs int64) error {
	if oldID == "" || newID == "" {
		return errors.New("store: supersede requires non-empty oldID and newID")
	}
	if updatedAtMs == 0 {
		updatedAtMs = time.Now().UnixMilli()
	}
	res, err := s.writeDB.ExecContext(
		ctx,
		`UPDATE memories SET superseded_by = ?, updated_at_ms = ? WHERE id = ?`,
		newID, updatedAtMs, oldID,
	)
	if err != nil {
		return fmt.Errorf("store: supersede memory %q -> %q: %w", oldID, newID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("store: supersede memory %q: not found", oldID)
	}
	return nil
}

// ReinforceMemory increments recall_count only when outside the anti-Matthew cooldown window.
func (s *Store) ReinforceMemory(ctx context.Context, id string, recalledAtMs, cooldownMs int64) (bool, error) {
	if id == "" {
		return false, errors.New("store: reinforce memory id must not be empty")
	}
	if recalledAtMs == 0 {
		recalledAtMs = time.Now().UnixMilli()
	}
	res, err := s.writeDB.ExecContext(
		ctx,
		`UPDATE memories
		 SET recall_count = recall_count + 1,
		     last_recalled_ms = ?,
		     updated_at_ms = ?
		 WHERE id = ?
		   AND (last_recalled_ms = 0 OR (? - last_recalled_ms) >= ?)`,
		recalledAtMs, recalledAtMs, id, recalledAtMs, cooldownMs,
	)
	if err != nil {
		return false, fmt.Errorf("store: reinforce memory %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: reinforce rows affected: %w", err)
	}
	return n > 0, nil
}

// ArchiveMemory marks a memory archived so it leaves the active hot index.
func (s *Store) ArchiveMemory(ctx context.Context, id string, updatedAtMs int64) error {
	if updatedAtMs == 0 {
		updatedAtMs = time.Now().UnixMilli()
	}
	if _, err := s.writeDB.ExecContext(
		ctx,
		`UPDATE memories SET archived = 1, updated_at_ms = ? WHERE id = ?`,
		updatedAtMs, id,
	); err != nil {
		return fmt.Errorf("store: archive memory %q: %w", id, err)
	}
	return nil
}

// SearchMemoriesFTS performs tokenized FTS5 BM25 recall, excluding superseded rows and optionally archived rows.
func (s *Store) SearchMemoriesFTS(ctx context.Context, roomID, query string, limit int, includeArchived bool) ([]MemorySearchHit, error) {
	ftsQuery := BuildFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}

	var where strings.Builder
	args := []any{ftsQuery}
	where.WriteString(`memories_fts MATCH ? AND m.superseded_by = ''`)
	if roomID != "" {
		where.WriteString(` AND m.room_id = ?`)
		args = append(args, roomID)
	}
	if !includeArchived {
		where.WriteString(` AND m.archived = 0`)
	}
	args = append(args, limit)

	sqlStr := `SELECT m.id, m.room_id, m.kind, m.content, m.t_event_ms, m.created_at_ms, m.updated_at_ms,
	                  m.last_recalled_ms, m.recall_count, m.importance, m.superseded_by, m.archived,
	                  bm25(memories_fts) AS score
	           FROM memories_fts
	           JOIN memories m ON m.id = memories_fts.memory_id
	           WHERE ` + where.String() + `
	           ORDER BY score ASC
	           LIMIT ?`

	rows, err := s.readDB.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("store: search memories fts: %w", err)
	}
	defer rows.Close()

	var hits []MemorySearchHit
	for rows.Next() {
		var m Memory
		var archived int
		var score float64
		if err := rows.Scan(
			&m.ID, &m.RoomID, &m.Kind, &m.Content, &m.TEventMs, &m.CreatedAtMs, &m.UpdatedAtMs,
			&m.LastRecalledMs, &m.RecallCount, &m.Importance, &m.SupersededBy, &archived, &score,
		); err != nil {
			return nil, fmt.Errorf("store: scan fts hit: %w", err)
		}
		m.Archived = archived != 0
		hits = append(hits, MemorySearchHit{Memory: m, BM25: score})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate fts hits: %w", err)
	}

	for i := range hits {
		ents, err := s.loadMemoryEntities(ctx, hits[i].Memory.ID)
		if err != nil {
			return nil, err
		}
		hits[i].Memory.Entities = ents
	}
	return hits, nil
}

// LoadActiveMemoryVectors loads all active, non-superseded memory embeddings for in-memory vector scan.
func (s *Store) LoadActiveMemoryVectors(ctx context.Context, roomID string) ([]MemoryVector, error) {
	query := `SELECT id, room_id, kind, t_event_ms, importance, recall_count, last_recalled_ms, embedding
	          FROM memories
	          WHERE archived = 0 AND superseded_by = '' AND embedding_dim > 0 AND embedding IS NOT NULL`
	var args []any
	if roomID != "" {
		query += ` AND room_id = ?`
		args = append(args, roomID)
	}
	query += ` ORDER BY t_event_ms DESC`

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: load active memory vectors: %w", err)
	}
	defer rows.Close()

	var out []MemoryVector
	for rows.Next() {
		var mv MemoryVector
		var raw []byte
		if err := rows.Scan(
			&mv.ID, &mv.RoomID, &mv.Kind, &mv.TEventMs, &mv.Importance, &mv.RecallCount, &mv.LastRecalledMs, &raw,
		); err != nil {
			return nil, fmt.Errorf("store: scan memory vector: %w", err)
		}
		vec, err := DecodeFloat32LE(raw)
		if err != nil {
			return nil, fmt.Errorf("store: decode memory vector %q: %w", mv.ID, err)
		}
		mv.Embedding = vec
		out = append(out, mv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate memory vectors: %w", err)
	}
	return out, nil
}

// ListMemoriesByEntity returns non-superseded memories anchored to canonicalEntity.
func (s *Store) ListMemoriesByEntity(ctx context.Context, roomID, canonicalEntity string, includeArchived bool) ([]Memory, error) {
	var where strings.Builder
	args := []any{canonicalEntity}
	where.WriteString(`me.canonical_entity = ? AND m.superseded_by = ''`)
	if roomID != "" {
		where.WriteString(` AND m.room_id = ?`)
		args = append(args, roomID)
	}
	if !includeArchived {
		where.WriteString(` AND m.archived = 0`)
	}

	rows, err := s.readDB.QueryContext(
		ctx,
		`SELECT m.id, m.room_id, m.kind, m.content, m.t_event_ms, m.created_at_ms, m.updated_at_ms,
		        m.last_recalled_ms, m.recall_count, m.importance, m.superseded_by, m.archived
		 FROM memory_entities me
		 JOIN memories m ON m.id = me.memory_id
		 WHERE `+where.String()+`
		 ORDER BY m.t_event_ms DESC`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list memories by entity %q: %w", canonicalEntity, err)
	}
	defer rows.Close()

	var out []Memory
	for rows.Next() {
		var m Memory
		var archived int
		if err := rows.Scan(
			&m.ID, &m.RoomID, &m.Kind, &m.Content, &m.TEventMs, &m.CreatedAtMs, &m.UpdatedAtMs,
			&m.LastRecalledMs, &m.RecallCount, &m.Importance, &m.SupersededBy, &archived,
		); err != nil {
			return nil, fmt.Errorf("store: scan entity memory: %w", err)
		}
		m.Archived = archived != 0
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate entity memories: %w", err)
	}
	for i := range out {
		ents, err := s.loadMemoryEntities(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Entities = ents
	}
	return out, nil
}

// UpsertEntityAlias binds an observed nickname or alias to a canonical entity within a room.
func (s *Store) UpsertEntityAlias(ctx context.Context, alias EntityAlias) error {
	if strings.TrimSpace(alias.RoomID) == "" || strings.TrimSpace(alias.Alias) == "" || strings.TrimSpace(alias.CanonicalEntity) == "" {
		return errors.New("store: entity alias requires room_id, alias, and canonical_entity")
	}
	if alias.UpdatedAtMs == 0 {
		alias.UpdatedAtMs = time.Now().UnixMilli()
	}
	_, err := s.writeDB.ExecContext(
		ctx,
		`INSERT INTO entity_aliases (room_id, alias, canonical_entity, sender_id, updated_at_ms)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(room_id, alias) DO UPDATE SET
			canonical_entity = excluded.canonical_entity,
			sender_id = excluded.sender_id,
			updated_at_ms = excluded.updated_at_ms`,
		alias.RoomID, alias.Alias, alias.CanonicalEntity, alias.SenderID, alias.UpdatedAtMs,
	)
	if err != nil {
		return fmt.Errorf("store: upsert entity alias %q: %w", alias.Alias, err)
	}
	return nil
}

// ListEntityAliases lists all alias mappings for roomID.
func (s *Store) ListEntityAliases(ctx context.Context, roomID string) ([]EntityAlias, error) {
	rows, err := s.readDB.QueryContext(
		ctx,
		`SELECT room_id, alias, canonical_entity, sender_id, updated_at_ms
		 FROM entity_aliases
		 WHERE room_id = ?
		 ORDER BY alias ASC`,
		roomID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list entity aliases: %w", err)
	}
	defer rows.Close()

	var out []EntityAlias
	for rows.Next() {
		var a EntityAlias
		if err := rows.Scan(&a.RoomID, &a.Alias, &a.CanonicalEntity, &a.SenderID, &a.UpdatedAtMs); err != nil {
			return nil, fmt.Errorf("store: scan entity alias: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate entity aliases: %w", err)
	}
	return out, nil
}

// DeleteEntityAlias removes a single alias mapping.
func (s *Store) DeleteEntityAlias(ctx context.Context, roomID, alias string) error {
	if _, err := s.writeDB.ExecContext(
		ctx,
		`DELETE FROM entity_aliases WHERE room_id = ? AND alias = ?`,
		roomID, alias,
	); err != nil {
		return fmt.Errorf("store: delete entity alias %q: %w", alias, err)
	}
	return nil
}

func (s *Store) loadMemoryEntities(ctx context.Context, memoryID string) ([]string, error) {
	rows, err := s.readDB.QueryContext(
		ctx,
		`SELECT canonical_entity FROM memory_entities WHERE memory_id = ? ORDER BY canonical_entity ASC`,
		memoryID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: load memory entities %q: %w", memoryID, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var ent string
		if err := rows.Scan(&ent); err != nil {
			return nil, fmt.Errorf("store: scan memory entity: %w", err)
		}
		out = append(out, ent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate memory entities: %w", err)
	}
	return out, nil
}

func dedupeNonEmpty(items []string) []string {
	var out []string
	for _, raw := range items {
		s := strings.TrimSpace(raw)
		if s == "" || slices.Contains(out, s) {
			continue
		}
		out = append(out, s)
	}
	return out
}
