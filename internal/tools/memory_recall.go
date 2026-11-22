package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"omni-bot/internal/ai"
)

// MemoryRecallQuery captures the filter criteria for active memory retrieval.
type MemoryRecallQuery struct {
	Query           string `json:"query"`
	Entity          string `json:"entity,omitempty"`
	TimeFrom        string `json:"time_from,omitempty"`
	TimeTo          string `json:"time_to,omitempty"`
	IncludeArchived bool   `json:"include_archived,omitempty"`
	Limit           int    `json:"limit,omitempty"`
}

// RecalledMemory is a single memory record returned by a MemoryRecaller.
type RecalledMemory struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind,omitempty"`
	Content   string   `json:"content"`
	TimeEvent string   `json:"t_event,omitempty"`
	Entities  []string `json:"entities,omitempty"`
	Score     float64  `json:"score,omitempty"`
	Archived  bool     `json:"archived,omitempty"`
}

// MemoryRecaller is the consumer-side interface to active memory retrieval.
type MemoryRecaller interface {
	Recall(ctx context.Context, q MemoryRecallQuery) ([]RecalledMemory, error)
}

type memoryRecallTool struct {
	recaller MemoryRecaller
}

// NewMemoryRecallTool creates the built-in memory_recall tool backed by recaller.
func NewMemoryRecallTool(recaller MemoryRecaller) Tool {
	return &memoryRecallTool{recaller: recaller}
}

func (t *memoryRecallTool) Definition() ai.ToolDef {
	return ai.ToolDef{
		Name:        "memory_recall",
		Description: "Actively search long-term group memories by keyword, entity, time range, and optional archived history.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Free-text recall query describing the topic, event, or fact to remember.",
				},
				"entity": map[string]any{
					"type":        "string",
					"description": "Optional person, nickname, or entity name to filter memories.",
				},
				"time_from": map[string]any{
					"type":        "string",
					"description": "Optional lower bound timestamp or date (e.g. 2026-01-01 or RFC3339).",
				},
				"time_to": map[string]any{
					"type":        "string",
					"description": "Optional upper bound timestamp or date (e.g. 2026-12-31 or RFC3339).",
				},
				"include_archived": map[string]any{
					"type":        "boolean",
					"description": "Include cold-archived or superseded memories when true.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of memories to return (default 8, max 25).",
				},
			},
		},
	}
}

func (t *memoryRecallTool) Execute(ctx context.Context, argsJSON []byte) (Result, error) {
	if t.recaller == nil {
		return Result{}, errors.New("memory recaller is not configured")
	}
	var q MemoryRecallQuery
	if err := json.Unmarshal(argsJSON, &q); err != nil {
		return Result{}, fmt.Errorf("decode memory_recall args: %w", err)
	}
	q.Query = strings.TrimSpace(q.Query)
	q.Entity = strings.TrimSpace(q.Entity)
	q.TimeFrom = strings.TrimSpace(q.TimeFrom)
	q.TimeTo = strings.TrimSpace(q.TimeTo)
	if q.Query == "" && q.Entity == "" && q.TimeFrom == "" && q.TimeTo == "" {
		return Result{}, errors.New("memory_recall requires at least one of query, entity, time_from, or time_to")
	}
	if q.Limit <= 0 {
		q.Limit = 8
	}
	if q.Limit > 25 {
		q.Limit = 25
	}

	items, err := t.recaller.Recall(ctx, q)
	if err != nil {
		return Result{}, fmt.Errorf("memory recall failed: %w", err)
	}
	if len(items) == 0 {
		return Result{
			Text:       "No matching memories found.",
			Structured: []RecalledMemory{},
		}, nil
	}

	var sb strings.Builder
	for i, item := range items {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString("- ")
		if item.TimeEvent != "" {
			sb.WriteByte('[')
			sb.WriteString(item.TimeEvent)
			sb.WriteString("] ")
		}
		if item.Archived {
			sb.WriteString("[archived] ")
		}
		sb.WriteString(strings.TrimSpace(item.Content))
		if len(item.Entities) > 0 {
			sb.WriteString(" (entities: ")
			sb.WriteString(strings.Join(item.Entities, ", "))
			sb.WriteByte(')')
		}
	}
	return Result{
		Text:       sb.String(),
		Structured: items,
	}, nil
}
