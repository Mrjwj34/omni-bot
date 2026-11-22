package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	MemeStatusActive  = "active"
	MemeStatusDeleted = "deleted"
)

// Meme represents a collected reaction sticker/meme with visual metadata and optional embedding.
type Meme struct {
	Hash         string    `json:"hash"`
	RelPath      string    `json:"rel_path"`
	ThumbRelPath string    `json:"thumb_rel_path"`
	PHash        string    `json:"phash"`
	OverlayText  string    `json:"overlay_text"`
	Description  string    `json:"description"`
	Emotions     string    `json:"emotions"`
	Scenes       string    `json:"scenes"`
	SearchBlob   string    `json:"search_blob"`
	Embedding    []float32 `json:"embedding,omitempty"`
	EmbeddingDim int       `json:"embedding_dim"`
	UseCount     int       `json:"use_count"`
	Status       string    `json:"status"`
	CreatedAtMs  int64     `json:"created_at_ms"`
	UpdatedAtMs  int64     `json:"updated_at_ms"`
}

// MemeSeen tracks reviewed or blocked image hashes to prevent duplicate vision evaluation or re-collection.
type MemeSeen struct {
	Hash        string `json:"hash"`
	PHash       string `json:"phash"`
	Status      string `json:"status"`
	Label       string `json:"label"`
	CreatedAtMs int64  `json:"created_at_ms"`
}

// UpsertMeme inserts or updates a meme entry.
func (s *Store) UpsertMeme(ctx context.Context, meme Meme) error {
	if strings.TrimSpace(meme.Hash) == "" {
		return errors.New("store: meme hash must not be empty")
	}
	nowMs := time.Now().UnixMilli()
	if meme.CreatedAtMs == 0 {
		meme.CreatedAtMs = nowMs
	}
	if meme.UpdatedAtMs == 0 {
		meme.UpdatedAtMs = nowMs
	}
	if meme.Status == "" {
		meme.Status = MemeStatusActive
	}
	embBlob := EncodeFloat32LE(meme.Embedding)
	embDim := meme.EmbeddingDim
	if embDim == 0 {
		embDim = len(meme.Embedding)
	}

	_, err := s.writeDB.ExecContext(
		ctx,
		`INSERT INTO memes (
			hash, rel_path, thumb_rel_path, phash, overlay_text, description,
			emotions, scenes, search_blob, embedding, embedding_dim,
			use_count, status, created_at_ms, updated_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(hash) DO UPDATE SET
			rel_path = excluded.rel_path,
			thumb_rel_path = excluded.thumb_rel_path,
			phash = excluded.phash,
			overlay_text = excluded.overlay_text,
			description = excluded.description,
			emotions = excluded.emotions,
			scenes = excluded.scenes,
			search_blob = excluded.search_blob,
			embedding = excluded.embedding,
			embedding_dim = excluded.embedding_dim,
			use_count = excluded.use_count,
			status = excluded.status,
			updated_at_ms = excluded.updated_at_ms`,
		meme.Hash, meme.RelPath, meme.ThumbRelPath, meme.PHash, meme.OverlayText, meme.Description,
		meme.Emotions, meme.Scenes, meme.SearchBlob, embBlob, embDim,
		meme.UseCount, meme.Status, meme.CreatedAtMs, meme.UpdatedAtMs,
	)
	if err != nil {
		return fmt.Errorf("store: upsert meme %q: %w", meme.Hash, err)
	}
	return nil
}

// GetMeme retrieves a meme by exact hash.
func (s *Store) GetMeme(ctx context.Context, hash string) (Meme, bool, error) {
	row := s.readDB.QueryRowContext(
		ctx,
		`SELECT hash, rel_path, thumb_rel_path, phash, overlay_text, description,
		        emotions, scenes, search_blob, embedding, embedding_dim,
		        use_count, status, created_at_ms, updated_at_ms
		 FROM memes WHERE hash = ?`,
		hash,
	)
	m, err := scanMeme(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Meme{}, false, nil
	}
	if err != nil {
		return Meme{}, false, fmt.Errorf("store: get meme %q: %w", hash, err)
	}
	return m, true, nil
}

// FindMemeByPrefix resolves an active meme by hash prefix (e.g. from inline [meme:#2ecd45]).
func (s *Store) FindMemeByPrefix(ctx context.Context, prefix string) (Meme, bool, error) {
	prefix = strings.TrimPrefix(strings.TrimSpace(prefix), "#")
	if prefix == "" {
		return Meme{}, false, nil
	}
	row := s.readDB.QueryRowContext(
		ctx,
		`SELECT hash, rel_path, thumb_rel_path, phash, overlay_text, description,
		        emotions, scenes, search_blob, embedding, embedding_dim,
		        use_count, status, created_at_ms, updated_at_ms
		 FROM memes
		 WHERE status = ? AND hash LIKE ?
		 ORDER BY use_count DESC, hash ASC
		 LIMIT 1`,
		MemeStatusActive, prefix+"%",
	)
	m, err := scanMeme(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Meme{}, false, nil
	}
	if err != nil {
		return Meme{}, false, fmt.Errorf("store: find meme by prefix %q: %w", prefix, err)
	}
	return m, true, nil
}

// SearchMemes searches active memes by substring across search_blob, description, emotions, scenes, and overlay_text.
func (s *Store) SearchMemes(ctx context.Context, query string, limit int) ([]Meme, error) {
	if limit <= 0 {
		limit = 20
	}
	q := strings.TrimSpace(query)
	var rows *sql.Rows
	var err error
	if q == "" {
		rows, err = s.readDB.QueryContext(
			ctx,
			`SELECT hash, rel_path, thumb_rel_path, phash, overlay_text, description,
			        emotions, scenes, search_blob, embedding, embedding_dim,
			        use_count, status, created_at_ms, updated_at_ms
			 FROM memes
			 WHERE status = ?
			 ORDER BY use_count DESC, updated_at_ms DESC
			 LIMIT ?`,
			MemeStatusActive, limit,
		)
	} else {
		like := "%" + q + "%"
		rows, err = s.readDB.QueryContext(
			ctx,
			`SELECT hash, rel_path, thumb_rel_path, phash, overlay_text, description,
			        emotions, scenes, search_blob, embedding, embedding_dim,
			        use_count, status, created_at_ms, updated_at_ms
			 FROM memes
			 WHERE status = ?
			   AND (search_blob LIKE ? OR description LIKE ? OR emotions LIKE ? OR scenes LIKE ? OR overlay_text LIKE ?)
			 ORDER BY use_count DESC, updated_at_ms DESC
			 LIMIT ?`,
			MemeStatusActive, like, like, like, like, like, limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("store: search memes: %w", err)
	}
	defer rows.Close()
	return scanMemeRows(rows)
}

// ListActiveMemes returns all active memes ordered by usage and recency.
func (s *Store) ListActiveMemes(ctx context.Context) ([]Meme, error) {
	rows, err := s.readDB.QueryContext(
		ctx,
		`SELECT hash, rel_path, thumb_rel_path, phash, overlay_text, description,
		        emotions, scenes, search_blob, embedding, embedding_dim,
		        use_count, status, created_at_ms, updated_at_ms
		 FROM memes
		 WHERE status = ?
		 ORDER BY use_count DESC, created_at_ms DESC`,
		MemeStatusActive,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list active memes: %w", err)
	}
	defer rows.Close()
	return scanMemeRows(rows)
}

// IncrementMemeUse increments use_count and updates updated_at_ms for an active meme.
func (s *Store) IncrementMemeUse(ctx context.Context, hash string, nowMs int64) error {
	if nowMs == 0 {
		nowMs = time.Now().UnixMilli()
	}
	if _, err := s.writeDB.ExecContext(
		ctx,
		`UPDATE memes SET use_count = use_count + 1, updated_at_ms = ? WHERE hash = ?`,
		nowMs, hash,
	); err != nil {
		return fmt.Errorf("store: increment meme use %q: %w", hash, err)
	}
	return nil
}

// DeleteMeme soft-deletes a meme and marks its hash deleted in meme_seen so it is not re-collected.
func (s *Store) DeleteMeme(ctx context.Context, hash string, nowMs int64) error {
	if nowMs == 0 {
		nowMs = time.Now().UnixMilli()
	}
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin delete meme: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var phash string
	_ = tx.QueryRowContext(ctx, `SELECT phash FROM memes WHERE hash = ?`, hash).Scan(&phash)

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE memes SET status = ?, updated_at_ms = ? WHERE hash = ?`,
		MemeStatusDeleted, nowMs, hash,
	); err != nil {
		return fmt.Errorf("store: mark meme deleted %q: %w", hash, err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO meme_seen (hash, phash, status, label, created_at_ms)
		 VALUES (?, ?, ?, 'deleted', ?)
		 ON CONFLICT(hash) DO UPDATE SET status = excluded.status, label = excluded.label`,
		hash, phash, MemeStatusDeleted, nowMs,
	); err != nil {
		return fmt.Errorf("store: mark meme_seen deleted %q: %w", hash, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit delete meme %q: %w", hash, err)
	}
	return nil
}

// MarkMemeSeen records a seen/approved/rejected/deleted meme hash.
func (s *Store) MarkMemeSeen(ctx context.Context, seen MemeSeen) error {
	if strings.TrimSpace(seen.Hash) == "" {
		return errors.New("store: meme_seen hash must not be empty")
	}
	if seen.Status == "" {
		seen.Status = "seen"
	}
	if seen.CreatedAtMs == 0 {
		seen.CreatedAtMs = time.Now().UnixMilli()
	}
	_, err := s.writeDB.ExecContext(
		ctx,
		`INSERT INTO meme_seen (hash, phash, status, label, created_at_ms)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(hash) DO UPDATE SET
			phash = excluded.phash,
			status = excluded.status,
			label = excluded.label`,
		seen.Hash, seen.PHash, seen.Status, seen.Label, seen.CreatedAtMs,
	)
	if err != nil {
		return fmt.Errorf("store: mark meme seen %q: %w", seen.Hash, err)
	}
	return nil
}

// GetMemeSeen looks up a meme_seen entry by hash.
func (s *Store) GetMemeSeen(ctx context.Context, hash string) (MemeSeen, bool, error) {
	var out MemeSeen
	err := s.readDB.QueryRowContext(
		ctx,
		`SELECT hash, phash, status, label, created_at_ms FROM meme_seen WHERE hash = ?`,
		hash,
	).Scan(&out.Hash, &out.PHash, &out.Status, &out.Label, &out.CreatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return MemeSeen{}, false, nil
	}
	if err != nil {
		return MemeSeen{}, false, fmt.Errorf("store: get meme seen %q: %w", hash, err)
	}
	return out, true, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMeme(row rowScanner) (Meme, error) {
	var m Meme
	var embBlob []byte
	if err := row.Scan(
		&m.Hash, &m.RelPath, &m.ThumbRelPath, &m.PHash, &m.OverlayText, &m.Description,
		&m.Emotions, &m.Scenes, &m.SearchBlob, &embBlob, &m.EmbeddingDim,
		&m.UseCount, &m.Status, &m.CreatedAtMs, &m.UpdatedAtMs,
	); err != nil {
		return Meme{}, err
	}
	if len(embBlob) > 0 {
		vec, err := DecodeFloat32LE(embBlob)
		if err != nil {
			return Meme{}, err
		}
		m.Embedding = vec
		if m.EmbeddingDim == 0 {
			m.EmbeddingDim = len(vec)
		}
	}
	return m, nil
}

func scanMemeRows(rows *sql.Rows) ([]Meme, error) {
	var out []Meme
	for rows.Next() {
		m, err := scanMeme(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan meme: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate memes: %w", err)
	}
	return out, nil
}
