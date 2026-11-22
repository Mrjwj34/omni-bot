package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RunTrace stores structured observability data for a single Kernel checkpoint or agent run.
type RunTrace struct {
	RunID         string `json:"run_id"`
	RoomID        string `json:"room_id"`
	TriggerSource string `json:"trigger_source"`
	StartedAtMs   int64  `json:"started_at_ms"`
	FinishedAtMs  int64  `json:"finished_at_ms"`
	Status        string `json:"status"`
	GateJSON      string `json:"gate_json"`
	PADJSON       string `json:"pad_json"`
	MemoriesJSON  string `json:"memories_json"`
	StepsJSON     string `json:"steps_json"`
	ActionsJSON   string `json:"actions_json"`
	ErrorText     string `json:"error_text"`
}

// UpsertRunTrace inserts or updates a run trace record.
func (s *Store) UpsertRunTrace(ctx context.Context, trace RunTrace) error {
	if strings.TrimSpace(trace.RunID) == "" {
		return errors.New("store: run_trace run_id must not be empty")
	}
	if trace.StartedAtMs == 0 {
		trace.StartedAtMs = time.Now().UnixMilli()
	}
	if trace.Status == "" {
		trace.Status = "running"
	}
	if trace.GateJSON == "" {
		trace.GateJSON = "{}"
	}
	if trace.PADJSON == "" {
		trace.PADJSON = "{}"
	}
	if trace.MemoriesJSON == "" {
		trace.MemoriesJSON = "[]"
	}
	if trace.StepsJSON == "" {
		trace.StepsJSON = "[]"
	}
	if trace.ActionsJSON == "" {
		trace.ActionsJSON = "[]"
	}

	_, err := s.writeDB.ExecContext(
		ctx,
		`INSERT INTO run_traces (
			run_id, room_id, trigger_source, started_at_ms, finished_at_ms, status,
			gate_json, pad_json, memories_json, steps_json, actions_json, error_text
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET
			room_id = excluded.room_id,
			trigger_source = excluded.trigger_source,
			started_at_ms = excluded.started_at_ms,
			finished_at_ms = excluded.finished_at_ms,
			status = excluded.status,
			gate_json = excluded.gate_json,
			pad_json = excluded.pad_json,
			memories_json = excluded.memories_json,
			steps_json = excluded.steps_json,
			actions_json = excluded.actions_json,
			error_text = excluded.error_text`,
		trace.RunID, trace.RoomID, trace.TriggerSource, trace.StartedAtMs, trace.FinishedAtMs, trace.Status,
		trace.GateJSON, trace.PADJSON, trace.MemoriesJSON, trace.StepsJSON, trace.ActionsJSON, trace.ErrorText,
	)
	if err != nil {
		return fmt.Errorf("store: upsert run trace %q: %w", trace.RunID, err)
	}
	return nil
}

// GetRunTrace retrieves a single run trace by runID.
func (s *Store) GetRunTrace(ctx context.Context, runID string) (RunTrace, bool, error) {
	var tr RunTrace
	err := s.readDB.QueryRowContext(
		ctx,
		`SELECT run_id, room_id, trigger_source, started_at_ms, finished_at_ms, status,
		        gate_json, pad_json, memories_json, steps_json, actions_json, error_text
		 FROM run_traces WHERE run_id = ?`,
		runID,
	).Scan(
		&tr.RunID, &tr.RoomID, &tr.TriggerSource, &tr.StartedAtMs, &tr.FinishedAtMs, &tr.Status,
		&tr.GateJSON, &tr.PADJSON, &tr.MemoriesJSON, &tr.StepsJSON, &tr.ActionsJSON, &tr.ErrorText,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RunTrace{}, false, nil
	}
	if err != nil {
		return RunTrace{}, false, fmt.Errorf("store: get run trace %q: %w", runID, err)
	}
	return tr, true, nil
}

// ListRunTraces returns recent run traces (optionally filtered by roomID) ordered newest first.
func (s *Store) ListRunTraces(ctx context.Context, roomID string, limit int) ([]RunTrace, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if roomID != "" {
		rows, err = s.readDB.QueryContext(
			ctx,
			`SELECT run_id, room_id, trigger_source, started_at_ms, finished_at_ms, status,
			        gate_json, pad_json, memories_json, steps_json, actions_json, error_text
			 FROM run_traces
			 WHERE room_id = ?
			 ORDER BY started_at_ms DESC, run_id DESC
			 LIMIT ?`,
			roomID, limit,
		)
	} else {
		rows, err = s.readDB.QueryContext(
			ctx,
			`SELECT run_id, room_id, trigger_source, started_at_ms, finished_at_ms, status,
			        gate_json, pad_json, memories_json, steps_json, actions_json, error_text
			 FROM run_traces
			 ORDER BY started_at_ms DESC, run_id DESC
			 LIMIT ?`,
			limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("store: list run traces: %w", err)
	}
	defer rows.Close()

	var out []RunTrace
	for rows.Next() {
		var tr RunTrace
		if err := rows.Scan(
			&tr.RunID, &tr.RoomID, &tr.TriggerSource, &tr.StartedAtMs, &tr.FinishedAtMs, &tr.Status,
			&tr.GateJSON, &tr.PADJSON, &tr.MemoriesJSON, &tr.StepsJSON, &tr.ActionsJSON, &tr.ErrorText,
		); err != nil {
			return nil, fmt.Errorf("store: scan run trace: %w", err)
		}
		out = append(out, tr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate run traces: %w", err)
	}
	return out, nil
}
