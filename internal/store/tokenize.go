package store

import (
	"strings"
	"unicode"
)

// TokenizeForFTS splits mixed CJK/ASCII text into space-delimited tokens for SQLite FTS5 unicode61.
func TokenizeForFTS(text string) string {
	return strings.Join(extractFTSTokens(text), " ")
}

// BuildFTSQuery constructs a safe FTS5 MATCH OR-expression from mixed CJK/ASCII text.
func BuildFTSQuery(text string) string {
	tokens := extractFTSTokens(text)
	if len(tokens) == 0 {
		return ""
	}
	seen := make(map[string]bool, len(tokens))
	parts := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if seen[tok] {
			continue
		}
		seen[tok] = true
		parts = append(parts, `"`+strings.ReplaceAll(tok, `"`, `""`)+`"`)
		if len(parts) >= 48 {
			break
		}
	}
	return strings.Join(parts, " OR ")
}

func extractFTSTokens(text string) []string {
	var tokens []string
	var asciiBuf strings.Builder
	var cjkRun []rune

	flushASCII := func() {
		if asciiBuf.Len() == 0 {
			return
		}
		tok := strings.ToLower(asciiBuf.String())
		asciiBuf.Reset()
		if isPureDigits(tok) && len(tok) <= 2 {
			return
		}
		tokens = append(tokens, tok)
	}

	flushCJK := func() {
		n := len(cjkRun)
		if n == 0 {
			return
		}
		if n == 1 {
			tokens = append(tokens, string(cjkRun[0]))
		} else {
			for i := range n - 1 {
				tokens = append(tokens, string(cjkRun[i:i+2]))
			}
		}
		cjkRun = cjkRun[:0]
	}

	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flushASCII()
			cjkRun = append(cjkRun, r)
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			flushCJK()
			asciiBuf.WriteRune(r)
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return tokens
}

func isPureDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
