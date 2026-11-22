package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"omni-bot/internal/ai"
	"omni-bot/internal/tools"
)

const maxCapturedOutputBytes = 16 << 20

type bashTool struct {
	ws  *Workspace
	cfg Config
}

func newBashTool(ws *Workspace, cfg Config) tools.Tool {
	return &bashTool{ws: ws, cfg: cfg}
}

func (t *bashTool) Definition() ai.ToolDef {
	return ai.ToolDef{
		Name:        "bash",
		Description: "Run a shell command inside the isolated sandbox workspace with sanitized environment, timeout, and output truncation.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "Shell command to execute inside the sandbox workspace.",
				},
				"workdir": map[string]any{
					"type":        "string",
					"description": "Optional working directory relative to the sandbox root.",
				},
				"timeout_seconds": map[string]any{
					"type":        "number",
					"description": "Optional timeout in seconds.",
				},
			},
			"required": []string{"command"},
		},
	}
}

type bashArgs struct {
	Command        string  `json:"command"`
	Workdir        string  `json:"workdir"`
	TimeoutSeconds float64 `json:"timeout_seconds"`
}

// BashOutput is the structured payload returned by the sandbox bash tool.
type BashOutput struct {
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	LogPath    string `json:"log_path,omitempty"`
	Output     string `json:"output"`
	DurationMs int64  `json:"duration_ms"`
}

func (t *bashTool) Execute(ctx context.Context, argsJSON []byte) (tools.Result, error) {
	var args bashArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return tools.Result{}, fmt.Errorf("decode bash args: %w", err)
	}
	command := strings.TrimSpace(args.Command)
	if command == "" {
		return tools.Result{}, errors.New("bash: command is required")
	}

	workDir := t.ws.Root()
	if strings.TrimSpace(args.Workdir) != "" {
		resolved, err := t.ws.Resolve(args.Workdir)
		if err != nil {
			return tools.Result{}, err
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return tools.Result{}, fmt.Errorf("bash: stat workdir: %w", err)
		}
		if !info.IsDir() {
			return tools.Result{}, fmt.Errorf("bash: workdir %q is not a directory", args.Workdir)
		}
		workDir = resolved
	}

	timeout := t.cfg.DefaultTimeout
	if args.TimeoutSeconds > 0 {
		reqTimeout := time.Duration(args.TimeoutSeconds * float64(time.Second))
		timeout = min(reqTimeout, t.cfg.MaxTimeout)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shellBin, shellArgs := resolveShell(t.cfg.Shell, command)
	cmd := exec.CommandContext(runCtx, shellBin, shellArgs...)
	cmd.Dir = workDir
	cmd.Env = SanitizeEnv(os.Environ(), t.ws.Root())
	configureProcessGroup(cmd)

	outBuf := &cappedBuffer{limit: maxCapturedOutputBytes}
	cmd.Stdout = outBuf
	cmd.Stderr = outBuf

	start := time.Now()
	runErr := cmd.Run()
	elapsedMs := time.Since(start).Milliseconds()

	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	rawOut := outBuf.String()
	displayOut, truncated := truncateHeadTail(rawOut, t.cfg.MaxOutputLines, t.cfg.MaxOutputBytes)
	var logRelPath string
	if truncated {
		if rel, err := spillLog(t.ws.Root(), rawOut); err == nil {
			logRelPath = rel
			displayOut = strings.Replace(displayOut, "{{LOG_PATH}}", rel, 1)
		} else {
			displayOut = strings.Replace(displayOut, "full output saved to {{LOG_PATH}}", "failed to write spill log", 1)
		}
	}

	structured := BashOutput{
		ExitCode:   exitCode,
		TimedOut:   timedOut,
		Truncated:  truncated,
		LogPath:    logRelPath,
		Output:     displayOut,
		DurationMs: elapsedMs,
	}

	var sb strings.Builder
	if timedOut {
		fmt.Fprintf(&sb, "command timed out after %s\n", timeout)
	}
	if displayOut != "" {
		sb.WriteString(displayOut)
	} else {
		sb.WriteString("(no output)")
	}
	if exitCode != 0 && !timedOut {
		fmt.Fprintf(&sb, "\n[exit code: %d]", exitCode)
	}

	res := tools.Result{
		Text:       sb.String(),
		Structured: structured,
		IsError:    timedOut || runErr != nil,
	}
	return res, nil
}

func resolveShell(configured, command string) (string, []string) {
	if configured != "" {
		base := strings.ToLower(filepath.Base(configured))
		switch base {
		case "pwsh", "pwsh.exe", "powershell", "powershell.exe":
			return configured, []string{"-NoProfile", "-NonInteractive", "-Command", command}
		case "cmd", "cmd.exe":
			return configured, []string{"/C", command}
		default:
			return configured, []string{"-c", command}
		}
	}
	if runtime.GOOS == "windows" {
		if pwsh, err := exec.LookPath("pwsh"); err == nil {
			return pwsh, []string{"-NoProfile", "-NonInteractive", "-Command", command}
		}
		return "cmd.exe", []string{"/C", command}
	}
	return "sh", []string{"-c", command}
}

type cappedBuffer struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	origLen := len(p)
	if b.buf.Len() < b.limit {
		remain := b.limit - b.buf.Len()
		if len(p) > remain {
			p = p[:remain]
			b.overflow = true
		}
		_, _ = b.buf.Write(p)
	} else {
		b.overflow = true
	}
	return origLen, nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf.String()
	if b.overflow {
		s += "\n[capture buffer reached 16MB hard limit]"
	}
	return s
}

func truncateHeadTail(raw string, maxLines, maxBytes int) (string, bool) {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) <= maxLines && len(normalized) <= maxBytes {
		return normalized, false
	}

	headLineBudget := max(1, maxLines/2)
	tailLineBudget := max(1, maxLines-headLineBudget)
	headByteBudget := max(64, maxBytes/2)
	tailByteBudget := max(64, maxBytes-headByteBudget)

	// Collect head lines within both line and byte budgets.
	var headParts []string
	headBytes := 0
	for i := 0; i < len(lines) && i < headLineBudget; i++ {
		line := lines[i]
		if headBytes+len(line)+1 > headByteBudget {
			if len(headParts) == 0 {
				headParts = append(headParts, sliceUTF8Prefix(line, headByteBudget))
				headBytes += len(headParts[0])
			}
			break
		}
		headParts = append(headParts, line)
		headBytes += len(line) + 1
	}

	// Collect tail lines within both line and byte budgets without overlapping head lines.
	var tailParts []string
	tailBytes := 0
	minTailIdx := len(headParts)
	for i := len(lines) - 1; i >= minTailIdx && len(tailParts) < tailLineBudget; i-- {
		line := lines[i]
		if tailBytes+len(line)+1 > tailByteBudget {
			if len(tailParts) == 0 {
				tailParts = append(tailParts, sliceUTF8Suffix(line, tailByteBudget))
				tailBytes += len(tailParts[0])
			}
			break
		}
		tailParts = append(tailParts, line)
		tailBytes += len(line) + 1
	}
	for l, r := 0, len(tailParts)-1; l < r; l, r = l+1, r-1 {
		tailParts[l], tailParts[r] = tailParts[r], tailParts[l]
	}

	omittedLines := max(0, len(lines)-len(headParts)-len(tailParts))
	omittedBytes := max(0, len(normalized)-headBytes-tailBytes)

	var sb strings.Builder
	sb.WriteString(strings.Join(headParts, "\n"))
	fmt.Fprintf(&sb, "\n... [output truncated: omitted %d lines / %d bytes; full output saved to {{LOG_PATH}}] ...\n", omittedLines, omittedBytes)
	sb.WriteString(strings.Join(tailParts, "\n"))
	return sb.String(), true
}

func sliceUTF8Prefix(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	out := s[:maxBytes]
	for len(out) > 0 && (out[len(out)-1]&0xC0) == 0x80 {
		out = out[:len(out)-1]
	}
	return out
}

func sliceUTF8Suffix(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && (s[start]&0xC0) == 0x80 {
		start++
	}
	return s[start:]
}

func spillLog(sandboxRoot, rawOutput string) (string, error) {
	logsDir := filepath.Join(sandboxRoot, logSubdirName)
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return "", err
	}
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	fileName := fmt.Sprintf("%d-%s.log", time.Now().UnixMilli(), hex.EncodeToString(rnd[:]))
	dst := filepath.Join(logsDir, fileName)
	if err := os.WriteFile(dst, []byte(rawOutput), 0o600); err != nil {
		return "", err
	}
	return path.Join(logSubdirName, fileName), nil
}
