package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"omni-bot/internal/ai"
)

// Result holds both model-facing text output and optional structured payload from a tool execution.
type Result struct {
	Text       string `json:"text"`
	Structured any    `json:"structured,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

// Tool is the unified contract for built-in, sandbox, and MCP-backed tools.
type Tool interface {
	Definition() ai.ToolDef
	Execute(ctx context.Context, argsJSON []byte) (Result, error)
}

// FuncTool adapts a definition and closure into a Tool.
type FuncTool struct {
	Def ai.ToolDef
	Fn  func(ctx context.Context, argsJSON []byte) (Result, error)
}

func (f FuncTool) Definition() ai.ToolDef { return f.Def }

func (f FuncTool) Execute(ctx context.Context, argsJSON []byte) (Result, error) {
	if f.Fn == nil {
		return Result{}, errors.New("tool handler is nil")
	}
	return f.Fn(ctx, argsJSON)
}

// Registry stores named tools and provides panic-safe, JSON-validated execution.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry and returns an error on empty or duplicate names.
func (r *Registry) Register(t Tool) error {
	if t == nil {
		return errors.New("tools: cannot register nil tool")
	}
	name := strings.TrimSpace(t.Definition().Name)
	if name == "" {
		return errors.New("tools: tool name must not be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tools: duplicate tool %q", name)
	}
	r.tools[name] = t
	return nil
}

// RegisterOrReplace inserts or replaces a tool by its definition name.
func (r *Registry) RegisterOrReplace(t Tool) error {
	if t == nil {
		return errors.New("tools: cannot register nil tool")
	}
	name := strings.TrimSpace(t.Definition().Name)
	if name == "" {
		return errors.New("tools: tool name must not be empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[name] = t
	return nil
}

func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Snapshot returns a shallow copy of the registry so an active Run can freeze its tool set.
func (r *Registry) Snapshot() *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cp := make(map[string]Tool, len(r.tools))
	maps.Copy(cp, r.tools)
	return &Registry{tools: cp}
}

// ToolDefs returns all registered tool schemas sorted deterministically by name.
func (r *Registry) ToolDefs() []ai.ToolDef {
	r.mu.RLock()
	names := slices.Sorted(maps.Keys(r.tools))
	defs := make([]ai.ToolDef, 0, len(names))
	for _, name := range names {
		defs = append(defs, r.tools[name].Definition())
	}
	r.mu.RUnlock()
	return defs
}

// Execute safely invokes a registered tool, converting unknown tools, malformed JSON, panics, and errors into IsError results.
func (r *Registry) Execute(ctx context.Context, name string, argsJSON []byte) (res Result) {
	tool, ok := r.Get(name)
	if !ok {
		return Result{
			Text:    fmt.Sprintf("error: unknown tool %q", name),
			IsError: true,
		}
	}

	trimmed := bytes.TrimSpace(argsJSON)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	if !json.Valid(trimmed) {
		return Result{
			Text:    fmt.Sprintf("error: invalid JSON arguments for tool %q", name),
			IsError: true,
		}
	}

	defer func() {
		if rec := recover(); rec != nil {
			res = Result{
				Text:    fmt.Sprintf("error: tool %q panicked: %v", name, rec),
				IsError: true,
			}
		}
	}()

	out, err := tool.Execute(ctx, trimmed)
	if err != nil {
		text := strings.TrimSpace(out.Text)
		if text == "" {
			text = "error: " + err.Error()
		} else if !strings.Contains(text, err.Error()) {
			text = text + "\nerror: " + err.Error()
		}
		return Result{
			Text:       text,
			Structured: out.Structured,
			IsError:    true,
		}
	}
	return out
}

// ExecuteMap marshals a map of arguments and delegates to Execute.
func (r *Registry) ExecuteMap(ctx context.Context, name string, args map[string]any) Result {
	if args == nil {
		return r.Execute(ctx, name, []byte("{}"))
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return Result{
			Text:    fmt.Sprintf("error: marshal arguments for tool %q: %v", name, err),
			IsError: true,
		}
	}
	return r.Execute(ctx, name, raw)
}
