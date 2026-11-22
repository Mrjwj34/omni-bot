package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// TimelineMessage represents a single normalized message in the append-only room timeline.
type TimelineMessage struct {
	SeqID            int64  `json:"seq_id"`
	ChannelID        string `json:"channel_id"`
	RoomID           string `json:"room_id"`
	MsgID            string `json:"msg_id"`
	SenderID         string `json:"sender_id"`
	SenderName       string `json:"sender_name"`
	IsBot            bool   `json:"is_bot"`
	IsAtBot          bool   `json:"is_at_bot"`
	TimestampMs      int64  `json:"timestamp_ms"`
	ContentJSON      string `json:"content_json"`
	RenderedText     string `json:"rendered_text"`
	Status           string `json:"status"`
	ArchivedToMemory bool   `json:"archived_to_memory"`
}

// RoomSummary holds the rolling narrative summary and watermark sequence ID for a room.
type RoomSummary struct {
	RoomID            string `json:"room_id"`
	SummaryText       string `json:"summary_text"`
	LastSummarizedSeq int64  `json:"last_summarized_seq"`
	UpdatedAtMs       int64  `json:"updated_at_ms"`
}

// AppendTimelineMessage appends a message (or upserts by explicit SeqID when msg.SeqID > 0).
func (s *Store) AppendTimelineMessage(ctx context.Context, msg TimelineMessage) (int64, error) {
	if msg.RoomID == "" {
		return 0, errors.New("store: timeline room_id must not be empty")
	}
	if msg.ContentJSON == "" {
		msg.ContentJSON = "[]"
	}
	if msg.Status == "" {
		msg.Status = "UNPROCESSED"
	}

	if msg.SeqID > 0 {
		_, err := s.writeDB.ExecContext(
			ctx,
			`INSERT INTO timeline_messages (
				seq_id, channel_id, room_id, msg_id, sender_id, sender_name,
				is_bot, is_at_bot, timestamp_ms, content_json, rendered_text, status, archived_to_memory
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(seq_id) DO UPDATE SET
				channel_id = excluded.channel_id,
				room_id = excluded.room_id,
				msg_id = excluded.msg_id,
				sender_id = excluded.sender_id,
				sender_name = excluded.sender_name,
				is_bot = excluded.is_bot,
				is_at_bot = excluded.is_at_bot,
				timestamp_ms = excluded.timestamp_ms,
				content_json = excluded.content_json,
				rendered_text = excluded.rendered_text,
				status = excluded.status,
				archived_to_memory = excluded.archived_to_memory`,
			msg.SeqID, msg.ChannelID, msg.RoomID, msg.MsgID, msg.SenderID, msg.SenderName,
			boolToInt(msg.IsBot), boolToInt(msg.IsAtBot), msg.TimestampMs,
			msg.ContentJSON, msg.RenderedText, msg.Status, boolToInt(msg.ArchivedToMemory),
		)
		if err != nil {
			return 0, fmt.Errorf("store: upsert timeline message seq=%d: %w", msg.SeqID, err)
		}
		return msg.SeqID, nil
	}

	res, err := s.writeDB.ExecContext(
		ctx,
		`INSERT INTO timeline_messages (
			channel_id, room_id, msg_id, sender_id, sender_name,
			is_bot, is_at_bot, timestamp_ms, content_json, rendered_text, status, archived_to_memory
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ChannelID, msg.RoomID, msg.MsgID, msg.SenderID, msg.SenderName,
		boolToInt(msg.IsBot), boolToInt(msg.IsAtBot), msg.TimestampMs,
		msg.ContentJSON, msg.RenderedText, msg.Status, boolToInt(msg.ArchivedToMemory),
	)
	if err != nil {
		return 0, fmt.Errorf("store: append timeline message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: timeline last insert id: %w", err)
	}
	return id, nil
}

// ListRecentTimeline returns up to limit most recent messages for roomID in chronological order.
func (s *Store) ListRecentTimeline(ctx context.Context, roomID string, limit int) ([]TimelineMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.readDB.QueryContext(
		ctx,
		`SELECT seq_id, channel_id, room_id, msg_id, sender_id, sender_name,
		        is_bot, is_at_bot, timestamp_ms, content_json, rendered_text, status, archived_to_memory
		 FROM timeline_messages
		 WHERE room_id = ?
		 ORDER BY seq_id DESC
		 LIMIT ?`,
		roomID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list recent timeline: %w", err)
	}
	defer rows.Close()

	msgs, err := scanTimelineRows(rows)
	if err != nil {
		return nil, err
	}
	slices.Reverse(msgs)
	return msgs, nil
}

// ListUnarchivedTimeline returns messages not yet archived to memory, ordered oldest first.
func (s *Store) ListUnarchivedTimeline(ctx context.Context, roomID string, limit int) ([]TimelineMessage, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.readDB.QueryContext(
		ctx,
		`SELECT seq_id, channel_id, room_id, msg_id, sender_id, sender_name,
		        is_bot, is_at_bot, timestamp_ms, content_json, rendered_text, status, archived_to_memory
		 FROM timeline_messages
		 WHERE room_id = ? AND archived_to_memory = 0
		 ORDER BY seq_id ASC
		 LIMIT ?`,
		roomID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list unarchived timeline: %w", err)
	}
	defer rows.Close()

	return scanTimelineRows(rows)
}

// MarkTimelineArchived marks the specified sequence IDs as archived to long-term memory.
func (s *Store) MarkTimelineArchived(ctx context.Context, seqIDs []int64) error {
	if len(seqIDs) == 0 {
		return nil
	}
	placeholders, args := int64InClause(seqIDs)
	query := `UPDATE timeline_messages SET archived_to_memory = 1 WHERE seq_id IN (` + placeholders + `)`
	if _, err := s.writeDB.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("store: mark timeline archived: %w", err)
	}
	return nil
}

// UpdateTimelineStatus updates the processing status for the specified sequence IDs.
func (s *Store) UpdateTimelineStatus(ctx context.Context, seqIDs []int64, status string) error {
	if len(seqIDs) == 0 {
		return nil
	}
	placeholders, idArgs := int64InClause(seqIDs)
	args := make([]any, 0, len(idArgs)+1)
	args = append(args, status)
	args = append(args, idArgs...)
	query := `UPDATE timeline_messages SET status = ? WHERE seq_id IN (` + placeholders + `)`
	if _, err := s.writeDB.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("store: update timeline status: %w", err)
	}
	return nil
}

// GetRoomSummary fetches the rolling summary for roomID.
func (s *Store) GetRoomSummary(ctx context.Context, roomID string) (RoomSummary, bool, error) {
	var out RoomSummary
	err := s.readDB.QueryRowContext(
		ctx,
		`SELECT room_id, summary_text, last_summarized_seq, updated_at_ms
		 FROM room_summaries WHERE room_id = ?`,
		roomID,
	).Scan(&out.RoomID, &out.SummaryText, &out.LastSummarizedSeq, &out.UpdatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return RoomSummary{}, false, nil
	}
	if err != nil {
		return RoomSummary{}, false, fmt.Errorf("store: get room summary %q: %w", roomID, err)
	}
	return out, true, nil
}

// UpsertRoomSummary saves or replaces the rolling summary for a room.
func (s *Store) UpsertRoomSummary(ctx context.Context, summary RoomSummary) error {
	if summary.RoomID == "" {
		return errors.New("store: room summary room_id must not be empty")
	}
	_, err := s.writeDB.ExecContext(
		ctx,
		`INSERT INTO room_summaries (room_id, summary_text, last_summarized_seq, updated_at_ms)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(room_id) DO UPDATE SET
			summary_text = excluded.summary_text,
			last_summarized_seq = excluded.last_summarized_seq,
			updated_at_ms = excluded.updated_at_ms`,
		summary.RoomID, summary.SummaryText, summary.LastSummarizedSeq, summary.UpdatedAtMs,
	)
	if err != nil {
		return fmt.Errorf("store: upsert room summary %q: %w", summary.RoomID, err)
	}
	return nil
}

func scanTimelineRows(rows *sql.Rows) ([]TimelineMessage, error) {
	var out []TimelineMessage
	for rows.Next() {
		var m TimelineMessage
		var isBot, isAtBot, archived int
		if err := rows.Scan(
			&m.SeqID, &m.ChannelID, &m.RoomID, &m.MsgID, &m.SenderID, &m.SenderName,
			&isBot, &isAtBot, &m.TimestampMs, &m.ContentJSON, &m.RenderedText, &m.Status, &archived,
		); err != nil {
			return nil, fmt.Errorf("store: scan timeline message: %w", err)
		}
		m.IsBot = isBot != 0
		m.IsAtBot = isAtBot != 0
		m.ArchivedToMemory = archived != 0
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate timeline messages: %w", err)
	}
	return out, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func int64InClause(ids []int64) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}
