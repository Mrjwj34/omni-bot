package sandbox

import (
	"path/filepath"
	"runtime"
	"strings"
)

var allowedEnvKeys = map[string]bool{
	"PATH":                   true,
	"LANG":                   true,
	"LC_ALL":                 true,
	"LC_CTYPE":               true,
	"TERM":                   true,
	"TZ":                     true,
	"NO_COLOR":               true,
	"COLORTERM":              true,
	"TEMP":                   true,
	"TMP":                    true,
	"TMPDIR":                 true,
	"SYSTEMROOT":             true,
	"WINDIR":                 true,
	"COMSPEC":                true,
	"PATHEXT":                true,
	"OS":                     true,
	"PROCESSOR_ARCHITECTURE": true,
	"NUMBER_OF_PROCESSORS":   true,
	"SYSTEMDRIVE":            true,
	"PROGRAMFILES":           true,
	"PROGRAMFILES(X86)":      true,
	"COMMONPROGRAMFILES":     true,
	"PSMODULEPATH":           true,
	"SHELL":                  true,
}

var blockedEnvSubstrings = []string{
	"_KEY",
	"_TOKEN",
	"_SECRET",
	"_PASSWORD",
	"_PASSWD",
	"_CREDENTIAL",
	"_AUTH",
}

var blockedEnvPrefixes = []string{
	"GEMINI_",
	"OPENAI_",
	"DEEPSEEK_",
	"TTS_",
	"ANTHROPIC_",
	"GOOGLE_",
	"AWS_",
	"AZURE_",
	"NAPCAT_",
	"ONEBOT_",
	"OMNI_",
}

// SanitizeEnv builds a minimal allowlisted environment for sandboxed subprocesses, stripping all secrets and API keys.
func SanitizeEnv(parentEnv []string, sandboxDir string) []string {
	out := make([]string, 0, len(allowedEnvKeys)+4)
	seen := make(map[string]bool, len(allowedEnvKeys))

	for _, entry := range parentEnv {
		eq := strings.IndexByte(entry, '=')
		if eq <= 0 {
			continue
		}
		key := entry[:eq]
		upper := strings.ToUpper(key)
		if !allowedEnvKeys[upper] || isSensitiveEnvKey(upper) {
			continue
		}
		if seen[upper] {
			continue
		}
		seen[upper] = true
		out = append(out, entry)
	}

	if sandboxDir != "" {
		cleanDir := filepath.Clean(sandboxDir)
		out = append(out, "HOME="+cleanDir)
		if runtime.GOOS == "windows" {
			out = append(out, "USERPROFILE="+cleanDir)
		} else {
			out = append(out, "PWD="+cleanDir)
		}
	}
	return out
}

func isSensitiveEnvKey(upperKey string) bool {
	for _, prefix := range blockedEnvPrefixes {
		if strings.HasPrefix(upperKey, prefix) {
			return true
		}
	}
	for _, sub := range blockedEnvSubstrings {
		if strings.Contains(upperKey, sub) {
			return true
		}
	}
	return false
}
