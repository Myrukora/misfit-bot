package updater

import (
	"fmt"
	"regexp"

	"strings"
)

// semverPattern is the official SemVer 2.0.0 grammar (semver.org), with the
// optional "v" prefix GitHub tags use. Numeric components reject leading zeros,
// pre-release identifiers are [0-9A-Za-z-] and must not be empty, and build
// metadata is accepted but never affects precedence.
var semverPattern = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// version is a parsed semantic version. pre is the pre-release string without
// its leading "-" ("" = a normal release). Build metadata is parsed only to
// validate it and then dropped — it never affects precedence.
//
// The numeric components are kept as the exact digit strings the grammar allows
// (never with a leading zero) rather than as ints, so ordering stays exact for
// any width: an absurd 30-digit component can neither overflow into a wrong
// order nor collapse onto a different version.
type version struct {
	major string
	minor string
	patch string
	pre   string
}

// ParseVersion parses a semantic version, with or without the "v" prefix, and
// tolerates surrounding whitespace (the VERSION file ends with a newline).
// Build metadata is ignored. Anything else — "dev", "1.2", "1.2.3.4", "1.2.x",
// "01.2.3", "v1.2.3-rc_1" — returns an error.
func ParseVersion(s string) (version, error) {
	trimmed := strings.TrimSpace(s)
	m := semverPattern.FindStringSubmatch(trimmed)
	if m == nil {
		return version{}, fmt.Errorf("invalid semantic version %q", trimmed)
	}
	return version{major: m[1], minor: m[2], patch: m[3], pre: m[4]}, nil
}

// Ahead reports whether the version in to sorts after the version in from. It
// returns false whenever either side is not a version — an unstamped "dev"
// build or a repo with no release tags — so callers never have to handle the
// error path just to decide "is there something newer?".
func Ahead(from, to string) bool {
	a, err := ParseVersion(from)
	if err != nil {
		return false
	}
	b, err := ParseVersion(to)
	if err != nil {
		return false
	}
	return compare(a, b) < 0
}

// IsVersion reports whether s parses as a semantic version.
func IsVersion(s string) bool {
	_, err := ParseVersion(s)
	return err == nil
}

// String returns the canonical "major.minor.patch[-pre]" form (no "v" prefix,
// no build metadata). The grammar forbids leading zeros, so the parsed digits
// already are the canonical digits.
func (v version) String() string {
	core := v.major + "." + v.minor + "." + v.patch
	if v.pre != "" {
		return core + "-" + v.pre
	}
	return core
}

// CompareVersions returns -1 when a sorts before b, 0 when they are equivalent
// and 1 when a sorts after, following SemVer 2.0.0 precedence rules: numeric
// components compare numerically (v0.10.0 > v0.9.9, never lexicographically), a
// pre-release sorts before its release, and build metadata is ignored.
// It errors when either side is not a semantic version.
func CompareVersions(a, b string) (int, error) {
	va, err := ParseVersion(a)
	if err != nil {
		return 0, err
	}
	vb, err := ParseVersion(b)
	if err != nil {
		return 0, err
	}
	return compare(va, vb), nil
}

func compare(a, b version) int {
	for _, pair := range [][2]string{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if c := compareNumeric(pair[0], pair[1]); c != 0 {
			return c
		}
	}
	return comparePre(a.pre, b.pre)
}

// compareNumeric orders two grammar-valid numeric identifiers of any width: with
// no leading zeros to account for, the longer digit string is the larger number,
// and equal-width strings then compare lexicographically.
func compareNumeric(a, b string) int {
	if len(a) != len(b) {
		return compareInts(len(a), len(b))
	}
	return strings.Compare(a, b)
}

func compareInts(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// comparePre implements SemVer §11: a version with a pre-release has lower
// precedence than the same version without one; otherwise the dot-separated
// identifiers are compared field by field (numeric identifiers numerically and
// always below alphanumeric ones, a shorter field list below a longer one).
func comparePre(a, b string) int {
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return 1 // release outranks pre-release
	case b == "":
		return -1
	}

	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, y := as[i], bs[i]
		if x == y {
			continue
		}
		if isAllDigits(x) != isAllDigits(y) {
			if isAllDigits(x) {
				return -1 // numeric identifiers rank below alphanumeric ones
			}
			return 1
		}
		if isAllDigits(x) {
			return compareNumeric(x, y) // exact at any width — no int conversion
		}
		return strings.Compare(x, y)
	}
	switch {
	case len(as) == len(bs):
		return 0
	case len(as) < len(bs):
		return -1
	default:
		return 1
	}
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// LatestVersionTag returns the highest version in tags, ignoring anything that
// is not a version. It accepts bare tag names, the "refs/tags/<name>" form
// printed by git ls-remote, and peeled annotated-tag entries ("<name>^{}"),
// which are treated as the tag they peel. ok is false when no entry parses.
func LatestVersionTag(tags []string) (string, bool) {
	var best version
	found := false
	for _, raw := range tags {
		name := strings.TrimSpace(raw)
		if idx := strings.LastIndex(name, "refs/tags/"); idx >= 0 {
			name = name[idx+len("refs/tags/"):]
		}
		name = strings.TrimSuffix(name, "^{}")
		v, err := ParseVersion(name)
		if err != nil {
			continue
		}
		if !found || compare(v, best) > 0 {
			best, found = v, true
		}
	}
	if !found {
		return "", false
	}
	return best.String(), true
}
