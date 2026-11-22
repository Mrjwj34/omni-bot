package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
}

func TestCheckRepo(t *testing.T) {
	tests := []struct {
		name         string
		files        map[string]string
		wantContains []string
	}{
		{
			name: "valid nested tree with excluded dirs ignored",
			files: map[string]string{
				"README.md":            "# root\n\nRoot package.\n\n## 目录索引\n\n- `go.mod` — module.\n  - `subnote` should not be treated as a top-level entry.\n- [`pkg/`](pkg/README.md) — packages.\n\n## Usage\n\n- `go test ./...` — run tests.\n\n```sh\n- `codeblock.go` — ignored\n```\n",
				"go.mod":               "module demo\n",
				"pkg/README.md":        "# pkg\n\nCore package.\n\n## 文件索引\n\n- `a.go` — alpha.\n",
				"pkg/a.go":             "package pkg\n",
				".github/workflows/ci": "name: ci\n",
				"data/local.db":        "ignored\n",
				"node_modules/x/a.js":  "ignored\n",
			},
		},
		{
			name: "missing README in subdir",
			files: map[string]string{
				"README.md": "# root\n\n## 目录索引\n\n- `pkg/` — packages.\n",
				"pkg/a.go":  "package pkg\n",
			},
			wantContains: []string{"pkg: missing README.md"},
		},
		{
			name: "missing file and subdir entries",
			files: map[string]string{
				"README.md":     "# root\n\n## 目录索引\n\n- `a.go` — a.\n",
				"a.go":          "package main\n",
				"b.go":          "package main\n",
				"sub/README.md": "# sub\n\n## 文件索引\n\n- `c.go` — c.\n",
				"sub/c.go":      "package sub\n",
			},
			wantContains: []string{
				".: missing entry for file `b.go`",
				".: missing entry for directory `sub/`",
			},
		},
		{
			name: "stale file and subdir entries",
			files: map[string]string{
				"README.md": "# root\n\n## 目录索引\n\n- `a.go` — a.\n- `ghost.go` — removed.\n- `olddir/` — removed dir.\n",
				"a.go":      "package main\n",
			},
			wantContains: []string{
				".: stale entry `ghost.go`",
				".: stale entry `olddir/`",
			},
		},
		{
			name: "type mismatch file vs directory",
			files: map[string]string{
				"README.md":     "# root\n\n## 目录索引\n\n- `a.go/` — wrong.\n- `sub` — missing slash.\n",
				"a.go":          "package main\n",
				"sub/README.md": "# sub\n\n## 文件索引\n\n- `c.go` — c.\n",
				"sub/c.go":      "package sub\n",
			},
			wantContains: []string{
				".: stale entry `a.go/`",
				".: missing entry for file `a.go`",
				".: stale entry `sub`",
				".: missing entry for directory `sub/`",
			},
		},
		{
			name: "duplicate entry in README",
			files: map[string]string{
				"README.md": "# root\n\n## 文件索引\n\n- `a.go` — first.\n- `a.go` — duplicate.\n",
				"a.go":      "package main\n",
			},
			wantContains: []string{
				".: duplicate entry `a.go`",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, tc.files)

			issues, err := checkRepo(t.Context(), root)
			if err != nil {
				t.Fatalf("checkRepo returned error: %v", err)
			}

			if len(tc.wantContains) == 0 {
				if len(issues) != 0 {
					t.Fatalf("expected no issues, got: %v", issues)
				}
				return
			}

			for _, want := range tc.wantContains {
				found := slices.ContainsFunc(issues, func(got string) bool {
					return strings.Contains(got, want)
				})
				if !found {
					t.Errorf("expected issue containing %q, got issues: %v", want, issues)
				}
			}
		})
	}
}

func TestCheckRepo_GitAware(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init")
	writeFiles(t, root, map[string]string{
		".gitignore":               ".env.local\ndata/\n",
		".env.local":               "SECRET=123\n",
		"data/snapshot/old.db":     "ignored\n",
		".github/workflows/ci.yml": "name: ci\n",
		"README.md":                "# repo\n\n## 目录索引\n\n- `.gitignore` — ignore rules.\n- `keep.go` — kept file.\n",
		"keep.go":                  "package main\n",
	})
	runGit("add", ".gitignore", "README.md", "keep.go", ".github/workflows/ci.yml")

	issues, err := checkRepo(t.Context(), root)
	if err != nil {
		t.Fatalf("checkRepo error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected clean repo with gitignored files, got: %v", issues)
	}

	// Untracked non-ignored file must be caught before git add.
	writeFiles(t, root, map[string]string{"newfile.go": "package main\n"})
	issues, err = checkRepo(t.Context(), root)
	if err != nil {
		t.Fatalf("checkRepo error: %v", err)
	}
	if !slices.ContainsFunc(issues, func(s string) bool { return strings.Contains(s, "newfile.go") }) {
		t.Fatalf("expected untracked file newfile.go to be flagged, got: %v", issues)
	}

	// Deleted tracked file must be flagged as stale if still listed in README.
	if err := os.Remove(filepath.Join(root, "newfile.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "keep.go")); err != nil {
		t.Fatal(err)
	}
	issues, err = checkRepo(t.Context(), root)
	if err != nil {
		t.Fatalf("checkRepo error: %v", err)
	}
	if !slices.ContainsFunc(issues, func(s string) bool { return strings.Contains(s, "stale entry `keep.go`") }) {
		t.Fatalf("expected deleted keep.go to be flagged as stale, got: %v", issues)
	}
}
