package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PADState holds the current continuous 3D PAD coordinates for a room.
type PADState struct {
	RoomID      string  `json:"room_id"`
	P           float64 `json:"p"`
	A           float64 `json:"a"`
	D           float64 `json:"d"`
	PrototypeID string  `json:"prototype_id"`
	UpdatedAtMs int64   `json:"updated_at_ms"`
}

// PADHistoryEntry records a single PAD state transition and stimulus delta for telemetry and drift EMA.
type PADHistoryEntry struct {
	ID          int64   `json:"id"`
	RoomID      string  `json:"room_id"`
	RunID       string  `json:"run_id"`
	P           float64 `json:"p"`
	A           float64 `json:"a"`
	D           float64 `json:"d"`
	DeltaP      float64 `json:"delta_p"`
	DeltaA      float64 `json:"delta_a"`
	DeltaD      float64 `json:"delta_d"`
	PrototypeID string  `json:"prototype_id"`
	CreatedAtMs int64   `json:"created_at_ms"`
}

// GetPADState retrieves the latest PAD state for roomID.
func (s *Store) GetPADState(ctx context.Context, roomID string) (PADState, bool, error) {
	var out PADState
	err := s.readDB.QueryRowContext(
		ctx,
		`SELECT room_id, p, a, d, prototype_id, updated_at_ms FROM pad_states WHERE room_id = ?`,
		roomID,
	).Scan(&out.RoomID, &out.P, &out.A, &out.D, &out.PrototypeID, &out.UpdatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return PADState{}, false, nil
	}
	if err != nil {
		return PADState{}, false, fmt.Errorf("store: get pad state %q: %w", roomID, err)
	}
	return out, true, nil
}

// UpsertPADState saves the latest PAD state for a room.
func (s *Store) UpsertPADState(ctx context.Context, state PADState) error {
	if state.RoomID == "" {
		return errors.New("store: pad state room_id must not be empty")
	}
	if state.UpdatedAtMs == 0 {
		state.UpdatedAtMs = time.Now().UnixMilli()
	}
	_, err := s.writeDB.ExecContext(
		ctx,
		`INSERT INTO pad_states (room_id, p, a, d, prototype_id, updated_at_ms)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(room_id) DO UPDATE SET
			p = excluded.p,
			a = excluded.a,
			d = excluded.d,
			prototype_id = excluded.prototype_id,
			updated_at_ms = excluded.updated_at_ms`,
		state.RoomID, state.P, state.A, state.D, state.PrototypeID, state.UpdatedAtMs,
	)
	if err != nil {
		return fmt.Errorf("store: upsert pad state %q: %w", state.RoomID, err)
	}
	return nil
}

// AppendPADHistory appends a PAD transition record and returns its row ID.
func (s *Store) AppendPADHistory(ctx context.Context, entry PADHistoryEntry) (int64, error) {
	if entry.RoomID == "" {
		return 0, errors.New("store: pad history room_id must not be empty")
	}
	if entry.CreatedAtMs == 0 {
		entry.CreatedAtMs = time.Now().UnixMilli()
	}
	res, err := s.writeDB.ExecContext(
		ctx,
		`INSERT INTO pad_history (
			room_id, run_id, p, a, d, delta_p, delta_a, delta_d, prototype_id, created_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.RoomID, entry.RunID, entry.P, entry.A, entry.D,
		entry.DeltaP, entry.DeltaA, entry.DeltaD, entry.PrototypeID, entry.CreatedAtMs,
	)
	if err != nil {
		return 0, fmt.Errorf("store: append pad history: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: pad history last insert id: %w", err)
	}
	return id, nil
}

// ListPADHistory returns recent PAD history entries for roomID ordered newest first.
func (s *Store) ListPADHistory(ctx context.Context, roomID string, limit int) ([]PADHistoryEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.readDB.QueryContext(
		ctx,
		`SELECT id, room_id, run_id, p, a, d, delta_p, delta_a, delta_d, prototype_id, created_at_ms
		 FROM pad_history
		 WHERE room_id = ?
		 ORDER BY created_at_ms DESC, id DESC
		 LIMIT ?`,
		roomID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list pad history: %w", err)
	}
	defer rows.Close()

	var out []PADHistoryEntry
	for rows.Next() {
		var e PADHistoryEntry
		if err := rows.Scan(
			&e.ID, &e.RoomID, &e.RunID, &e.P, &e.A, &e.D,
			&e.DeltaP, &e.DeltaA, &e.DeltaD, &e.PrototypeID, &e.CreatedAtMs,
		); err != nil {
			return nil, fmt.Errorf("store: scan pad history: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate pad history: %w", err)
	}
	return out, nil
}
