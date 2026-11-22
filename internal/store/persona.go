package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const DefaultPersonaRoomID = "default"

// PADTriple stores a 3D Pleasure-Arousal-Dominance vector in JSON-friendly form.
type PADTriple struct {
	P float64 `json:"p"`
	A float64 `json:"a"`
	D float64 `json:"d"`
}

// TemperamentParams stores congenital PAD dynamics serialized in persona_core.temperament_json.
type TemperamentParams struct {
	InitialBaseline PADTriple `json:"initial_baseline"`
	Baseline        PADTriple `json:"baseline"`
	Eta             PADTriple `json:"eta"`
	TauSec          PADTriple `json:"tau_sec"`
	Deadzone        float64   `json:"deadzone"`
}

// PersonaCore stores the frozen 1st-person beliefs, temperament dynamics, and optional verbatim legacy prompt.
type PersonaCore struct {
	RoomID       string            `json:"room_id"`
	Beliefs      string            `json:"beliefs"`
	Temperament  TemperamentParams `json:"temperament"`
	LegacyPrompt string            `json:"legacy_prompt,omitempty"`
	UpdatedAtMs  int64             `json:"updated_at_ms"`
}

// PersonaTrait is a single atomic 1st-person character trait in ordered display position.
type PersonaTrait struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	Position    int    `json:"position"`
	CreatedAtMs int64  `json:"created_at_ms"`
	UpdatedAtMs int64  `json:"updated_at_ms"`
}

// PersonaSnapshot is an immutable audit checkpoint capturing core + traits at a point in time.
type PersonaSnapshot struct {
	ID          int64          `json:"id"`
	Reason      string         `json:"reason"`
	Core        PersonaCore    `json:"core"`
	Traits      []PersonaTrait `json:"traits"`
	CreatedAtMs int64          `json:"created_at_ms"`
}

// TraitChange records a modified trait content between two persona snapshots.
type TraitChange struct {
	ID     string `json:"id"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// PersonaSnapshotDiff summarizes differences between two persona snapshots.
type PersonaSnapshotDiff struct {
	FromID             int64          `json:"from_id"`
	ToID               int64          `json:"to_id"`
	BeliefsChanged     bool           `json:"beliefs_changed"`
	TemperamentChanged bool           `json:"temperament_changed"`
	AddedTraits        []PersonaTrait `json:"added_traits,omitempty"`
	RemovedTraits      []PersonaTrait `json:"removed_traits,omitempty"`
	UpdatedTraits      []TraitChange  `json:"updated_traits,omitempty"`
}

// GetPersonaCore returns the stored core beliefs and temperament for roomID (defaulting empty to "default").
func (s *Store) GetPersonaCore(ctx context.Context, roomID string) (PersonaCore, bool, error) {
	if roomID == "" {
		roomID = DefaultPersonaRoomID
	}
	var out PersonaCore
	var tempJSON string
	err := s.readDB.QueryRowContext(
		ctx,
		`SELECT room_id, beliefs, temperament_json, legacy_prompt, updated_at_ms
		 FROM persona_core WHERE room_id = ?`,
		roomID,
	).Scan(&out.RoomID, &out.Beliefs, &tempJSON, &out.LegacyPrompt, &out.UpdatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return PersonaCore{}, false, nil
	}
	if err != nil {
		return PersonaCore{}, false, fmt.Errorf("store: get persona core %q: %w", roomID, err)
	}
	if strings.TrimSpace(tempJSON) != "" && tempJSON != "{}" {
		if err := json.Unmarshal([]byte(tempJSON), &out.Temperament); err != nil {
			return PersonaCore{}, false, fmt.Errorf("store: decode temperament json: %w", err)
		}
	}
	return out, true, nil
}

// SetPersonaCore writes core beliefs and temperament, optionally recording an immutable snapshot when snapshotReason != "".
func (s *Store) SetPersonaCore(ctx context.Context, core PersonaCore, snapshotReason string) error {
	if core.RoomID == "" {
		core.RoomID = DefaultPersonaRoomID
	}
	if core.UpdatedAtMs == 0 {
		core.UpdatedAtMs = time.Now().UnixMilli()
	}
	tempBytes, err := json.Marshal(core.Temperament)
	if err != nil {
		return fmt.Errorf("store: encode temperament: %w", err)
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin set persona core: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO persona_core (room_id, beliefs, temperament_json, legacy_prompt, updated_at_ms)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(room_id) DO UPDATE SET
			beliefs = excluded.beliefs,
			temperament_json = excluded.temperament_json,
			legacy_prompt = excluded.legacy_prompt,
			updated_at_ms = excluded.updated_at_ms`,
		core.RoomID, core.Beliefs, string(tempBytes), core.LegacyPrompt, core.UpdatedAtMs,
	)
	if err != nil {
		return fmt.Errorf("store: upsert persona core: %w", err)
	}

	if snapshotReason != "" {
		traits, err := listTraitsTx(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := insertPersonaSnapshotTx(ctx, tx, snapshotReason, core, traits, core.UpdatedAtMs); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit persona core: %w", err)
	}
	return nil
}

// ListPersonaTraits returns all atomic character traits ordered by position.
func (s *Store) ListPersonaTraits(ctx context.Context) ([]PersonaTrait, error) {
	rows, err := s.readDB.QueryContext(
		ctx,
		`SELECT id, content, position, created_at_ms, updated_at_ms
		 FROM persona_traits ORDER BY position ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list persona traits: %w", err)
	}
	defer rows.Close()
	return scanTraits(rows)
}

// ReplacePersonaTraits atomically replaces all character traits and appends an immutable persona snapshot.
func (s *Store) ReplacePersonaTraits(ctx context.Context, roomID string, traits []PersonaTrait, reason string) (int64, error) {
	if roomID == "" {
		roomID = DefaultPersonaRoomID
	}
	nowMs := time.Now().UnixMilli()

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin replace persona traits: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM persona_traits`); err != nil {
		return 0, fmt.Errorf("store: clear persona traits: %w", err)
	}

	normalized := make([]PersonaTrait, len(traits))
	for i, tr := range traits {
		if strings.TrimSpace(tr.ID) == "" {
			return 0, fmt.Errorf("store: persona trait at index %d has empty id", i)
		}
		if tr.CreatedAtMs == 0 {
			tr.CreatedAtMs = nowMs
		}
		if tr.UpdatedAtMs == 0 {
			tr.UpdatedAtMs = nowMs
		}
		if tr.Position == 0 {
			tr.Position = i + 1
		}
		normalized[i] = tr
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO persona_traits (id, content, position, created_at_ms, updated_at_ms)
			 VALUES (?, ?, ?, ?, ?)`,
			tr.ID, tr.Content, tr.Position, tr.CreatedAtMs, tr.UpdatedAtMs,
		); err != nil {
			return 0, fmt.Errorf("store: insert persona trait %q: %w", tr.ID, err)
		}
	}

	core, err := getPersonaCoreTx(ctx, tx, roomID)
	if err != nil {
		return 0, err
	}
	snapID, err := insertPersonaSnapshotTx(ctx, tx, reason, core, normalized, nowMs)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit replace persona traits: %w", err)
	}
	return snapID, nil
}

// ListPersonaSnapshots returns recent persona snapshots ordered newest first.
func (s *Store) ListPersonaSnapshots(ctx context.Context, limit int) ([]PersonaSnapshot, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.readDB.QueryContext(
		ctx,
		`SELECT id, reason, core_json, traits_json, created_at_ms
		 FROM persona_snapshots ORDER BY id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list persona snapshots: %w", err)
	}
	defer rows.Close()

	var out []PersonaSnapshot
	for rows.Next() {
		snap, err := scanPersonaSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate persona snapshots: %w", err)
	}
	return out, nil
}

// GetPersonaSnapshot retrieves a single persona snapshot by ID.
func (s *Store) GetPersonaSnapshot(ctx context.Context, id int64) (PersonaSnapshot, bool, error) {
	var snap PersonaSnapshot
	var coreJSON, traitsJSON string
	err := s.readDB.QueryRowContext(
		ctx,
		`SELECT id, reason, core_json, traits_json, created_at_ms
		 FROM persona_snapshots WHERE id = ?`,
		id,
	).Scan(&snap.ID, &snap.Reason, &coreJSON, &traitsJSON, &snap.CreatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return PersonaSnapshot{}, false, nil
	}
	if err != nil {
		return PersonaSnapshot{}, false, fmt.Errorf("store: get persona snapshot %d: %w", id, err)
	}
	if err := decodeSnapshotPayloads(coreJSON, traitsJSON, &snap); err != nil {
		return PersonaSnapshot{}, false, err
	}
	return snap, true, nil
}

// DiffPersonaSnapshots computes structural changes between two persona snapshots.
func (s *Store) DiffPersonaSnapshots(ctx context.Context, fromID, toID int64) (PersonaSnapshotDiff, error) {
	fromSnap, ok, err := s.GetPersonaSnapshot(ctx, fromID)
	if err != nil {
		return PersonaSnapshotDiff{}, err
	}
	if !ok {
		return PersonaSnapshotDiff{}, fmt.Errorf("store: snapshot %d not found", fromID)
	}
	toSnap, ok, err := s.GetPersonaSnapshot(ctx, toID)
	if err != nil {
		return PersonaSnapshotDiff{}, err
	}
	if !ok {
		return PersonaSnapshotDiff{}, fmt.Errorf("store: snapshot %d not found", toID)
	}

	diff := PersonaSnapshotDiff{
		FromID:             fromID,
		ToID:               toID,
		BeliefsChanged:     fromSnap.Core.Beliefs != toSnap.Core.Beliefs,
		TemperamentChanged: fromSnap.Core.Temperament != toSnap.Core.Temperament,
	}

	fromByID := make(map[string]PersonaTrait, len(fromSnap.Traits))
	for _, tr := range fromSnap.Traits {
		fromByID[tr.ID] = tr
	}
	toByID := make(map[string]PersonaTrait, len(toSnap.Traits))
	for _, tr := range toSnap.Traits {
		toByID[tr.ID] = tr
		prev, existed := fromByID[tr.ID]
		if !existed {
			diff.AddedTraits = append(diff.AddedTraits, tr)
		} else if prev.Content != tr.Content {
			diff.UpdatedTraits = append(diff.UpdatedTraits, TraitChange{
				ID:     tr.ID,
				Before: prev.Content,
				After:  tr.Content,
			})
		}
	}
	for _, tr := range fromSnap.Traits {
		if _, exists := toByID[tr.ID]; !exists {
			diff.RemovedTraits = append(diff.RemovedTraits, tr)
		}
	}
	return diff, nil
}

func getPersonaCoreTx(ctx context.Context, tx *sql.Tx, roomID string) (PersonaCore, error) {
	var out PersonaCore
	var tempJSON string
	err := tx.QueryRowContext(
		ctx,
		`SELECT room_id, beliefs, temperament_json, legacy_prompt, updated_at_ms
		 FROM persona_core WHERE room_id = ?`,
		roomID,
	).Scan(&out.RoomID, &out.Beliefs, &tempJSON, &out.LegacyPrompt, &out.UpdatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return PersonaCore{RoomID: roomID}, nil
	}
	if err != nil {
		return PersonaCore{}, fmt.Errorf("store: read persona core in tx: %w", err)
	}
	if strings.TrimSpace(tempJSON) != "" && tempJSON != "{}" {
		if err := json.Unmarshal([]byte(tempJSON), &out.Temperament); err != nil {
			return PersonaCore{}, fmt.Errorf("store: decode temperament in tx: %w", err)
		}
	}
	return out, nil
}

func listTraitsTx(ctx context.Context, tx *sql.Tx) ([]PersonaTrait, error) {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT id, content, position, created_at_ms, updated_at_ms
		 FROM persona_traits ORDER BY position ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list persona traits in tx: %w", err)
	}
	defer rows.Close()
	return scanTraits(rows)
}

func insertPersonaSnapshotTx(ctx context.Context, tx *sql.Tx, reason string, core PersonaCore, traits []PersonaTrait, createdAtMs int64) (int64, error) {
	coreBytes, err := json.Marshal(core)
	if err != nil {
		return 0, fmt.Errorf("store: marshal snapshot core: %w", err)
	}
	if traits == nil {
		traits = []PersonaTrait{}
	}
	traitsBytes, err := json.Marshal(traits)
	if err != nil {
		return 0, fmt.Errorf("store: marshal snapshot traits: %w", err)
	}
	res, err := tx.ExecContext(
		ctx,
		`INSERT INTO persona_snapshots (reason, core_json, traits_json, created_at_ms)
		 VALUES (?, ?, ?, ?)`,
		reason, string(coreBytes), string(traitsBytes), createdAtMs,
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert persona snapshot: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: snapshot last insert id: %w", err)
	}
	return id, nil
}

func scanTraits(rows *sql.Rows) ([]PersonaTrait, error) {
	var out []PersonaTrait
	for rows.Next() {
		var tr PersonaTrait
		if err := rows.Scan(&tr.ID, &tr.Content, &tr.Position, &tr.CreatedAtMs, &tr.UpdatedAtMs); err != nil {
			return nil, fmt.Errorf("store: scan persona trait: %w", err)
		}
		out = append(out, tr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate persona traits: %w", err)
	}
	return out, nil
}

func scanPersonaSnapshot(rows *sql.Rows) (PersonaSnapshot, error) {
	var snap PersonaSnapshot
	var coreJSON, traitsJSON string
	if err := rows.Scan(&snap.ID, &snap.Reason, &coreJSON, &traitsJSON, &snap.CreatedAtMs); err != nil {
		return PersonaSnapshot{}, fmt.Errorf("store: scan persona snapshot: %w", err)
	}
	if err := decodeSnapshotPayloads(coreJSON, traitsJSON, &snap); err != nil {
		return PersonaSnapshot{}, err
	}
	return snap, nil
}

func decodeSnapshotPayloads(coreJSON, traitsJSON string, snap *PersonaSnapshot) error {
	if err := json.Unmarshal([]byte(coreJSON), &snap.Core); err != nil {
		return fmt.Errorf("store: decode snapshot core: %w", err)
	}
	if err := json.Unmarshal([]byte(traitsJSON), &snap.Traits); err != nil {
		return fmt.Errorf("store: decode snapshot traits: %w", err)
	}
	return nil
}
