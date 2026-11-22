package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var excludedDirs = map[string]bool{
	".git":         true,
	".github":      true,
	"data":         true,
	"node_modules": true,
}

var walkIgnoredNames = map[string]bool{
	".DS_Store":   true,
	"Thumbs.db":   true,
	".svelte-kit": true,
	".vite":       true,
	".idea":       true,
	".vscode":     true,
}

var (
	entryPattern   = regexp.MustCompile("^[-*]\\s+(?:\\[`([^`]+)`\\]\\([^)]+\\)|`([^`]+)`)")
	headingPattern = regexp.MustCompile(`^(#{1,3})\s+(.+)$`)
)

type dirContents struct {
	files   map[string]bool
	subdirs map[string]bool
}

func main() {
	root := flag.String("root", ".", "repository root directory to validate")
	flag.Parse()

	issues, err := checkRepo(context.Background(), *root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "checkreadme: %v\n", err)
		os.Exit(2)
	}
	if len(issues) > 0 {
		for _, issue := range issues {
			fmt.Fprintln(os.Stderr, issue)
		}
		fmt.Fprintf(os.Stderr, "checkreadme: found %d issue(s)\n", len(issues))
		os.Exit(1)
	}
	fmt.Println("checkreadme: all directory README.md indexes are up to date")
}

func checkRepo(ctx context.Context, root string) ([]string, error) {
	files, err := collectFiles(ctx, root)
	if err != nil {
		return nil, err
	}

	dirs := buildDirTree(files)
	dirNames := slices.Sorted(maps.Keys(dirs))

	var issues []string
	for _, dir := range dirNames {
		contents := dirs[dir]
		readmeRel := "README.md"
		if dir != "." {
			readmeRel = path.Join(dir, "README.md")
		}
		readmePath := filepath.Join(root, filepath.FromSlash(readmeRel))

		raw, err := os.ReadFile(readmePath)
		if errors.Is(err, os.ErrNotExist) {
			issues = append(issues, fmt.Sprintf("%s: missing README.md", dir))
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", readmeRel, err)
		}

		entries := parseIndexedEntries(string(raw))
		seen := make(map[string]bool, len(entries))
		for _, entry := range entries {
			if entry == "README.md" {
				continue
			}
			if seen[entry] {
				issues = append(issues, fmt.Sprintf("%s: duplicate entry `%s`", dir, entry))
				continue
			}
			seen[entry] = true

			if name, isDir := strings.CutSuffix(entry, "/"); isDir {
				if !contents.subdirs[name] {
					issues = append(issues, fmt.Sprintf("%s: stale entry `%s`", dir, entry))
				}
			} else {
				if !contents.files[entry] {
					issues = append(issues, fmt.Sprintf("%s: stale entry `%s`", dir, entry))
				}
			}
		}

		for _, file := range slices.Sorted(maps.Keys(contents.files)) {
			if file == "README.md" {
				continue
			}
			if !seen[file] {
				issues = append(issues, fmt.Sprintf("%s: missing entry for file `%s`", dir, file))
			}
		}
		for _, sub := range slices.Sorted(maps.Keys(contents.subdirs)) {
			want := sub + "/"
			if !seen[want] {
				issues = append(issues, fmt.Sprintf("%s: missing entry for directory `%s`", dir, want))
			}
		}
	}

	return issues, nil
}

func collectFiles(ctx context.Context, root string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		if _, err := exec.LookPath("git"); err == nil {
			return collectFromGit(ctx, root)
		}
	}
	return collectFromWalk(root)
}

func collectFromGit(ctx context.Context, root string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}

	var files []string
	for raw := range bytes.SplitSeq(out, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		rel := path.Clean(filepath.ToSlash(string(raw)))
		if hasExcludedSegment(rel) {
			continue
		}
		// Skip tracked files that were deleted locally without staging.
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); errors.Is(err, os.ErrNotExist) {
			continue
		}
		files = append(files, rel)
	}
	return files, nil
}

func collectFromWalk(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if excludedDirs[name] || walkIgnoredNames[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if walkIgnoredNames[name] || name == ".env" || strings.HasPrefix(name, ".env.") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	return files, nil
}

func hasExcludedSegment(rel string) bool {
	for seg := range strings.SplitSeq(rel, "/") {
		if excludedDirs[seg] {
			return true
		}
	}
	return false
}

func buildDirTree(files []string) map[string]*dirContents {
	dirs := make(map[string]*dirContents)
	ensure := func(dir string) *dirContents {
		c, ok := dirs[dir]
		if !ok {
			c = &dirContents{
				files:   make(map[string]bool),
				subdirs: make(map[string]bool),
			}
			dirs[dir] = c
		}
		return c
	}

	for _, rel := range files {
		dir := path.Dir(rel)
		base := path.Base(rel)
		ensure(dir).files[base] = true

		for cur := dir; cur != "."; {
			parent := path.Dir(cur)
			ensure(parent).subdirs[path.Base(cur)] = true
			cur = parent
		}
	}
	return dirs
}

func parseIndexedEntries(content string) []string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")

	var scoped []string
	var fallback []string
	hasIndexHeading := false
	inIndexSection := false
	inFence := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		if m := headingPattern.FindStringSubmatch(trimmed); m != nil {
			level := len(m[1])
			title := strings.ToLower(m[2])
			if isIndexHeading(title) {
				hasIndexHeading = true
				inIndexSection = true
			} else if level <= 2 {
				inIndexSection = false
			}
			continue
		}

		// Only match top-level bullets so indented explanatory notes are ignored.
		m := entryPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		entry := m[1]
		if entry == "" {
			entry = m[2]
		}
		fallback = append(fallback, entry)
		if inIndexSection {
			scoped = append(scoped, entry)
		}
	}

	if hasIndexHeading {
		return scoped
	}
	return fallback
}

func isIndexHeading(lowerTitle string) bool {
	for _, kw := range []string{"索引", "清单", "index", "contents", "files", "directories"} {
		if strings.Contains(lowerTitle, kw) {
			return true
		}
	}
	return false
}
