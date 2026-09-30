package updater

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/misfit/bot/config"
)

// ── test doubles ──────────────────────────────────────────────────────────

type testLogger struct{}

func (testLogger) Debug(string, ...any) {}
func (testLogger) Info(string, ...any)  {}
func (testLogger) Warn(string, ...any)  {}
func (testLogger) Error(string, ...any) {}

// fakeGH is a scripted ghAPI implementation.
type fakeGH struct {
	mu      sync.Mutex
	head    string
	commits []ghCommit // returned by fetchCommitsSince
	prs     []ghPR
	user    *ghUser
	headErr error
	prsErr  error
	compErr error
}

func (f *fakeGH) fetchRemoteHead(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.head, f.headErr
}
func (f *fakeGH) fetchCommitsSince(context.Context, string) ([]ghCommit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commits, f.compErr
}
func (f *fakeGH) fetchOpenPRs(context.Context) ([]ghPR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prs, f.prsErr
}
func (f *fakeGH) fetchUser(context.Context) (*ghUser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.user, nil
}

func testCfg() *config.UpdaterConfig {
	return &config.UpdaterConfig{
		Enabled:       true,
		Repo:          "Myrukora/misfit-bot",
		Branch:        "main",
		CheckInterval: 300,
		AutoPull:      false, // tests never touch git
		NotifyChannel: "1234567890",
	}
}

// newTestManager builds a Manager with a scripted GitHub client and a
// capturing send function.
func newTestManager(t *testing.T, gh ghAPI) (*Manager, *[]discord.Embed) {
	t.Helper()
	var sent []discord.Embed
	m := New(t.TempDir(), testLogger{}, func() *config.UpdaterConfig { return testCfg() })
	m.gh = gh
	m.send = func(channelID string, e discord.Embed) error {
		if channelID != "1234567890" {
			t.Errorf("send to unexpected channel %q", channelID)
		}
		sent = append(sent, e)
		return nil
	}
	return m, &sent
}

// ── embed format ──────────────────────────────────────────────────────────

func TestBuildPREmbed(t *testing.T) {
	cfg := testCfg()
	pr := ghPR{
		Number:  3604,
		Title:   "Bipkibipki",
		Body:    "**bold** *italic* `code` and a [link](https://github.com)",
		HTMLURL: "https://github.com/Myrukora/misfit-bot/pull/3604",
		User:    ghUser{Login: "myrukora", AvatarURL: "https://avatars.githubusercontent.com/u/1", HTMLURL: "https://github.com/myrukora"},
	}
	e := buildPREmbed(cfg, pr)

	if e.Author == nil || e.Author.Name != "myrukora" || e.Author.URL != "https://github.com/myrukora" || e.Author.IconURL != "https://avatars.githubusercontent.com/u/1" {
		t.Errorf("author row wrong: %+v", e.Author)
	}
	if e.Title != "Pull request opened: #3604 Bipkibipki" {
		t.Errorf("title = %q, want `Pull request opened: #3604 Bipkibipki`", e.Title)
	}
	if e.URL != pr.HTMLURL {
		t.Errorf("url = %q, want PR html_url", e.URL)
	}
	if e.Color != colorGitHubGreen {
		t.Errorf("color = %#x, want GitHub green %#x", e.Color, colorGitHubGreen)
	}
	if e.Description != pr.Body {
		t.Errorf("description = %q, want the raw (markdown) body", e.Description)
	}
	if e.Footer == nil || e.Footer.Text != "Myrukora/misfit-bot @ main" {
		t.Errorf("footer = %+v", e.Footer)
	}
}

func TestBuildPREmbedEmptyBody(t *testing.T) {
	e := buildPREmbed(testCfg(), ghPR{Number: 1, Title: "x", User: ghUser{Login: "u"}})
	if e.Description != "No description provided." {
		t.Errorf("empty body should become 'No description provided.', got %q", e.Description)
	}
}

func TestBuildCommitEmbed(t *testing.T) {
	cfg := testCfg()
	c := ghCommit{
		SHA:     "1d84cc2abc123def456",
		HTMLURL: "https://github.com/Myrukora/misfit-bot/commit/1d84cc2abc123def456",
		Commit:  ghCommitDetails{Message: "Fix the thing\n\nDetails here", Author: ghCommitAuthor{Name: "Myrukora"}},
		Author:  &ghUser{Login: "myrukora", AvatarURL: "https://avatars.githubusercontent.com/u/1", HTMLURL: "https://github.com/myrukora"},
	}
	e := buildCommitEmbed(cfg, c)

	if e.Title != "1 new commit #1d84cc2" {
		t.Errorf("title = %q, want `1 new commit #1d84cc2`", e.Title)
	}
	if e.Color != colorGitHubBlue {
		t.Errorf("color = %#x, want GitHub blue %#x", e.Color, colorGitHubBlue)
	}
	if e.Description != c.Commit.Message {
		t.Errorf("description should be the full commit message")
	}
	if e.Author == nil || e.Author.Name != "myrukora" {
		t.Errorf("author row should use the GitHub account: %+v", e.Author)
	}
}

func TestBuildCommitEmbedNilAuthor(t *testing.T) {
	// Commits without a linked GitHub account must fall back to the git name.
	c := ghCommit{
		SHA:    "deadbeef1234",
		Commit: ghCommitDetails{Message: "no account", Author: ghCommitAuthor{Name: "Someone Else"}},
	}
	e := buildCommitEmbed(testCfg(), c)
	if e.Author == nil || e.Author.Name != "Someone Else" {
		t.Errorf("author fallback wrong: %+v", e.Author)
	}
}

func TestTruncate(t *testing.T) {
	short := "**bold** *italic* `code` and a [link](https://github.com)"
	if got := truncate(short, maxDescription); got != short {
		t.Errorf("short content must pass through untruncated")
	}
	long := strings.Repeat("я", 5000) // multibyte runes must not be split
	got := truncate(long, maxDescription)
	if len([]rune(got)) != maxDescription {
		t.Errorf("truncate length = %d runes, want %d", len([]rune(got)), maxDescription)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated string should end with an ellipsis")
	}
}

// TestFailedPRSendRetriesNextPoll verifies at-least-once delivery: a PR whose
// embed fails to send (e.g. "discord rest client unavailable" at startup) must
// NOT be marked seen, so the next poll retries it — and the state file never
// records it as delivered.
func TestFailedPRSendRetriesNextPoll(t *testing.T) {
	gh := &fakeGH{
		head: "cccccc1",
		prs:  []ghPR{{Number: 10, Title: "old pr", User: ghUser{Login: "u1"}}},
	}
	m, sent := newTestManager(t, gh)
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("seed poll: %v", err)
	}

	// PR #12 arrives; the send fails (transient — e.g. REST not ready).
	gh.mu.Lock()
	gh.prs = []ghPR{
		{Number: 10, Title: "old pr", User: ghUser{Login: "u1"}},
		{Number: 12, Title: "new feature", User: ghUser{Login: "u2"}},
	}
	gh.mu.Unlock()
	sendErr := errors.New("discord rest client unavailable")
	m.send = func(channelID string, e discord.Embed) error {
		if channelID != "1234567890" {
			t.Errorf("send to unexpected channel %q", channelID)
		}
		*sent = append(*sent, e)
		return sendErr
	}
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("poll with failing send: %v", err)
	}
	if m.loadState().SeenPRs[12] {
		t.Fatal("failed PR #12 must NOT be marked seen")
	}

	// Next poll: the send works — #12 is delivered exactly once and marked seen.
	delivered := 0
	m.send = func(channelID string, e discord.Embed) error {
		*sent = append(*sent, e)
		if e.Title == "Pull request opened: #12 new feature" {
			delivered++
		}
		return nil
	}
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("poll with working send: %v", err)
	}
	if !m.loadState().SeenPRs[12] {
		t.Fatal("delivered PR #12 must be marked seen")
	}
	if delivered != 1 {
		t.Errorf("PR #12 delivered %d times, want exactly 1", delivered)
	}
}

// TestFailedCommitSendRetriesWithoutDuplicates verifies the commit retry
// semantics: the last-seen SHA only advances past commits that were actually
// sent, so a failed commit is retried next poll while already-sent ones are
// NOT re-sent.
func TestFailedCommitSendRetriesWithoutDuplicates(t *testing.T) {
	gh := &fakeGH{
		head: "cccccc1",
		prs:  []ghPR{},
	}
	m, sent := newTestManager(t, gh)
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("seed poll: %v", err)
	}

	// Two new commits; the newest (ddddddd) sends fine, the older (cccccc2) fails.
	gh.mu.Lock()
	gh.head = "eeeeeee"
	gh.commits = []ghCommit{
		{SHA: "ddddddd", Commit: ghCommitDetails{Message: "newest", Author: ghCommitAuthor{Name: "u1"}}, Author: &ghUser{Login: "u1"}},
		{SHA: "cccccc2", Commit: ghCommitDetails{Message: "older", Author: ghCommitAuthor{Name: "u1"}}, Author: &ghUser{Login: "u1"}},
	}
	gh.mu.Unlock()
	failNext := true
	m.send = func(channelID string, e discord.Embed) error {
		*sent = append(*sent, e)
		if failNext && e.Title == "1 new commit #cccccc2" {
			failNext = false
			return errors.New("discord rest client unavailable")
		}
		return nil
	}
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("poll with partial failure: %v", err)
	}
	st := m.loadState()
	if st.LastCommitSHA != "ddddddd" {
		t.Fatalf("last seen SHA = %s, want ddddddd (newest sent, failed one NOT advanced past)", st.LastCommitSHA)
	}

	// Next poll: compare(base=ddddddd) yields only cccccc2 — retried, no dupes.
	gh.mu.Lock()
	gh.commits = []ghCommit{
		{SHA: "cccccc2", Commit: ghCommitDetails{Message: "older", Author: ghCommitAuthor{Name: "u1"}}, Author: &ghUser{Login: "u1"}},
	}
	gh.mu.Unlock()
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("retry poll: %v", err)
	}
	if st2 := m.loadState(); st2.LastCommitSHA != "eeeeeee" {
		t.Errorf("last seen SHA = %s, want head eeeeeee", st2.LastCommitSHA)
	}
	dddCount := 0
	for _, e := range *sent {
		if e.Title == "1 new commit #ddddddd" {
			dddCount++
		}
	}
	if dddCount != 1 {
		t.Errorf("commit ddddddd sent %d times, want exactly 1 (no duplicates)", dddCount)
	}
}

// TestFailedSendThenRecoveredWithoutRestart verifies that a poll with a
// permanent-looking send failure leaves the PR unseen even after several polls.
func TestFailedSendThenRecoveredWithoutRestart(t *testing.T) {
	gh := &fakeGH{
		head: "cccccc1",
		prs:  []ghPR{{Number: 10, Title: "old pr", User: ghUser{Login: "u1"}}},
	}
	m, sent := newTestManager(t, gh)
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("seed poll: %v", err)
	}
	gh.mu.Lock()
	gh.prs = []ghPR{
		{Number: 10, Title: "old pr", User: ghUser{Login: "u1"}},
		{Number: 42, Title: "stuck", User: ghUser{Login: "u2"}},
	}
	gh.mu.Unlock()

	m.send = func(channelID string, e discord.Embed) error { return errors.New("rest unavailable") }
	for i := 0; i < 3; i++ {
		if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
	}
	if m.loadState().SeenPRs[42] {
		t.Fatal("PR #42 marked seen despite never being delivered")
	}
	// Recovery: the next successful poll delivers it.
	m.send = func(channelID string, e discord.Embed) error {
		*sent = append(*sent, e)
		return nil
	}
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("recovery poll: %v", err)
	}
	if !m.loadState().SeenPRs[42] {
		t.Fatal("PR #42 should be seen after successful delivery")
	}
}

// TestNormalizeChannel covers the send-time tolerance: configs saved before
// mention normalization (raw <#id> values) must still work.
func TestNormalizeChannel(t *testing.T) {
	cases := map[string]string{
		"<#1534790217545027745>":     "1534790217545027745",
		"1534790217545027745":        "1534790217545027745",
		"  <#1534790217545027745>  ": "1534790217545027745",
		"":                           "",
		"updates":                    "updates", // not a mention — parse will reject later
	}
	for in, want := range cases {
		if got := normalizeChannel(in); got != want {
			t.Errorf("normalizeChannel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsMergeCommit(t *testing.T) {
	merges := []string{
		"Merge pull request #123 from user/branch",
		"Merge branch 'main' into feat/x",
		"Merge commit 'abc123'",
	}
	for _, m := range merges {
		if !isMergeCommit(m) {
			t.Errorf("%q should be treated as a merge commit", m)
		}
	}
	notMerges := []string{
		"Merge things manually",
		"Fix bug in merge logic",
		"merging stuff (lowercase)",
		"",
	}
	for _, m := range notMerges {
		if isMergeCommit(m) {
			t.Errorf("%q should NOT be treated as a merge commit", m)
		}
	}
}

// ── state persistence ─────────────────────────────────────────────────────

func TestStateRoundTrip(t *testing.T) {
	m := New(t.TempDir(), testLogger{}, func() *config.UpdaterConfig { return testCfg() })
	st := m.loadState()
	st.LastCommitSHA = "abc123"
	st.SeenPRs = map[int]bool{1: true, 42: true}
	st.Seeded = true
	if err := m.saveState(); err != nil {
		t.Fatalf("save: %v", err)
	}

	m2 := New(m.Dir, testLogger{}, func() *config.UpdaterConfig { return testCfg() })
	st2 := m2.loadState()
	if st2.LastCommitSHA != "abc123" || !st2.Seeded || !st2.SeenPRs[42] || st2.SeenPRs[7] {
		t.Errorf("round-trip mismatch: %+v", st2)
	}
}

func TestStateCorruptFile(t *testing.T) {
	m := New(t.TempDir(), testLogger{}, func() *config.UpdaterConfig { return testCfg() })
	if err := os.WriteFile(m.statePath(), []byte("{not json"), 0600); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}
	st := m.loadState()
	if st.Seeded || st.LastCommitSHA != "" || len(st.SeenPRs) != 0 {
		t.Errorf("corrupt state should yield empty defaults, got %+v", st)
	}
}

// ── diffing against the fake GitHub client ────────────────────────────────

// TestFirstPollSeedsSilently verifies the no-spam guarantee: the first poll
// records HEAD + open PRs but posts nothing.
func TestFirstPollSeedsSilently(t *testing.T) {
	gh := &fakeGH{
		head: "cccccc1",
		prs: []ghPR{
			{Number: 10, Title: "old pr", User: ghUser{Login: "u1"}},
			{Number: 11, Title: "another old pr", User: ghUser{Login: "u1"}},
		},
	}
	m, sent := newTestManager(t, gh)
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("checkNotifications: %v", err)
	}
	if len(*sent) != 0 {
		t.Fatalf("first poll must seed silently, posted %d embeds", len(*sent))
	}
	st := m.loadState()
	if st.LastCommitSHA != "cccccc1" || !st.Seeded || !st.SeenPRs[10] || !st.SeenPRs[11] {
		t.Errorf("state not seeded: %+v", st)
	}
}

// TestSecondPollNotifiesNewPRsAndCommits verifies the diff: new PR + new
// commits (merge commits skipped) after seeding.
func TestSecondPollNotifiesNewPRsAndCommits(t *testing.T) {
	gh := &fakeGH{
		head: "cccccc1",
		prs:  []ghPR{{Number: 10, Title: "old pr", User: ghUser{Login: "u1"}}},
	}
	m, sent := newTestManager(t, gh)
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("seed poll: %v", err)
	}

	// Remote moves forward: one real commit + one merge commit + one new PR.
	gh.mu.Lock()
	gh.head = "eeeeeee"
	gh.commits = []ghCommit{
		{SHA: "ddddddd", Commit: ghCommitDetails{Message: "real feature", Author: ghCommitAuthor{Name: "u1"}}, Author: &ghUser{Login: "u1"}},
		{SHA: "eeeeeee", Commit: ghCommitDetails{Message: "Merge pull request #12 from u1/branch"}, Author: &ghUser{Login: "u1"}},
	}
	gh.prs = []ghPR{
		{Number: 10, Title: "old pr", User: ghUser{Login: "u1"}},
		{Number: 12, Title: "new feature", Body: "body text", User: ghUser{Login: "u2"}},
	}
	gh.mu.Unlock()

	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("second poll: %v", err)
	}

	got := *sent
	if len(got) != 2 {
		t.Fatalf("want 2 embeds (1 PR + 1 non-merge commit), got %d: %+v", len(got), got)
	}
	// First embed: the PR (PRs are checked before commits in checkNotifications).
	if got[0].Title != "Pull request opened: #12 new feature" {
		t.Errorf("first embed should be the new PR, got %+v", got[0])
	}
	// Second embed: the non-merge commit (merge commit skipped).
	if got[1].Title != "1 new commit #ddddddd" {
		t.Errorf("second embed should be the non-merge commit, got %+v", got[1])
	}
	if got[1].Description != "real feature" {
		t.Errorf("commit description wrong: %+v", got[1].Description)
	}

	st := m.loadState()
	if st.LastCommitSHA != "eeeeeee" || !st.SeenPRs[12] || st.SeenPRs[10] == false {
		t.Errorf("state not advanced: %+v", st)
	}
}

// TestReopenedPRNotifiesAgain verifies seen-set pruning: a PR closed and
// reopened is notified again.
func TestReopenedPRNotifiesAgain(t *testing.T) {
	gh := &fakeGH{
		head: "cccccc1",
		prs:  []ghPR{{Number: 10, Title: "old pr", User: ghUser{Login: "u1"}}},
	}
	m, sent := newTestManager(t, gh)
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("seed poll: %v", err)
	}
	// PR 10 closed → not in the open set anymore.
	gh.mu.Lock()
	gh.prs = nil
	gh.mu.Unlock()
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("closed poll: %v", err)
	}
	if len(*sent) != 0 {
		t.Fatalf("closing a PR must not notify")
	}
	// PR 10 reopened.
	gh.mu.Lock()
	gh.prs = []ghPR{{Number: 10, Title: "old pr", User: ghUser{Login: "u1"}}}
	gh.mu.Unlock()
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("reopen poll: %v", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("reopened PR should notify again, got %d embeds", len(*sent))
	}
}

// TestForcePushResyncsSilently verifies the compare-failure path: the seen SHA
// resets to remote head without notifications.
func TestForcePushResyncsSilently(t *testing.T) {
	gh := &fakeGH{
		head: "cccccc1",
		prs:  []ghPR{},
	}
	m, sent := newTestManager(t, gh)
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("seed poll: %v", err)
	}
	gh.mu.Lock()
	gh.head = "fffffff"
	gh.compErr = context.DeadlineExceeded // compare fails (rewritten history)
	gh.mu.Unlock()
	if err := m.checkNotifications(context.Background(), testCfg()); err != nil {
		t.Fatalf("poll after force push: %v", err)
	}
	if len(*sent) != 0 {
		t.Fatalf("force-push resync must not notify, got %d embeds", len(*sent))
	}
	st := m.loadState()
	if st.LastCommitSHA != "fffffff" {
		t.Errorf("last seen SHA should resync to remote head, got %s", st.LastCommitSHA)
	}
}

// ── Apply: update must not depend on optional runtime artifacts ───────────

// applyTestRepo builds a throwaway working tree that Apply can drive end to
// end without touching the real repo: a bare origin whose main has one commit,
// a clone of it with a stale `bot` file and a `scripts/setup_imagefilter.sh`
// stand-in, checked out one commit behind so the fast-forward has work to do.
func applyTestRepo(t *testing.T, script string) *Manager {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}

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
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(msg string) {
		runIn(dir, "add", "-A")
		runIn(dir, "commit", "-q", "-m", msg)
	}

	runIn(root, "init", "-q", "--bare", "--initial-branch=main", "origin.git")
	runIn(root, "init", "-q", "-b", "main", "work")
	runIn(dir, "remote", "add", "origin", remote)

	// The tracked package Apply's `go build ./cmd/bot/` compiles.
	write("go.mod", "module applyfixture\n\ngo 1.21\n")
	write("cmd/bot/main.go", "package main\n\nfunc main() {}\n")
	write("scripts/setup_imagefilter.sh", script)
	commit("first")
	runIn(dir, "push", "-q", "--set-upstream", "origin", "main")

	// A newer origin commit to fast-forward onto.
	runIn(dir, "checkout", "-q", "-b", "dev")
	write("cmd/bot/main.go", "package main\n\nfunc main() { println(\"v2\") }\n")
	commit("second")
	runIn(dir, "push", "-q", "origin", "dev:main")

	// The local checkout stays behind remote main, with a stale binary in place.
	runIn(dir, "checkout", "-q", "main")
	write("bot", "old binary\n")

	return New(dir, testLogger{}, func() *config.UpdaterConfig { return testCfg() })
}

// TestApplyRunsArtifactSetupAfterSwap is the ordering guard: the optional
// imagefilter artifacts are re-provisioned as part of an update, after the new
// binary is in place and before the process is flagged for re-exec.
func TestApplyRunsArtifactSetupAfterSwap(t *testing.T) {
	m := applyTestRepo(t, "#!/usr/bin/env bash\nset -eu\n[ -x bot ]\necho new-binary-in-place\n")

	called := false
	m.setupArtifacts = func(context.Context) error {
		called = true
		// The hook must run after the swap, which is observable through the
		// renamed-away original and the freshly built replacement.
		if _, err := os.Stat(filepath.Join(m.Dir, "bot.old")); err != nil {
			t.Errorf("setup ran before the binary swap: bot.old missing (%v)", err)
		}
		return nil
	}

	if err := m.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !called {
		t.Fatal("Apply did not run the artifact setup hook")
	}
	if !m.ApplyRequested() {
		t.Fatal("Apply must still request the restart after a successful swap")
	}
	if b, err := os.ReadFile(filepath.Join(m.Dir, "bot")); err != nil {
		t.Fatalf("installed binary unreadable: %v", err)
	} else if string(b) == "old binary\n" {
		t.Error("the stale binary was not replaced")
	}
}

// TestApplyArtifactFailureDoesNotFailTheUpdate pins the non-fatal contract: a
// broken artifact provisioner (no pip, no network, unsupported host) must never
// surface as an update error — the filter stays cold, the bot still restarts.
func TestApplyArtifactFailureDoesNotFailTheUpdate(t *testing.T) {
	m := applyTestRepo(t, "#!/usr/bin/env bash\nexit 1\n")
	m.setupArtifacts = func(context.Context) error {
		return errors.New("simulated provisioning failure")
	}

	if err := m.Apply(context.Background()); err != nil {
		t.Fatalf("artifact failure must not fail the update, got: %v", err)
	}
	if !m.ApplyRequested() {
		t.Fatal("the update must complete and request a restart")
	}
}

// TestRunArtifactSetupReportsFailure covers the default implementation: a
// missing or failing script is reported, never mistaken for success. Apply is
// what decides to swallow that error.
func TestRunArtifactSetupReportsFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(dir, testLogger{}, func() *config.UpdaterConfig { return testCfg() })
	if err := m.setupArtifacts(context.Background()); err == nil {
		t.Fatal("a missing artifact script should be reported")
	}

	script := filepath.Join(dir, "scripts", "setup_imagefilter.sh")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\necho boom >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := m.setupArtifacts(context.Background())
	if err == nil {
		t.Fatal("a failing artifact script should be reported")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should carry the script's output, got: %v", err)
	}
}

// ── branch check ───────────────────────────────────────────────────────────

func TestCheckCheckedOutBranch(t *testing.T) {
	tests := []struct {
		name    string
		current string
		tracked string
		wantErr string
	}{
		{name: "match main", current: "main", tracked: "main", wantErr: ""},
		{name: "match named", current: "main", tracked: "", wantErr: ""},
		{name: "match empty default", current: "release", tracked: "release", wantErr: ""},
		{name: "mismatch", current: "feature-x", tracked: "main", wantErr: `on "feature-x" but the updater tracks "main"`},
		{name: "detached HEAD", current: "HEAD", tracked: "main", wantErr: "detached"},
		{name: "detached HEAD empty tracked", current: "HEAD", tracked: "", wantErr: "detached"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkCheckedOutBranch(tt.current, tt.tracked)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.wantErr)
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}
