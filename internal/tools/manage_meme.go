package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"omni-bot/internal/ai"
	"omni-bot/internal/meme"
)

type manageMemeTool struct {
	vault *meme.Vault
}

// NewManageMemeTool creates the built-in manage_meme tool backed by a meme.Vault.
func NewManageMemeTool(vault *meme.Vault) Tool {
	return &manageMemeTool{vault: vault}
}

func (t *manageMemeTool) Definition() ai.ToolDef {
	return ai.ToolDef{
		Name:        "manage_meme",
		Description: "Search collected memes, refine a meme's impression tags, or delete and blacklist a meme.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"search", "update_impression", "delete"},
					"description": "Operation to perform on the meme vault.",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Keyword or emotion query for action=search.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum results for action=search (default 8, max 20).",
				},
				"hash": map[string]any{
					"type":        "string",
					"description": "Meme full hash or #prefix (e.g. #a1b2c3) for update_impression or delete.",
				},
				"overlay_text": map[string]any{
					"type":        "string",
					"description": "Updated caption/overlay text for update_impression.",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Updated visual description for update_impression.",
				},
				"emotions": map[string]any{
					"type":        "string",
					"description": "Updated emotion tags for update_impression.",
				},
				"scenes": map[string]any{
					"type":        "string",
					"description": "Updated usage scenes for update_impression.",
				},
			},
			"required": []string{"action"},
		},
	}
}

type manageMemeArgs struct {
	Action      string  `json:"action"`
	Query       string  `json:"query"`
	Limit       int     `json:"limit"`
	Hash        string  `json:"hash"`
	OverlayText *string `json:"overlay_text"`
	Description *string `json:"description"`
	Emotions    *string `json:"emotions"`
	Scenes      *string `json:"scenes"`
}

func (t *manageMemeTool) Execute(ctx context.Context, argsJSON []byte) (Result, error) {
	if t.vault == nil {
		return Result{}, errors.New("manage_meme: meme vault is not configured")
	}
	var args manageMemeArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return Result{}, fmt.Errorf("decode manage_meme args: %w", err)
	}

	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "search":
		limit := args.Limit
		if limit <= 0 {
			limit = 8
		}
		if limit > 20 {
			limit = 20
		}
		items, err := t.vault.Search(ctx, args.Query, limit)
		if err != nil {
			return Result{}, fmt.Errorf("manage_meme search: %w", err)
		}
		if len(items) == 0 {
			return Result{
				Text:       "No matching memes found.",
				Structured: []meme.HandItem{},
			}, nil
		}
		return Result{
			Text:       meme.FormatAvailableMemes(items),
			Structured: items,
		}, nil

	case "update_impression":
		if strings.TrimSpace(args.Hash) == "" {
			return Result{}, errors.New("manage_meme: hash is required for update_impression")
		}
		if args.OverlayText == nil && args.Description == nil && args.Emotions == nil && args.Scenes == nil {
			return Result{}, errors.New("manage_meme: at least one of overlay_text, description, emotions, or scenes is required")
		}
		updated, err := t.vault.UpdateImpression(ctx, args.Hash, meme.ImpressionPatch{
			OverlayText: args.OverlayText,
			Description: args.Description,
			Emotions:    args.Emotions,
			Scenes:      args.Scenes,
		})
		if err != nil {
			return Result{}, fmt.Errorf("manage_meme update_impression: %w", err)
		}
		return Result{
			Text:       fmt.Sprintf("Updated meme #%s: %s", meme.ShortHash(updated.Hash), updated.Description),
			Structured: updated,
		}, nil

	case "delete":
		if strings.TrimSpace(args.Hash) == "" {
			return Result{}, errors.New("manage_meme: hash is required for delete")
		}
		deleted, err := t.vault.Delete(ctx, args.Hash)
		if err != nil {
			return Result{}, fmt.Errorf("manage_meme delete: %w", err)
		}
		return Result{
			Text:       fmt.Sprintf("Deleted and blacklisted meme #%s.", meme.ShortHash(deleted.Hash)),
			Structured: deleted,
		}, nil

	default:
		return Result{}, fmt.Errorf("manage_meme: unsupported action %q", args.Action)
	}
}
