package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"omni-bot/internal/ai"
	"omni-bot/internal/tools"
)

// --- read ---

type readTool struct {
	ws  *Workspace
	cfg Config
}

func newReadTool(ws *Workspace, cfg Config) tools.Tool {
	return &readTool{ws: ws, cfg: cfg}
}

func (t *readTool) Definition() ai.ToolDef {
	return ai.ToolDef{
		Name:        "read",
		Description: "Read a UTF-8 text file inside the sandbox workspace with optional 1-based line offset and line limit.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path inside the sandbox workspace.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "1-based start line number (default 1).",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of lines to return (default 200, max 1000).",
				},
			},
			"required": []string{"path"},
		},
	}
}

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (t *readTool) Execute(ctx context.Context, argsJSON []byte) (tools.Result, error) {
	if err := ctx.Err(); err != nil {
		return tools.Result{}, err
	}
	var args readArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return tools.Result{}, fmt.Errorf("decode read args: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return tools.Result{}, errors.New("read: path is required")
	}
	absPath, err := t.ws.Resolve(args.Path)
	if err != nil {
		return tools.Result{}, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return tools.Result{}, fmt.Errorf("read: stat %q: %w", args.Path, err)
	}
	if info.IsDir() {
		return tools.Result{}, fmt.Errorf("read: %q is a directory", args.Path)
	}
	if info.Size() > t.cfg.MaxReadBytes {
		return tools.Result{}, fmt.Errorf("read: file %q exceeds max size (%d bytes)", args.Path, t.cfg.MaxReadBytes)
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return tools.Result{}, fmt.Errorf("read: %w", err)
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return tools.Result{}, fmt.Errorf("read: %q appears to be a binary file", args.Path)
	}

	offset := max(1, args.Offset)
	limit := args.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}

	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	totalLines := len(lines)
	if offset > totalLines {
		return tools.Result{
			Text: fmt.Sprintf("File %s has %d lines; offset %d is past EOF.", t.ws.RelPath(absPath), totalLines, offset),
		}, nil
	}
	end := min(totalLines, offset-1+limit)
	var sb strings.Builder
	for i := offset - 1; i < end; i++ {
		if i > offset-1 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "%d: %s", i+1, lines[i])
	}
	if end < totalLines {
		fmt.Fprintf(&sb, "\n... [%d more lines]", totalLines-end)
	}
	return tools.Result{
		Text: sb.String(),
		Structured: map[string]any{
			"path":        t.ws.RelPath(absPath),
			"start_line":  offset,
			"end_line":    end,
			"total_lines": totalLines,
		},
	}, nil
}

// --- write ---

type writeTool struct {
	ws  *Workspace
	cfg Config
}

func newWriteTool(ws *Workspace, cfg Config) tools.Tool {
	return &writeTool{ws: ws, cfg: cfg}
}

func (t *writeTool) Definition() ai.ToolDef {
	return ai.ToolDef{
		Name:        "write",
		Description: "Write or overwrite a file inside the sandbox workspace (creating parent directories as needed).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path inside the sandbox workspace.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Full file content to write.",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t *writeTool) Execute(ctx context.Context, argsJSON []byte) (tools.Result, error) {
	if err := ctx.Err(); err != nil {
		return tools.Result{}, err
	}
	var args writeArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return tools.Result{}, fmt.Errorf("decode write args: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return tools.Result{}, errors.New("write: path is required")
	}
	if int64(len(args.Content)) > t.cfg.MaxWriteBytes {
		return tools.Result{}, fmt.Errorf("write: content exceeds max size (%d bytes)", t.cfg.MaxWriteBytes)
	}
	absPath, err := t.ws.Resolve(args.Path)
	if err != nil {
		return tools.Result{}, err
	}
	if absPath == t.ws.Root() {
		return tools.Result{}, errors.New("write: cannot overwrite sandbox root directory")
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return tools.Result{}, fmt.Errorf("write: create parent dir: %w", err)
	}
	// Re-resolve after directory creation to guarantee parent chain still resolves inside root.
	absPath, err = t.ws.Resolve(args.Path)
	if err != nil {
		return tools.Result{}, err
	}
	if err := writeAtomic(absPath, []byte(args.Content)); err != nil {
		return tools.Result{}, fmt.Errorf("write: %w", err)
	}
	rel := t.ws.RelPath(absPath)
	return tools.Result{
		Text: fmt.Sprintf("Wrote %d bytes to %s.", len(args.Content), rel),
		Structured: map[string]any{
			"path":  rel,
			"bytes": len(args.Content),
		},
	}, nil
}

// --- edit ---

type editTool struct {
	ws  *Workspace
	cfg Config
}

func newEditTool(ws *Workspace, cfg Config) tools.Tool {
	return &editTool{ws: ws, cfg: cfg}
}

func (t *editTool) Definition() ai.ToolDef {
	return ai.ToolDef{
		Name:        "edit",
		Description: "Replace an exact text snippet in a file inside the sandbox workspace.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path inside the sandbox workspace.",
				},
				"old_text": map[string]any{
					"type":        "string",
					"description": "Exact text to replace.",
				},
				"new_text": map[string]any{
					"type":        "string",
					"description": "Replacement text.",
				},
				"replace_all": map[string]any{
					"type":        "boolean",
					"description": "Replace all occurrences instead of requiring a unique match.",
				},
			},
			"required": []string{"path", "old_text", "new_text"},
		},
	}
}

type editArgs struct {
	Path       string `json:"path"`
	OldText    string `json:"old_text"`
	NewText    string `json:"new_text"`
	ReplaceAll bool   `json:"replace_all"`
}

func (t *editTool) Execute(ctx context.Context, argsJSON []byte) (tools.Result, error) {
	if err := ctx.Err(); err != nil {
		return tools.Result{}, err
	}
	var args editArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return tools.Result{}, fmt.Errorf("decode edit args: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return tools.Result{}, errors.New("edit: path is required")
	}
	if args.OldText == "" {
		return tools.Result{}, errors.New("edit: old_text must not be empty")
	}
	absPath, err := t.ws.Resolve(args.Path)
	if err != nil {
		return tools.Result{}, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return tools.Result{}, fmt.Errorf("edit: stat %q: %w", args.Path, err)
	}
	if info.Size() > t.cfg.MaxReadBytes {
		return tools.Result{}, fmt.Errorf("edit: file %q exceeds max size", args.Path)
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return tools.Result{}, fmt.Errorf("edit: read %q: %w", args.Path, err)
	}
	content := string(raw)
	count := strings.Count(content, args.OldText)
	if count == 0 {
		return tools.Result{}, fmt.Errorf("edit: old_text not found in %s", t.ws.RelPath(absPath))
	}
	if count > 1 && !args.ReplaceAll {
		return tools.Result{}, fmt.Errorf("edit: old_text matched %d times in %s; provide more context or set replace_all=true", count, t.ws.RelPath(absPath))
	}

	var updated string
	if args.ReplaceAll {
		updated = strings.ReplaceAll(content, args.OldText, args.NewText)
	} else {
		updated = strings.Replace(content, args.OldText, args.NewText, 1)
		count = 1
	}
	if int64(len(updated)) > t.cfg.MaxWriteBytes {
		return tools.Result{}, fmt.Errorf("edit: resulting file exceeds max size (%d bytes)", t.cfg.MaxWriteBytes)
	}
	if err := writeAtomic(absPath, []byte(updated)); err != nil {
		return tools.Result{}, fmt.Errorf("edit: write %q: %w", args.Path, err)
	}
	rel := t.ws.RelPath(absPath)
	return tools.Result{
		Text: fmt.Sprintf("Edited %s (%d replacement(s)).", rel, count),
		Structured: map[string]any{
			"path":         rel,
			"replacements": count,
		},
	}, nil
}

// --- find ---

type findTool struct {
	ws *Workspace
}

func newFindTool(ws *Workspace) tools.Tool {
	return &findTool{ws: ws}
}

func (t *findTool) Definition() ai.ToolDef {
	return ai.ToolDef{
		Name:        "find",
		Description: "List files inside the sandbox workspace matching an optional glob pattern.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Optional subdirectory relative to sandbox root (default '.').",
				},
				"pattern": map[string]any{
					"type":        "string",
					"description": "Optional glob pattern matched against file name or relative path (default '*').",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum number of paths to return (default 100, max 500).",
				},
			},
		},
	}
}

type findArgs struct {
	Path       string `json:"path"`
	Pattern    string `json:"pattern"`
	MaxResults int    `json:"max_results"`
}

func (t *findTool) Execute(ctx context.Context, argsJSON []byte) (tools.Result, error) {
	var args findArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return tools.Result{}, fmt.Errorf("decode find args: %w", err)
	}
	startDir, err := t.ws.Resolve(args.Path)
	if err != nil {
		return tools.Result{}, err
	}
	pattern := strings.TrimSpace(args.Pattern)
	if pattern == "" {
		pattern = "*"
	}
	maxResults := args.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}
	if maxResults > 500 {
		maxResults = 500
	}

	var matches []string
	truncated := false
	err = filepath.WalkDir(startDir, func(p string, d fs.DirEntry, werr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if werr != nil {
			return nil
		}
		if p == startDir {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if _, rerr := t.ws.Resolve(p); rerr != nil {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			if d.Name() == logSubdirName && filepath.Dir(p) == t.ws.Root() && args.Path == "" {
				return filepath.SkipDir
			}
			return nil
		}
		rel := t.ws.RelPath(p)
		if matchGlob(pattern, d.Name(), rel) {
			if len(matches) >= maxResults {
				truncated = true
				return fs.SkipAll
			}
			matches = append(matches, rel)
		}
		return nil
	})
	if err != nil {
		return tools.Result{}, fmt.Errorf("find: %w", err)
	}
	slices.Sort(matches)

	if len(matches) == 0 {
		return tools.Result{
			Text:       "No matching files found.",
			Structured: []string{},
		}, nil
	}
	text := strings.Join(matches, "\n")
	if truncated {
		text += fmt.Sprintf("\n... [truncated at %d results]", maxResults)
	}
	return tools.Result{
		Text:       text,
		Structured: matches,
	}, nil
}

// --- grep ---

type grepTool struct {
	ws  *Workspace
	cfg Config
}

func newGrepTool(ws *Workspace, cfg Config) tools.Tool {
	return &grepTool{ws: ws, cfg: cfg}
}

func (t *grepTool) Definition() ai.ToolDef {
	return ai.ToolDef{
		Name:        "grep",
		Description: "Search text files inside the sandbox workspace by regex or literal substring.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regular expression (or literal string when literal=true) to search for.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Optional file or directory relative to sandbox root (default '.').",
				},
				"glob": map[string]any{
					"type":        "string",
					"description": "Optional filename glob filter (e.g. '*.go').",
				},
				"literal": map[string]any{
					"type":        "boolean",
					"description": "Treat pattern as a literal substring instead of a regex.",
				},
				"case_insensitive": map[string]any{
					"type":        "boolean",
					"description": "Case-insensitive matching when true.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum matching lines to return (default 100, max 500).",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

type grepArgs struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Glob            string `json:"glob"`
	Literal         bool   `json:"literal"`
	CaseInsensitive bool   `json:"case_insensitive"`
	MaxResults      int    `json:"max_results"`
}

// GrepMatch represents one matching line returned by grep.
type GrepMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func (t *grepTool) Execute(ctx context.Context, argsJSON []byte) (tools.Result, error) {
	var args grepArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return tools.Result{}, fmt.Errorf("decode grep args: %w", err)
	}
	if args.Pattern == "" {
		return tools.Result{}, errors.New("grep: pattern is required")
	}
	expr := args.Pattern
	if args.Literal {
		expr = regexp.QuoteMeta(expr)
	}
	if args.CaseInsensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return tools.Result{}, fmt.Errorf("grep: invalid pattern: %w", err)
	}

	target, err := t.ws.Resolve(args.Path)
	if err != nil {
		return tools.Result{}, err
	}
	maxResults := args.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}
	if maxResults > 500 {
		maxResults = 500
	}

	var matches []GrepMatch
	truncated := false
	scanFile := func(p string) error {
		info, err := os.Stat(p)
		if err != nil || info.IsDir() || info.Size() > t.cfg.MaxReadBytes {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil || bytes.IndexByte(raw, 0) >= 0 {
			return nil
		}
		rel := t.ws.RelPath(p)
		lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
		for idx, line := range lines {
			if re.MatchString(line) {
				if len(matches) >= maxResults {
					truncated = true
					return fs.SkipAll
				}
				matches = append(matches, GrepMatch{
					Path: rel,
					Line: idx + 1,
					Text: line,
				})
			}
		}
		return nil
	}

	info, err := os.Stat(target)
	if err != nil {
		return tools.Result{}, fmt.Errorf("grep: stat %q: %w", args.Path, err)
	}
	if !info.IsDir() {
		if err := scanFile(target); err != nil && !errors.Is(err, fs.SkipAll) {
			return tools.Result{}, err
		}
	} else {
		err = filepath.WalkDir(target, func(p string, d fs.DirEntry, werr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if werr != nil {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				if _, rerr := t.ws.Resolve(p); rerr != nil {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			if d.IsDir() {
				if d.Name() == logSubdirName && filepath.Dir(p) == t.ws.Root() && args.Path == "" {
					return filepath.SkipDir
				}
				return nil
			}
			if args.Glob != "" && !matchGlob(args.Glob, d.Name(), t.ws.RelPath(p)) {
				return nil
			}
			return scanFile(p)
		})
		if err != nil && !errors.Is(err, fs.SkipAll) {
			return tools.Result{}, fmt.Errorf("grep: %w", err)
		}
	}

	if len(matches) == 0 {
		return tools.Result{
			Text:       "No matches found.",
			Structured: []GrepMatch{},
		}, nil
	}
	var sb strings.Builder
	for i, m := range matches {
		if i > 0 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "%s:%d: %s", m.Path, m.Line, m.Text)
	}
	if truncated {
		fmt.Fprintf(&sb, "\n... [truncated at %d matches]", maxResults)
	}
	return tools.Result{
		Text:       sb.String(),
		Structured: matches,
	}, nil
}

func matchGlob(pattern, baseName, relPath string) bool {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" || pattern == "*" {
		return true
	}
	if ok, _ := path.Match(pattern, baseName); ok {
		return true
	}
	if ok, _ := path.Match(pattern, relPath); ok {
		return true
	}
	return false
}

func writeAtomic(dstPath string, data []byte) error {
	dir := filepath.Dir(dstPath)
	tmp, err := os.CreateTemp(dir, ".tmp-sandbox-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}
