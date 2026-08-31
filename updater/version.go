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
//
// The first real content line wins. Blank lines and "#" comment lines may
// surround it (a VERSION file is sometimes annotated), but a version is never
// read out of a comment and never has a trailing comment of its own — anything
// that is not a bare version fails to parse and reports "".
func ReadVersionFile(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "VERSION"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		return NormalizeVersion(s)
	}
	return ""
}
