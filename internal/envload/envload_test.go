package envload

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr string
	}{
		{
			name: "comments, export, quotes, inline comments, CRLF and BOM",
			input: "\uFEFF# leading comment\r\n" +
				"\r\n" +
				"PLAIN=hello\r\n" +
				"export EXPORTED=world\r\n" +
				"EXPORT_PREFIX_KEY=kept\r\n" +
				"WITH_INLINE = value # trailing comment\r\n" +
				"URL=https://example.com/path#frag\r\n" +
				"SINGLE='keep # hash and \\n literal' # comment\r\n" +
				"DOUBLE=\"line1\\nline2\\t\\\"quoted\\\"\\\\\" # comment\r\n" +
				"EMPTY=\r\n" +
				"EMPTY_QUOTED=\"\"\r\n" +
				"DUP=first\r\n" +
				"DUP=second\r\n",
			want: map[string]string{
				"PLAIN":             "hello",
				"EXPORTED":          "world",
				"EXPORT_PREFIX_KEY": "kept",
				"WITH_INLINE":       "value",
				"URL":               "https://example.com/path#frag",
				"SINGLE":            `keep # hash and \n literal`,
				"DOUBLE":            "line1\nline2\t\"quoted\"\\",
				"EMPTY":             "",
				"EMPTY_QUOTED":      "",
				"DUP":               "second",
			},
		},
		{
			name:    "missing equals",
			input:   "NOT_AN_ASSIGNMENT\n",
			wantErr: "line 1",
		},
		{
			name:    "invalid key name",
			input:   "123BAD=val\n",
			wantErr: "invalid key",
		},
		{
			name:    "unterminated double quote",
			input:   "KEY=\"unterminated\n",
			wantErr: "unterminated quote",
		},
		{
			name:    "unterminated single quote",
			input:   "KEY='unterminated\n",
			wantErr: "unterminated quote",
		},
		{
			name:    "trailing garbage after closing quote",
			input:   "KEY=\"ok\" extra\n",
			wantErr: "unexpected trailing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(strings.NewReader(tc.input))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (values: %v)", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !maps.Equal(got, tc.want) {
				t.Fatalf("Parse mismatch:\n got: %#v\nwant: %#v", got, tc.want)
			}
		})
	}
}

func TestLoadFile_DoesNotOverrideExistingEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env.local")
	content := "ENVLOAD_TEST_PRESET=from_file\nENVLOAD_TEST_EMPTY_PRESET=from_file\nENVLOAD_TEST_NEW=loaded_value\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ENVLOAD_TEST_PRESET", "already_set")
	t.Setenv("ENVLOAD_TEST_EMPTY_PRESET", "")
	// Ensure ENVLOAD_TEST_NEW is absent before loading and restored after test.
	t.Setenv("ENVLOAD_TEST_NEW", "")
	if err := os.Unsetenv("ENVLOAD_TEST_NEW"); err != nil {
		t.Fatal(err)
	}

	if err := LoadFile(envPath); err != nil {
		t.Fatalf("LoadFile error: %v", err)
	}

	if got := os.Getenv("ENVLOAD_TEST_PRESET"); got != "already_set" {
		t.Errorf("existing env var was overridden: got %q, want %q", got, "already_set")
	}
	if got, ok := os.LookupEnv("ENVLOAD_TEST_EMPTY_PRESET"); !ok || got != "" {
		t.Errorf("existing empty env var was overridden: got (%q, %v), want (\"\", true)", got, ok)
	}
	if got := os.Getenv("ENVLOAD_TEST_NEW"); got != "loaded_value" {
		t.Errorf("new env var not loaded: got %q, want %q", got, "loaded_value")
	}
}

func TestLoadFile_MissingReturnsErrNotExist(t *testing.T) {
	err := LoadFile(filepath.Join(t.TempDir(), "does-not-exist.env"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got: %v", err)
	}
}

func TestLoadIfExists_Precedence(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, ".env.local")
	second := filepath.Join(dir, ".env")
	missing := filepath.Join(dir, ".env.missing")

	if err := os.WriteFile(first, []byte("ENVLOAD_PREC_A=from_local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("ENVLOAD_PREC_A=from_default\nENVLOAD_PREC_B=from_default\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, k := range []string{"ENVLOAD_PREC_A", "ENVLOAD_PREC_B"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}

	if err := LoadIfExists(missing, first, second); err != nil {
		t.Fatalf("LoadIfExists error: %v", err)
	}
	if got := os.Getenv("ENVLOAD_PREC_A"); got != "from_local" {
		t.Errorf("ENVLOAD_PREC_A = %q, want from_local", got)
	}
	if got := os.Getenv("ENVLOAD_PREC_B"); got != "from_default" {
		t.Errorf("ENVLOAD_PREC_B = %q, want from_default", got)
	}
}

func TestLoadRepoEnv(t *testing.T) {
	repo := t.TempDir()
	nested := filepath.Join(repo, "internal", "memory")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantEnv := filepath.Join(repo, ".env.local")
	if err := os.WriteFile(wantEnv, []byte("ENVLOAD_REPO_KEY=found_at_root\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ENVLOAD_REPO_KEY", "")
	if err := os.Unsetenv("ENVLOAD_REPO_KEY"); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadRepoEnv(nested)
	if err != nil {
		t.Fatalf("LoadRepoEnv error: %v", err)
	}
	if loaded != wantEnv {
		t.Fatalf("loaded path = %q, want %q", loaded, wantEnv)
	}
	if got := os.Getenv("ENVLOAD_REPO_KEY"); got != "found_at_root" {
		t.Fatalf("ENVLOAD_REPO_KEY = %q, want found_at_root", got)
	}

	// Stops at go.mod boundary and returns ("", nil) when no .env.local exists inside repo.
	outer := t.TempDir()
	innerRepo := filepath.Join(outer, "subrepo")
	innerPkg := filepath.Join(innerRepo, "pkg")
	if err := os.MkdirAll(innerPkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outer, ".env.local"), []byte("ENVLOAD_OUTER=nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(innerRepo, "go.mod"), []byte("module inner\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err = LoadRepoEnv(innerPkg)
	if err != nil {
		t.Fatalf("LoadRepoEnv inner error: %v", err)
	}
	if loaded != "" {
		t.Fatalf("expected no .env.local loaded past go.mod boundary, got %q", loaded)
	}
}
