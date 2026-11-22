package envload

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Parse reads dotenv key=value pairs from r. Later entries for the same key overwrite earlier ones.
func Parse(r io.Reader) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if lineNum == 1 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if rest, ok := strings.CutPrefix(line, "export"); ok && len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimSpace(rest)
		}

		rawKey, rawVal, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("envload: line %d: missing '='", lineNum)
		}
		key := strings.TrimSpace(rawKey)
		if !isValidKey(key) {
			return nil, fmt.Errorf("envload: line %d: invalid key %q", lineNum, key)
		}

		val, err := parseValue(strings.TrimSpace(rawVal))
		if err != nil {
			return nil, fmt.Errorf("envload: line %d: %w", lineNum, err)
		}
		values[key] = val
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("envload: scan: %w", err)
	}
	return values, nil
}

// LoadFile parses path and sets any keys not already present in the process environment.
func LoadFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("envload: open %s: %w", path, err)
	}
	defer f.Close()

	values, err := Parse(f)
	if err != nil {
		return fmt.Errorf("envload: parse %s: %w", path, err)
	}
	for k, v := range values {
		if _, exists := os.LookupEnv(k); exists {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("envload: setenv %s: %w", k, err)
		}
	}
	return nil
}

// LoadIfExists loads each file in order, ignoring missing files and preserving existing env vars.
func LoadIfExists(paths ...string) error {
	for _, p := range paths {
		err := LoadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// LoadRepoEnv searches upward from startDir (or cwd if empty) to the nearest go.mod
// boundary for .env.local and loads it if present.
func LoadRepoEnv(startDir string) (string, error) {
	dir := startDir
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("envload: getwd: %w", err)
		}
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("envload: abs %s: %w", dir, err)
	}

	for cur := absDir; ; {
		candidate := filepath.Join(cur, ".env.local")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			if err := LoadFile(candidate); err != nil {
				return "", err
			}
			return candidate, nil
		}
		// Never walk above the repository module root.
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return "", nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", nil
		}
		cur = parent
	}
}

func isValidKey(k string) bool {
	if k == "" {
		return false
	}
	for i := range len(k) {
		c := k[i]
		isLetter := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_'
		isDigit := c >= '0' && c <= '9'
		if i == 0 && !isLetter {
			return false
		}
		if !isLetter && !isDigit {
			return false
		}
	}
	return true
}

func parseValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if raw[0] == '\'' {
		return parseSingleQuoted(raw)
	}
	if raw[0] == '"' {
		return parseDoubleQuoted(raw)
	}
	// Strip inline comment only when '#' is preceded by whitespace so URLs keep fragments.
	for i := range len(raw) {
		if raw[i] == '#' && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t') {
			return strings.TrimSpace(raw[:i]), nil
		}
	}
	return raw, nil
}

func parseSingleQuoted(raw string) (string, error) {
	end := strings.IndexByte(raw[1:], '\'')
	if end < 0 {
		return "", errors.New("unterminated quote")
	}
	val := raw[1 : 1+end]
	rest := strings.TrimSpace(raw[2+end:])
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return "", fmt.Errorf("unexpected trailing content %q", rest)
	}
	return val, nil
}

func parseDoubleQuoted(raw string) (string, error) {
	var b strings.Builder
	for i := 1; i < len(raw); i++ {
		switch raw[i] {
		case '\\':
			if i+1 >= len(raw) {
				return "", errors.New("unterminated quote")
			}
			i++
			switch raw[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			default:
				b.WriteByte('\\')
				b.WriteByte(raw[i])
			}
		case '"':
			rest := strings.TrimSpace(raw[i+1:])
			if rest != "" && !strings.HasPrefix(rest, "#") {
				return "", fmt.Errorf("unexpected trailing content %q", rest)
			}
			return b.String(), nil
		default:
			b.WriteByte(raw[i])
		}
	}
	return "", errors.New("unterminated quote")
}
