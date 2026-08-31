package updater

import (
	"errors"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/misfit/bot/config"
)

// ── resolveVersions: tagged remote, untagged remote, unstamped build ──────

func TestResolveVersions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		build      string
		tags       []string
		wantFrom   string
		wantTo     string
		wantBehind bool
	}{
		{
			name:     "newer tag → behind by version",
			build:    "0.1.0",
			tags:     []string{"v0.1.0", "v0.2.0"},
			wantFrom: "0.1.0", wantTo: "0.2.0", wantBehind: true,
		},
		{
			name:     "same tag → not version-behind",
			build:    "0.2.0",
			tags:     []string{"v0.1.0", "v0.2.0"},
			wantFrom: "0.2.0", wantTo: "0.2.0", wantBehind: false,
		},
		{
			name:     "build ahead of every tag (a dev checkout past the last release)",
			build:    "v0.3.0",
			tags:     []string{"v0.1.0", "v0.2.0"},
			wantFrom: "0.3.0", wantTo: "0.2.0", wantBehind: false,
		},
		{
			name:     "no tags at all → the commit-count fallback",
			build:    "0.1.0",
			tags:     nil,
			wantFrom: "0.1.0", wantTo: "", wantBehind: false,
		},
		{
			name:     "only non-version tags → still the fallback",
			build:    "0.1.0",
			tags:     []string{"latest", "legacy-v1"},
			wantFrom: "0.1.0", wantTo: "", wantBehind: false,
		},
		{
			name:     "unstamped dev build → version reported as unknown",
			build:    "dev",
			tags:     []string{"v0.2.0"},
			wantFrom: "", wantTo: "0.2.0", wantBehind: false,
		},
		{
			name:     "numeric ordering, not lexicographic",
			build:    "0.9.9",
			tags:     []string{"v0.9.9", "v0.10.0"},
			wantFrom: "0.9.9", wantTo: "0.10.0", wantBehind: true,
		},
		{
			name:     "a pre-release tag never outranks the release it precedes",
			build:    "0.2.0",
			tags:     []string{"v0.2.0-rc.1"},
			wantFrom: "0.2.0", wantTo: "0.2.0-rc.1", wantBehind: false,
		},
	} {
		from, to, behind := resolveVersions(tc.build, tc.tags)
		if from != tc.wantFrom || to != tc.wantTo || behind != tc.wantBehind {
			t.Errorf("%s: resolveVersions(%q, %q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.name, tc.build, tc.tags, from, to, behind, tc.wantFrom, tc.wantTo, tc.wantBehind)
		}
	}
}

func TestCheckResultVersionSummary(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  CheckResult
		want string
	}{
		{
			name: "both sides known",
			res:  CheckResult{FromVersion: "0.1.0", ToVersion: "0.2.0", VersionBehind: true},
			want: "v0.1.0 → v0.2.0",
		},
		{
			name: "already on the newest release",
			res:  CheckResult{FromVersion: "0.2.0", ToVersion: "0.2.0"},
			want: "v0.2.0",
		},
		{
			name: "untagged repo → the running version alone",
			res:  CheckResult{FromVersion: "0.1.0"},
			want: "v0.1.0",
		},
		{
			name: "unstamped build → nothing to report",
			res:  CheckResult{FromVersion: "dev", ToVersion: "0.2.0", VersionBehind: false},
			want: "",
		},
	} {
		if got := tc.res.VersionSummary(); got != tc.want {
			t.Errorf("%s: VersionSummary() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// ── ls-remote parsing ─────────────────────────────────────────────────────

func TestParseTagRefs(t *testing.T) {
	out := "1ad5c1e7\trefs/tags/v0.1.0\n" +
		"e0f9c2d4\trefs/tags/v0.1.0^{}\n" +
		"9b7f0a11\trefs/tags/v0.2.0\n" +
		"deadbee1\trefs/heads/main\n" +
		"\n" +
		"garbage line\n"
	got := parseTagRefs(out)
	want := []string{"v0.1.0", "v0.2.0"} // peeled duplicate collapsed, branch refs dropped
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("parseTagRefs() = %v, want %v", got, want)
	}
	if empty := parseTagRefs(""); len(empty) != 0 {
		t.Errorf("parseTagRefs(\"\") = %v, want no tags", empty)
	}
}

// ── update announcement embed ─────────────────────────────────────────────

func TestBuildUpdateEmbed(t *testing.T) {
	res := &CheckResult{FromVersion: "0.1.0", ToVersion: "0.2.0", VersionBehind: true, Behind: 3}
	e := buildUpdateEmbed(testCfg(), res)

	if e.Title != "Update available — v0.1.0 → v0.2.0" {
		t.Errorf("title = %q, want the version pair in the title", e.Title)
	}
	if e.Footer == nil || !strings.HasPrefix(e.Footer.Text, "3 commits · ") {
		t.Errorf("footer = %+v, want the commit count first", e.Footer)
	}
	if e.Footer == nil || !strings.Contains(e.Footer.Text, "Myrukora/misfit-bot @ main") {
		t.Errorf("footer = %+v, want repo @ branch", e.Footer)
	}
	if !strings.Contains(e.Description, "rebuild") {
		t.Errorf("description = %q, want the apply hint", e.Description)
	}
}

func TestBuildUpdateEmbedWithoutVersion(t *testing.T) {
	// An untagged repo (or an unstamped build) must still produce a sane embed:
	// no empty "v → v" title, commit count still in the footer.
	res := &CheckResult{Behind: 1, NewSHAs: []string{"abc1234"}}
	e := buildUpdateEmbed(testCfg(), res)

	if e.Title != "Update available" {
		t.Errorf("title = %q, want the plain title", e.Title)
	}
	if e.Footer == nil || !strings.HasPrefix(e.Footer.Text, "1 commit · ") {
		t.Errorf("footer = %+v, want the singular commit count", e.Footer)
	}
	if !strings.Contains(e.Description, "abc1234") {
		t.Errorf("description = %q, want the new SHA listed", e.Description)
	}
}

// ── state plumbing: cache the latest version, announce each one once ──────

func TestRecordLatestVersionCachesAndRoundTrips(t *testing.T) {
	m := New(t.TempDir(), testLogger{}, func() *config.UpdaterConfig { return testCfg() })
	m.recordLatestVersion("") // a tagless repo must not create an entry
	if m.LatestVersion() != "" {
		t.Fatalf("LatestVersion() = %q, want empty", m.LatestVersion())
	}

	m.recordLatestVersion("0.2.0")
	if got := m.LatestVersion(); got != "0.2.0" {
		t.Fatalf("LatestVersion() = %q, want 0.2.0", got)
	}

	// Survives a restart through updater_state.json.
	m2 := New(m.Dir, testLogger{}, func() *config.UpdaterConfig { return testCfg() })
	if got := m2.LatestVersion(); got != "0.2.0" {
		t.Errorf("LatestVersion() after restart = %q, want 0.2.0", got)
	}
}

func TestAnnounceUpdateOncePerVersion(t *testing.T) {
	m, sent := newTestManager(t, &fakeGH{})
	res := &CheckResult{Behind: 3, FromVersion: "0.1.0", ToVersion: "0.2.0", VersionBehind: true}

	m.announceUpdate(testCfg(), res)
	m.announceUpdate(testCfg(), res) // same release — must not post twice
	if len(*sent) != 1 {
		t.Fatalf("announceUpdate posted %d embeds, want exactly 1", len(*sent))
	}
	if (*sent)[0].Title != "Update available — v0.1.0 → v0.2.0" {
		t.Errorf("title = %q", (*sent)[0].Title)
	}

	// A different release is announced again.
	m.announceUpdate(testCfg(), &CheckResult{Behind: 1, ToVersion: "0.3.0"})
	if len(*sent) != 2 {
		t.Fatalf("announceUpdate posted %d embeds, want 2 after a new release", len(*sent))
	}
}

func TestAnnounceUpdateNeedsATargetVersion(t *testing.T) {
	m, sent := newTestManager(t, &fakeGH{})
	// No version tag known (prod today has none) → no announcement at all.
	m.announceUpdate(testCfg(), &CheckResult{Behind: 5})
	if len(*sent) != 0 {
		t.Fatalf("announceUpdate posted %d embeds, want none without a version target", len(*sent))
	}
}

func TestAnnounceUpdateFailureIsRetried(t *testing.T) {
	m, _ := newTestManager(t, &fakeGH{})
	m.send = func(string, discord.Embed) error { return errors.New("rest unavailable") }
	res := &CheckResult{Behind: 2, ToVersion: "0.2.0"}

	m.announceUpdate(testCfg(), res)
	if got := m.loadState().NotifiedVersion; got != "" {
		t.Fatalf("NotifiedVersion = %q after a failed send, want it left unset so the next poll retries", got)
	}

	sent := 0
	m.send = func(string, discord.Embed) error { sent++; return nil }
	m.announceUpdate(testCfg(), res)
	if sent != 1 {
		t.Errorf("failed announcement was not retried: %d sends", sent)
	}
}
