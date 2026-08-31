package updater

import (
	"testing"
)

// ── ParseVersion ──────────────────────────────────────────────────────────

func TestParseVersionValid(t *testing.T) {
	for _, tc := range []struct {
		in        string
		major     string
		minor     string
		patch     string
		pre       string
		canonical string
	}{
		{"0.1.0", "0", "1", "0", "", "0.1.0"},
		{"v0.1.0", "0", "1", "0", "", "0.1.0"},
		{"1.22.3", "1", "22", "3", "", "1.22.3"},
		{"v10.20.30", "10", "20", "30", "", "10.20.30"},
		{"v0.2.0-rc.1", "0", "2", "0", "rc.1", "0.2.0-rc.1"},
		{"1.0.0-alpha", "1", "0", "0", "alpha", "1.0.0-alpha"},
		{"1.0.0-rc.1+build.5", "1", "0", "0", "rc.1", "1.0.0-rc.1"}, // build metadata ignored
		{" v1.2.3 ", "1", "2", "3", "", "1.2.3"},
		// Components are exact at any width — nothing is truncated to an int.
		{"99999999999999999999.0.0", "99999999999999999999", "0", "0", "", "99999999999999999999.0.0"},
	} {
		v, err := ParseVersion(tc.in)
		if err != nil {
			t.Errorf("ParseVersion(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if v.major != tc.major || v.minor != tc.minor || v.patch != tc.patch || v.pre != tc.pre {
			t.Errorf("ParseVersion(%q) = %+v, want major=%s minor=%s patch=%s pre=%q", tc.in, v, tc.major, tc.minor, tc.patch, tc.pre)
		}
		if got := v.String(); got != tc.canonical {
			t.Errorf("ParseVersion(%q).String() = %q, want %q", tc.in, got, tc.canonical)
		}
	}
}

func TestParseVersionInvalid(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		"dev",
		"1",
		"1.2",
		"1.2.3.4",
		"a.b.c",
		"1.2.x",
		"v1.2",
		"1.2.3-",        // empty prerelease after the dash
		"1.2.3+",        // empty build metadata
		"1.2.3-rc..1",   // empty prerelease identifier
		"1.2.3-rc_1",    // underscore is not a legal identifier character
		"01.2.3",        // leading zeros are not legal
		"1.0.0-alpha..", // empty prerelease identifier
		"-1.2.3",
		"1.2.-3",
		"vv1.2.3",
	} {
		if v, err := ParseVersion(in); err == nil {
			t.Errorf("ParseVersion(%q) = %+v, want an error", in, v)
		}
	}
}

// ── CompareVersions ───────────────────────────────────────────────────────

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		// ordering along each position
		{"0.1.0", "0.2.0", -1},
		{"0.2.0", "0.1.0", 1},
		{"0.1.0", "0.1.0", 0},
		{"1.0.0", "0.99.99", 1},
		{"1.1.0", "1.0.99", 1},
		{"1.0.1", "1.0.0", 1},

		// numeric, not lexicographic: 0.10.0 > 0.9.9, and padding must not matter
		{"v0.10.0", "v0.9.9", 1},
		{"v0.9.9", "v0.10.0", -1},
		{"v0.9.10", "v0.9.9", 1},
		{"v1.0.0", "v1.0.10", -1},
		{"0.0.100", "0.0.99", 1},

		// the "v" prefix is insignificant, including when comparing across forms
		{"v0.2.0", "0.2.0", 0},
		{"v0.2.0", "0.10.0", -1},

		// pre-release sorts before the associated release (SemVer §11)
		{"v0.2.0-rc.1", "v0.2.0", -1},
		{"v0.2.0", "v0.2.0-rc.1", 1},
		{"0.2.0-alpha", "0.2.0-beta", -1},
		{"0.2.0-alpha.1", "0.2.0-alpha.beta", -1}, // numeric < alphanumeric
		{"0.2.0-beta.2", "0.2.0-beta.11", -1},     // numeric compare, not lexical
		{"0.2.0-beta.2", "0.2.0-beta", 1},         // more identifiers win
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-rc.1", "1.0.0-rc.1", 0},

		// build metadata is ignored for precedence
		{"1.0.0+build.1", "1.0.0+build.2", 0},
		{"1.0.0+build.1", "1.0.0", 0},

		// widths beyond any integer type still order exactly: components are
		// compared as digit strings, so nothing saturates or compares equal
		{"99999999999999999999.0.0", "100000000000000000000.0.0", -1},
		{"1.0.0-99999999999999999999", "1.0.0-100000000000000000000", -1},
		{"18446744073709551615.0.0", "18446744073709551616.0.0", -1}, // past MaxUint64
	} {
		got, err := CompareVersions(tc.a, tc.b)
		if err != nil {
			t.Errorf("CompareVersions(%q, %q): unexpected error: %v", tc.a, tc.b, err)
			continue
		}
		if got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		// antisymmetry: swapping the arguments flips the sign
		if rev, err := CompareVersions(tc.b, tc.a); err != nil {
			t.Errorf("CompareVersions(%q, %q): unexpected error: %v", tc.b, tc.a, err)
		} else if rev != -tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d (antisymmetry)", tc.b, tc.a, rev, -tc.want)
		}
	}
}

// TestCompareVersionsSpecChain walks the precedence example from SemVer 2.0.0
// §11: every neighbouring pair must compare as strictly increasing.
func TestCompareVersionsSpecChain(t *testing.T) {
	ascending := []string{
		"1.0.0-alpha",
		"1.0.0-alpha.0.beta", // extra: longer field list beats the shorter prefix, 0 < 1
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
	}
	for i := 0; i+1 < len(ascending); i++ {
		l, r := ascending[i], ascending[i+1]
		got, err := CompareVersions(l, r)
		if err != nil {
			t.Errorf("CompareVersions(%q, %q): %v", l, r, err)
			continue
		}
		if got != -1 {
			t.Errorf("CompareVersions(%q, %q) = %d, want -1", l, r, got)
		}
	}
}

func TestCompareVersionsMalformed(t *testing.T) {
	for _, tc := range []struct{ a, b string }{
		{"dev", "0.1.0"},
		{"0.1.0", "dev"},
		{"", "0.1.0"},
		{"0.1", "0.1.0"},
	} {
		if got, err := CompareVersions(tc.a, tc.b); err == nil {
			t.Errorf("CompareVersions(%q, %q) = %d, want an error", tc.a, tc.b, got)
		}
	}
}

func TestIsVersion(t *testing.T) {
	for _, in := range []string{"0.1.0", "v0.1.0", "1.2.3-rc.1", "v1.2.3+build"} {
		if !IsVersion(in) {
			t.Errorf("IsVersion(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"", "   ", "dev", "1.2", "1.2.3.4", "v", "1.2.x"} {
		if IsVersion(in) {
			t.Errorf("IsVersion(%q) = true, want false", in)
		}
	}
}

// ── LatestVersionTag ──────────────────────────────────────────────────────

func TestLatestVersionTag(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags []string
		want string
		ok   bool
	}{
		{
			name: "highest patch wins",
			tags: []string{"v0.1.0", "v0.2.3", "v0.2.10", "v0.1.9"},
			want: "0.2.10",
			ok:   true,
		},
		{
			name: "numeric not lexicographic",
			tags: []string{"v0.9.9", "v0.10.0"},
			want: "0.10.0",
			ok:   true,
		},
		{
			name: "prerelease never outranks its release",
			tags: []string{"v0.2.0-rc.1", "v0.2.0"},
			want: "0.2.0",
			ok:   true,
		},
		{
			name: "non-version tags are ignored",
			tags: []string{"latest", "release-3", "v1", "v1.2", "0.1.0"},
			want: "0.1.0",
			ok:   true,
		},
		{
			name: "mixed v-prefix",
			tags: []string{"0.3.0", "v0.2.0"},
			want: "0.3.0",
			ok:   true,
		},
		{
			name: "ls-remote ref form is accepted",
			tags: []string{"refs/tags/v0.1.0", "refs/tags/v0.2.0", "refs/tags/v0.2.0^{}"},
			want: "0.2.0",
			ok:   true,
		},
		{name: "empty", tags: nil, want: "", ok: false},
		{name: "no version tags", tags: []string{"main", "HEAD"}, want: "", ok: false},
	} {
		got, ok := LatestVersionTag(tc.tags)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: LatestVersionTag(%q) = (%q, %v), want (%q, %v)", tc.name, tc.tags, got, ok, tc.want, tc.ok)
		}
	}
}
