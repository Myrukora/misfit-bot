package updater

import (
	"os"
	"path/filepath"
	"strings"
)

// NormalizeVersion trims a raw VERSION-file value and returns it in canonical
// form (no "v" prefix, no build metadata). It returns "" when the value is not
// a valid semantic version, so a corrupt or hand-mangled VERSION file can never
// inject arbitrary text into a -ldflags argument.
func NormalizeVersion(raw string) string {
	v, err := ParseVersion(raw)
	if err != nil {
		return ""
	}
	return v.String()
}

// ReadVersionFile returns the version declared in <dir>/VERSION — the single
// source of truth for the bot's release version, stamped into the binary by
// every build site — or "" when the file is missing, unreadable or malformed.
func ReadVersionFile(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "VERSION"))
	if err != nil {
		return ""
	}
	// Only the first non-empty line counts: a VERSION file may carry a comment
	// or trailing newline, but never a second version.
	for _, line := range strings.Split(string(data), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return NormalizeVersion(s)
		}
	}
	return ""
}
