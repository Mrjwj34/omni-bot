package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"omni-bot/internal/tools"
)

func TestRegisterIfEnabledDefaultOff(t *testing.T) {
	reg := tools.NewRegistry()
	dir := t.TempDir()

	registered, err := RegisterIfEnabled(reg, Config{SandboxDir: dir})
	if err != nil {
		t.Fatalf("RegisterIfEnabled(default off): %v", err)
	}
	if registered {
		t.Fatalf("expected default-OFF sandbox not to register")
	}
	if defs := reg.ToolDefs(); len(defs) != 0 {
		t.Fatalf("expected 0 tools registered when disabled, got %d", len(defs))
	}

	registered, err = RegisterIfEnabled(reg, Config{Enabled: true, SandboxDir: dir})
	if err != nil {
		t.Fatalf("RegisterIfEnabled(enabled): %v", err)
	}
	if !registered {
		t.Fatalf("expected enabled sandbox to register")
	}
	defs := reg.ToolDefs()
	wantNames := []string{"bash", "edit", "find", "grep", "read", "write"}
	if len(defs) != len(wantNames) {
		t.Fatalf("expected %d tools, got %d", len(wantNames), len(defs))
	}
	for i, want := range wantNames {
		if defs[i].Name != want {
			t.Fatalf("defs[%d] = %q, want %q", i, defs[i].Name, want)
		}
	}
}

func TestPathConfinementAndTraversalRejection(t *testing.T) {
	parent := t.TempDir()
	sandboxDir := filepath.Join(parent, "workspace")
	outsideFile := filepath.Join(parent, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("TOP_SECRET_OUTSIDE"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	reg := tools.NewRegistry()
	if _, err := RegisterIfEnabled(reg, Config{Enabled: true, SandboxDir: sandboxDir}); err != nil {
		t.Fatalf("register sandbox: %v", err)
	}

	escapePaths := []string{
		"../secret.txt",
		"sub/../../secret.txt",
		outsideFile,
	}

	// Also verify symlink escape when the OS permits creating symlinks.
	symlinkPath := filepath.Join(sandboxDir, "escape-link.txt")
	if err := os.Symlink(outsideFile, symlinkPath); err == nil {
		escapePaths = append(escapePaths, "escape-link.txt")
	}

	for _, badPath := range escapePaths {
		if res := reg.ExecuteMap(t.Context(), "read", map[string]any{"path": badPath}); !res.IsError {
			t.Fatalf("read should reject %q, got: %s", badPath, res.Text)
		}
		if res := reg.ExecuteMap(t.Context(), "write", map[string]any{"path": badPath, "content": "pwn"}); !res.IsError {
			t.Fatalf("write should reject %q, got: %s", badPath, res.Text)
		}
		if res := reg.ExecuteMap(t.Context(), "edit", map[string]any{"path": badPath, "old_text": "TOP", "new_text": "X"}); !res.IsError {
			t.Fatalf("edit should reject %q, got: %s", badPath, res.Text)
		}
		if res := reg.ExecuteMap(t.Context(), "find", map[string]any{"path": filepath.Dir(badPath)}); !res.IsError && badPath != "escape-link.txt" {
			t.Fatalf("find should reject parent of %q, got: %s", badPath, res.Text)
		}
		if res := reg.ExecuteMap(t.Context(), "grep", map[string]any{"pattern": "SECRET", "path": badPath}); !res.IsError {
			t.Fatalf("grep should reject %q, got: %s", badPath, res.Text)
		}
	}

	if res := reg.ExecuteMap(t.Context(), "bash", map[string]any{
		"command": "echo hi",
		"workdir": "..",
	}); !res.IsError {
		t.Fatalf("bash should reject workdir escaping sandbox, got: %s", res.Text)
	}
}

func TestFileToolsetRoundTrip(t *testing.T) {
	reg := tools.NewRegistry()
	if _, err := RegisterIfEnabled(reg, Config{Enabled: true, SandboxDir: t.TempDir()}); err != nil {
		t.Fatalf("register sandbox: %v", err)
	}

	// write nested file
	wRes := reg.ExecuteMap(t.Context(), "write", map[string]any{
		"path":    "notes/todo.txt",
		"content": "alpha\nbeta target\ngamma\n",
	})
	if wRes.IsError {
		t.Fatalf("write failed: %s", wRes.Text)
	}

	// read slice
	rRes := reg.ExecuteMap(t.Context(), "read", map[string]any{
		"path":   "notes/todo.txt",
		"offset": 2,
		"limit":  2,
	})
	if rRes.IsError || !strings.Contains(rRes.Text, "2: beta target") {
		t.Fatalf("read failed: err=%v text=%q", rRes.IsError, rRes.Text)
	}

	// edit unique match
	eRes := reg.ExecuteMap(t.Context(), "edit", map[string]any{
		"path":     "notes/todo.txt",
		"old_text": "beta target",
		"new_text": "beta UPDATED",
	})
	if eRes.IsError {
		t.Fatalf("edit failed: %s", eRes.Text)
	}

	// find
	fRes := reg.ExecuteMap(t.Context(), "find", map[string]any{
		"pattern": "*.txt",
	})
	if fRes.IsError || !strings.Contains(fRes.Text, "notes/todo.txt") {
		t.Fatalf("find failed: err=%v text=%q", fRes.IsError, fRes.Text)
	}

	// grep
	gRes := reg.ExecuteMap(t.Context(), "grep", map[string]any{
		"pattern": "UPDATED",
	})
	if gRes.IsError || !strings.Contains(gRes.Text, "notes/todo.txt:2: beta UPDATED") {
		t.Fatalf("grep failed: err=%v text=%q", gRes.IsError, gRes.Text)
	}
}

func TestBashStripsSensitiveEnvVars(t *testing.T) {
	secrets := map[string]string{
		"GEMINI_API_KEY":    "gm-super-secret-value-111",
		"OPENAI_API_KEY":    "sk-openai-secret-value-222",
		"DEEPSEEK_API_KEY":  "ds-secret-value-333",
		"TTS_TOKEN":         "fish-tts-secret-444",
		"CUSTOM_SECRET":     "custom-secret-555",
		"BOT_INTERNAL_KEY":  "internal-key-666",
		"UNLISTED_NON_SAFE": "unlisted-var-777",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}

	reg := tools.NewRegistry()
	if _, err := RegisterIfEnabled(reg, Config{Enabled: true, SandboxDir: t.TempDir()}); err != nil {
		t.Fatalf("register sandbox: %v", err)
	}

	cmd := "env"
	if runtime.GOOS == "windows" {
		cmd = "Get-ChildItem Env: | ForEach-Object { \"$($_.Name)=$($_.Value)\" }"
	}
	res := reg.ExecuteMap(t.Context(), "bash", map[string]any{
		"command": cmd,
	})
	if res.IsError {
		t.Fatalf("bash env dump failed: %s", res.Text)
	}
	for k, v := range secrets {
		if strings.Contains(res.Text, k) || strings.Contains(res.Text, v) {
			t.Fatalf("secret env %s=%s leaked into sandbox bash output:\n%s", k, v, res.Text)
		}
	}
}

func TestBashHeadTailTruncationAndSpillLog(t *testing.T) {
	sandboxDir := t.TempDir()
	reg := tools.NewRegistry()
	if _, err := RegisterIfEnabled(reg, Config{
		Enabled:        true,
		SandboxDir:     sandboxDir,
		MaxOutputLines: 10,
		MaxOutputBytes: 400,
	}); err != nil {
		t.Fatalf("register sandbox: %v", err)
	}

	cmd := `i=1; while [ "$i" -le 120 ]; do printf "LINE-%03d\n" "$i"; i=$((i+1)); done`
	if runtime.GOOS == "windows" {
		cmd = `1..120 | ForEach-Object { "LINE-{0:D3}" -f $_ }`
	}
	res := reg.ExecuteMap(t.Context(), "bash", map[string]any{
		"command": cmd,
	})
	if res.IsError {
		t.Fatalf("bash truncation command failed: %s", res.Text)
	}
	if !strings.Contains(res.Text, "LINE-001") || !strings.Contains(res.Text, "LINE-120") {
		t.Fatalf("expected both head LINE-001 and tail LINE-120 in output:\n%s", res.Text)
	}
	if strings.Contains(res.Text, "LINE-060") {
		t.Fatalf("expected middle line LINE-060 to be omitted from truncated output:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, ".omni-logs/") {
		t.Fatalf("expected spill log path in truncation notice:\n%s", res.Text)
	}

	out, ok := res.Structured.(BashOutput)
	if !ok || !out.Truncated || out.LogPath == "" {
		t.Fatalf("unexpected structured BashOutput: %#v", res.Structured)
	}
	fullBytes, err := os.ReadFile(filepath.Join(sandboxDir, filepath.FromSlash(out.LogPath)))
	if err != nil {
		t.Fatalf("read spill log %q: %v", out.LogPath, err)
	}
	fullText := string(fullBytes)
	if !strings.Contains(fullText, "LINE-001") || !strings.Contains(fullText, "LINE-060") || !strings.Contains(fullText, "LINE-120") {
		t.Fatalf("spill log missing expected lines")
	}
}

func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	reg := tools.NewRegistry()
	if _, err := RegisterIfEnabled(reg, Config{
		Enabled:        true,
		SandboxDir:     t.TempDir(),
		DefaultTimeout: 5 * time.Second,
		MaxTimeout:     10 * time.Second,
	}); err != nil {
		t.Fatalf("register sandbox: %v", err)
	}

	cmd := "sleep 10"
	if runtime.GOOS == "windows" {
		cmd = "Start-Sleep -Seconds 10"
	}
	start := time.Now()
	res := reg.ExecuteMap(t.Context(), "bash", map[string]any{
		"command":         cmd,
		"timeout_seconds": 0.4,
	})
	elapsed := time.Since(start)
	if !res.IsError {
		t.Fatalf("expected timeout error result, got ok: %s", res.Text)
	}
	out, ok := res.Structured.(BashOutput)
	if !ok || !out.TimedOut {
		t.Fatalf("expected TimedOut=true, got %#v (text=%q)", res.Structured, res.Text)
	}
	if elapsed > 6*time.Second {
		t.Fatalf("timeout took too long (%s), process group may not have been killed promptly", elapsed)
	}
}
