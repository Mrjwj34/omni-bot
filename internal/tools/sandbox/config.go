package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"omni-bot/internal/tools"
)

const (
	defaultTimeout     = 30 * time.Second
	defaultMaxTimeout  = 120 * time.Second
	defaultMaxLines    = 200
	defaultMaxBytes    = 16 * 1024
	defaultMaxReadSize = 1 << 20
	defaultMaxWrite    = 4 << 20
	logSubdirName      = ".omni-logs"
)

// Config controls the default-OFF sandboxed bash and file toolset.
type Config struct {
	Enabled        bool
	SandboxDir     string
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
	MaxOutputLines int
	MaxOutputBytes int
	MaxReadBytes   int64
	MaxWriteBytes  int64
	Shell          string
}

func (c Config) withDefaults() Config {
	if c.DefaultTimeout <= 0 {
		c.DefaultTimeout = defaultTimeout
	}
	if c.MaxTimeout <= 0 {
		c.MaxTimeout = defaultMaxTimeout
	}
	if c.MaxTimeout < c.DefaultTimeout {
		c.MaxTimeout = c.DefaultTimeout
	}
	if c.MaxOutputLines <= 0 {
		c.MaxOutputLines = defaultMaxLines
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = defaultMaxBytes
	}
	if c.MaxReadBytes <= 0 {
		c.MaxReadBytes = defaultMaxReadSize
	}
	if c.MaxWriteBytes <= 0 {
		c.MaxWriteBytes = defaultMaxWrite
	}
	return c
}

// RegisterIfEnabled registers the 6 sandboxed tools (bash, read, write, edit, find, grep) ONLY when cfg.Enabled is true.
func RegisterIfEnabled(reg *tools.Registry, cfg Config) (bool, error) {
	if !cfg.Enabled {
		return false, nil
	}
	if reg == nil {
		return false, errors.New("sandbox: registry is nil")
	}
	cfg = cfg.withDefaults()
	ws, err := NewWorkspace(cfg.SandboxDir)
	if err != nil {
		return false, err
	}

	toolset := []tools.Tool{
		newBashTool(ws, cfg),
		newReadTool(ws, cfg),
		newWriteTool(ws, cfg),
		newEditTool(ws, cfg),
		newFindTool(ws),
		newGrepTool(ws, cfg),
	}
	for _, t := range toolset {
		if err := reg.Register(t); err != nil {
			return false, fmt.Errorf("sandbox: register %q: %w", t.Definition().Name, err)
		}
	}
	return true, nil
}

// Workspace enforces strict path confinement inside a canonical sandbox root directory.
type Workspace struct {
	root string
}

var ErrPathEscape = errors.New("sandbox: path escapes workspace root")

func NewWorkspace(dir string) (*Workspace, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("sandbox: SandboxDir is required when sandbox tools are enabled")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolve root %q: %w", dir, err)
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox: create root %q: %w", absDir, err)
	}
	realRoot, err := filepath.EvalSymlinks(absDir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: eval root symlinks %q: %w", absDir, err)
	}
	return &Workspace{root: realRoot}, nil
}

func (w *Workspace) Root() string { return w.root }

// Resolve resolves rawPath within the workspace and rejects any traversal or symlink escape outside Root().
func (w *Workspace) Resolve(rawPath string) (string, error) {
	if strings.ContainsRune(rawPath, '\x00') {
		return "", fmt.Errorf("%w: null byte in path", ErrPathEscape)
	}
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" || trimmed == "." {
		return w.root, nil
	}

	// Reject Windows alternate data stream syntax (colon outside a leading drive letter).
	vol := filepath.VolumeName(trimmed)
	rest := strings.TrimPrefix(trimmed, vol)
	if strings.ContainsRune(rest, ':') {
		return "", fmt.Errorf("%w: invalid colon in path %q", ErrPathEscape, rawPath)
	}

	var candidate string
	if filepath.IsAbs(trimmed) {
		candidate = filepath.Clean(trimmed)
	} else {
		candidate = filepath.Join(w.root, trimmed)
	}
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("sandbox: resolve %q: %w", rawPath, err)
	}
	if !isWithinRoot(w.root, absCandidate) {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, rawPath)
	}

	// Walk upward to the deepest existing path segment and verify symlink resolution stays inside root.
	resolved, err := w.resolveThroughExistingParent(absCandidate)
	if err != nil {
		return "", err
	}
	if !isWithinRoot(w.root, resolved) {
		return "", fmt.Errorf("%w: symlink target escapes root for %q", ErrPathEscape, rawPath)
	}
	return resolved, nil
}

func (w *Workspace) RelPath(absPath string) string {
	rel, err := filepath.Rel(w.root, absPath)
	if err != nil {
		return filepath.ToSlash(absPath)
	}
	return filepath.ToSlash(rel)
}

func (w *Workspace) resolveThroughExistingParent(absTarget string) (string, error) {
	cur := absTarget
	var tail []string
	for {
		info, err := os.Lstat(cur)
		if err == nil {
			// If the existing node itself is a symlink (possibly dangling), evaluate it explicitly.
			if info.Mode()&os.ModeSymlink != 0 {
				linkResolved, lerr := filepath.EvalSymlinks(cur)
				if lerr != nil {
					return "", fmt.Errorf("%w: unresolved symlink %q", ErrPathEscape, cur)
				}
				cur = linkResolved
			} else {
				realCur, rerr := filepath.EvalSymlinks(cur)
				if rerr != nil {
					return "", fmt.Errorf("sandbox: eval symlinks %q: %w", cur, rerr)
				}
				cur = realCur
			}
			if !isWithinRoot(w.root, cur) {
				return "", fmt.Errorf("%w: %q escapes root", ErrPathEscape, absTarget)
			}
			for i := len(tail) - 1; i >= 0; i-- {
				cur = filepath.Join(cur, tail[i])
			}
			return cur, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("sandbox: stat %q: %w", cur, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("%w: %q", ErrPathEscape, absTarget)
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

func isWithinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	return true
}
