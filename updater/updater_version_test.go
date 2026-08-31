package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	// The peeled duplicate collapses onto its tag and supplies the commit SHA;
	// branch refs and junk lines are dropped.
	want := []tagRef{{"v0.1.0", "e0f9c2d4"}, {"v0.2.0", "9b7f0a11"}}
	if len(got) != len(want) {
		t.Fatalf("parseTagRefs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseTagRefs()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	if empty := parseTagRefs(""); len(empty) != 0 {
		t.Errorf("parseTagRefs(\"\") = %v, want no tags", empty)
	}
}

// ── tags are scoped to the tracked branch ─────────────────────────────────

// gitTestRepo builds a throwaway clone of a throwaway origin: main carries
// v0.1.0 and v0.2.0, a hotfix branch carries v9.9.9, and FETCH_HEAD is set by
// a real `git fetch origin main` — the state Check() leaves behind.
func gitTestRepo(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "origin.git")
	dir := filepath.Join(root, "work")

	runIn := func(cwd string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@invalid", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@invalid", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git -C %s %s: %v (%s)", cwd, strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(msg string) {
		runIn(dir, "commit", "-q", "--allow-empty", "-m", msg)
	}

	runIn(root, "init", "-q", "--bare", "--initial-branch=main", "origin.git")
	runIn(root, "init", "-q", "-b", "main", "work")
	runIn(dir, "remote", "add", "origin", remote)
	runIn(dir, "commit", "-q", "--allow-empty", "-m", "first")
	runIn(dir, "tag", "v0.1.0")
	runIn(dir, "push", "-q", "--set-upstream", "origin", "main")

	runIn(dir, "checkout", "-q", "-b", "hotfix")
	commit("work off the tracked branch")
	runIn(dir, "tag", "v9.9.9") // a release published somewhere else
	runIn(dir, "push", "-q", "origin", "hotfix", "--tags")

	runIn(dir, "checkout", "-q", "main")
	commit("second")
	runIn(dir, "tag", "v0.2.0")
	runIn(dir, "push", "-q", "origin", "main", "--tags")

	runIn(dir, "fetch", "-q", "origin", "main") // leaves FETCH_HEAD at main's tip

	return New(dir, testLogger{}, func() *config.UpdaterConfig { return testCfg() })
}

func TestListRemoteTagsIgnoresForeignTags(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	m := gitTestRepo(t)

	// ls-remote itself sees every tag in the repository, including the one on
	// the hotfix branch; only tags merged into the tracked branch are releases.
	tags, err := m.listRemoteTags(context.Background(), testCfg())
	if err != nil {
		t.Fatalf("listRemoteTags: %v", err)
	}
	if strings.Join(tags, ",") != "v0.1.0,v0.2.0" {
		t.Errorf("listRemoteTags() = %v, want [v0.1.0 v0.2.0]", tags)
	}
	if latest, _ := LatestVersionTag(tags); latest != "0.2.0" {
		t.Errorf("LatestVersionTag() = %q, want 0.2.0 — the foreign v9.9.9 must not win", latest)
	}
}

func TestCheckReportsVersionsScopedToTheTrackedBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	m := gitTestRepo(t)
	m.SetVersion("0.1.0")

	res, err := m.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.ToVersion != "0.2.0" {
		t.Errorf("ToVersion = %q, want 0.2.0 (not the hotfix branch's v9.9.9)", res.ToVersion)
	}
	if !res.VersionBehind || res.FromVersion != "0.1.0" {
		t.Errorf("FromVersion/VersionBehind = %q/%v, want 0.1.0/true", res.FromVersion, res.VersionBehind)
	}
	if m.LatestVersion() != "0.2.0" {
		t.Errorf("LatestVersion() = %q, want the cached 0.2.0", m.LatestVersion())
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

func TestAnnounceUpdateIsScopedToItsTarget(t *testing.T) {
	m, sent := newTestManager(t, &fakeGH{})
	res := &CheckResult{Behind: 2, ToVersion: "0.2.0"}

	// The updater's configuration is live, so "the same version" is not by
	// itself the same release: another branch or another repo must announce
	// again rather than be swallowed by the previous target's dedupe entry.
	m.announceUpdate(testCfg(), res)

	otherBranch := testCfg()
	otherBranch.Branch = "stable"
	m.announceUpdate(otherBranch, res)

	otherRepo := testCfg()
	otherRepo.Repo = "someoneelse/fork"
	m.announceUpdate(otherRepo, res)

	if len(*sent) != 3 {
		t.Fatalf("announceUpdate posted %d embeds, want one per target", len(*sent))
	}

	// The identical targets stay silent, so the scoping did not simply disable
	// deduplication.
	m.announceUpdate(testCfg(), res)
	m.announceUpdate(otherBranch, res)
	if len(*sent) != 3 {
		t.Errorf("announceUpdate posted %d embeds, want the known targets left alone", len(*sent))
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
	if got := m.loadState().Announced; len(got) != 0 {
		t.Fatalf("Announced = %v after a failed send, want it left empty so the next poll retries", got)
	}

	sent := 0
	m.send = func(string, discord.Embed) error { sent++; return nil }
	m.announceUpdate(testCfg(), res)
	if sent != 1 {
		t.Errorf("failed announcement was not retried: %d sends", sent)
	}
}

// ── concurrent state access ───────────────────────────────────────────────

// TestConcurrentStateAccess exercises the updater's persisted state from
// several goroutines at once — the poll loop writing versions, the command and
// dashboard surfaces reading them. Run under -race it fails if any of them
// touches the shared struct (or marshals it) without Manager.mu.
func TestConcurrentStateAccess(t *testing.T) {
	m := New(t.TempDir(), testLogger{}, func() *config.UpdaterConfig { return testCfg() })

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				switch (w + n) % 5 {
				case 0:
					m.recordLatestVersion(fmt.Sprintf("0.%d.0", n))
				case 1:
					_ = m.LatestVersion()
				case 2:
					_ = m.Status()["latest_version"]
				case 3:
					m.updateState(func(st *state) { st.SeenPRs[n] = true })
				case 4:
					m.markReleaseAnnounced(releaseKey(testCfg(), fmt.Sprintf("0.%d.0", n)))
				}
			}
		}(w)
	}
	wg.Wait()

	if m.LatestVersion() == "" {
		t.Error("LatestVersion() lost the value written concurrently")
	}
	if got := len(m.loadState().SeenPRs); got == 0 {
		t.Error("updateState() writes were lost")
	}
}
