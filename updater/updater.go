// Package updater keeps the bot in sync with its own GitHub repository:
// it polls for new commits and pull requests (posting embed notifications to
// a Discord channel), and can automatically pull the latest code, rebuild the
// bot binary (and Go plugin modules) and re-launch itself.
//
// The repository is assumed private (may be made public later); the GitHub
// token lives only in the gitignored config.yml and is used per-invocation
// (never stored in .git/config). No user-controlled input is ever
// interpolated into a shell command.
package updater

import (
	"context"
	"encoding/base64"
	"fmt"

	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/misfit/bot/config"
)

// Logger is the minimal logging surface the updater needs. The bot's
// *logger.Logger satisfies it structurally (defined locally to avoid an import
// cycle with the modules package).
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// CheckResult is the outcome of a [Manager.Check].
//
// Version reporting is additive on top of the commit-count check: ToVersion is
// the newest v-tag reachable on the tracked branch (empty when the repo has no
// version tags), and VersionBehind says whether that tag outranks the running
// build. "Up to date" stays defined by commit SHAs — see the package comment —
// so a repo without tags (or a binary built without a version stamp, which
// reports "dev") keeps working exactly as before.
type CheckResult struct {
	UpToDate  bool     `json:"up_to_date"`
	Behind    int      `json:"behind"`   // commits behind the remote branch
	NewSHAs   []string `json:"new_shas"` // abbreviated new SHAs, newest first (max 10)
	LocalSHA  string   `json:"local_sha"`
	RemoteSHA string   `json:"remote_sha"`

	FromVersion   string `json:"from_version"`   // version this build reports ("dev" when unstamped)
	ToVersion     string `json:"to_version"`     // newest version tag on the tracked branch ("" = none)
	VersionBehind bool   `json:"version_behind"` // the remote tag outranks FromVersion
}

// VersionSummary renders "v0.1.0 → v0.2.0" for humans, falling back to the
// running version alone when no newer tag is known. Returns "" when this build
// has no usable version (an unstamped "dev" build).
func (r *CheckResult) VersionSummary() string {
	from := displayVersion(r.FromVersion)
	if from == "" {
		return ""
	}
	to := displayVersion(r.ToVersion)
	if to == "" || !r.VersionBehind {
		return "v" + from
	}
	return "v" + from + " → v" + to
}

// resolveVersions compares the running build's version with the tags published
// on the tracked branch and returns the pair for a CheckResult. from is ""
// when the build has no usable version (an unstamped "dev" build); to is ""
// when the remote publishes no version tags at all — the caller then falls back
// to the commit-count check, which is the historical behaviour.
//
// versionBehind is deliberately true only when BOTH sides parse and the remote
// tag outranks the build: an unknown local version must never be reported as
// "behind" while the commit count already drives the decision.
func resolveVersions(buildVersion string, tags []string) (from, to string, versionBehind bool) {
	from = displayVersion(buildVersion)
	latest, ok := LatestVersionTag(tags)
	if !ok {
		return from, "", false
	}
	to = latest
	if from == "" {
		return from, to, false
	}
	cur, err := ParseVersion(from)
	if err != nil {
		return from, to, false
	}
	higher, err := ParseVersion(to)
	if err != nil {
		return from, to, false
	}
	return from, to, compare(cur, higher) < 0
}

// displayVersion canonicalises a version for display ("v0.1.0" and " 0.1.0 "
// both become "0.1.0") and returns "" for anything that is not a version.
func displayVersion(s string) string {
	v, err := ParseVersion(s)
	if err != nil {
		return ""
	}
	return v.String()
}

// Manager coordinates the self-update pipeline and GitHub notifications.
// Construct it once in main() and start Run() exactly once — in-process bot
// restarts must not spawn duplicate poll loops.
type Manager struct {
	Dir    string // bot directory (git working tree root)
	Logger Logger
	getCfg func() *config.UpdaterConfig // live config (SetConfig takes effect without restart)
	rest   rest.Rest                    // re-assigned via SetRest on every bot run()
	gh     ghAPI

	// send posts an embed to a channel; overridable in tests.
	send func(channelID string, e discord.Embed) error

	buildVersion string // version of the running binary (main injects it via SetVersion)

	mu        sync.Mutex // guards state, lastCheck, lastError, buildVersion, listTags
	state     *state
	lastCheck time.Time
	lastError string

	// listTags enumerates the tag refs on origin. Overridable in tests; the
	// default shells out to git ls-remote.
	listTags       func(ctx context.Context, cfg *config.UpdaterConfig) ([]string, error)
	applyMu        sync.Mutex  // serializes Apply (and its build steps)
	applyRequested atomic.Bool // set when a new binary is installed and a restart is pending
	onApplied      func()      // invoked after a successful Apply (main wires it to the restart channel)

	restReady chan struct{} // closed on the first SetRest; Run waits for it so the first
	restOnce  sync.Once     // poll never fires with a nil REST client
}

// New creates the updater manager. getCfg returns the live updater config
// (main passes a closure over Cfg.Updater).
func New(dir string, logger Logger, getCfg func() *config.UpdaterConfig) *Manager {
	m := &Manager{
		Dir:       dir,
		Logger:    logger,
		getCfg:    getCfg,
		gh:        newGitHubClient(),
		restReady: make(chan struct{}),
	}
	m.send = m.sendEmbed
	m.listTags = m.listRemoteTags
	return m
}

// SetVersion records the version stamped into the running binary (main passes
// its Version variable). It is the "from" side of every version comparison;
// an unstamped build passes "dev" and the updater degrades to commit counting.
func (m *Manager) SetVersion(v string) {
	v = strings.TrimSpace(v)
	if !IsVersion(v) {
		// Not fatal: every comparison degrades to the historical commit-count
		// check, but the owner should know the build was not stamped.
		m.Logger.Debug("Updater: build version %q is not a semantic version — reporting commits, not releases", v)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buildVersion = v
}

// Version returns the running binary's version as reported by SetVersion
// ("dev" for an unstamped build, "" when never set).
func (m *Manager) Version() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buildVersion
}

// LatestVersion returns the newest release tag the updater has seen on the
// tracked branch (persisted across restarts). Empty until the first successful
// check that finds a version tag. The read takes Manager.mu: the state is a
// single shared struct that the poll loop writes concurrently.
func (m *Manager) LatestVersion() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadStateLocked().LatestVersion
}

// SetRest attaches the current Discord REST client. Called on every bot run()
// because the disgo client is recreated on restart. The first call also
// releases the poll loop (see restReady).
func (m *Manager) SetRest(r rest.Rest) {
	m.rest = r
	m.restOnce.Do(func() { close(m.restReady) })
}

// OnApplied registers the callback invoked after a successful Apply (before
// the process re-executes itself).
func (m *Manager) OnApplied(fn func()) { m.onApplied = fn }

// ApplyRequested reports whether a new binary was installed and the process
// should re-exec itself instead of an in-process restart.
func (m *Manager) ApplyRequested() bool { return m.applyRequested.Load() }

// ResetApplied clears the re-exec flag (used when the exec fails and the bot
// falls back to an in-process restart).
func (m *Manager) ResetApplied() { m.applyRequested.Store(false) }

// Run starts the poll loop. It runs forever (until the process exits): the
// first tick seeds state silently (records current HEAD + open PRs, posts
// nothing), then every check_interval it checks GitHub notifications and, if
// auto_pull is enabled and new commits exist, applies the update.
func (m *Manager) Run(ctx context.Context) {
	cfg := m.getCfg()
	if cfg == nil || !cfg.Enabled || cfg.Repo == "" {
		m.Logger.Info("Updater disabled (set updater.repo to enable)")
		return
	}
	m.Logger.Info("Updater running: watching %s @ %s (every %ds, auto_pull=%v)", cfg.Repo, cfg.Branch, cfg.CheckInterval, cfg.AutoPull)

	// The Discord REST client is attached shortly after the gateway connects —
	// wait for it so the first poll (and its notifications) can't fire with a
	// nil client. Failures that still slip through are retried on the next
	// poll (state only advances on successful delivery).
	select {
	case <-m.restReady:
	case <-time.After(30 * time.Second):
		m.Logger.Warn("Updater: timed out waiting for the Discord REST client — notification failures will retry on the next poll")
	case <-ctx.Done():
		return
	}

	m.poll(ctx) // seed tick: silent

	interval := time.Duration(cfg.CheckInterval) * time.Second
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

// poll runs one full check cycle: notifications first, then the auto-update.
func (m *Manager) poll(ctx context.Context) {
	cfg := m.getCfg()
	if cfg == nil || !cfg.Enabled || cfg.Repo == "" {
		return
	}
	m.syncClient(cfg)

	if err := m.checkNotifications(ctx, cfg); err != nil {
		m.Logger.Warn("Updater: notification check failed: %v", err)
		m.setError(err)
	}

	if !cfg.AutoPull {
		return
	}
	res, err := m.Check(ctx)
	if err != nil {
		m.Logger.Warn("Updater: update check failed: %v", err)
		m.setError(err)
		return
	}
	if res.UpToDate {
		m.setError(nil)
		return
	}

	// Version-first reporting; the commit count stays the trigger either way.
	if summary := res.VersionSummary(); summary != "" && res.VersionBehind {
		m.Logger.Info("Updater: %s is behind by %d commit(s) — applying update", summary, res.Behind)
	} else {
		m.Logger.Info("Updater: %d new commit(s) behind %s — applying update", res.Behind, cfg.Branch)
	}

	m.announceUpdate(cfg, res)

	if err := m.Apply(ctx); err != nil {
		m.Logger.Error("Updater: auto-apply failed: %v", err)
		m.setError(err)
	}
}

// syncClient pushes the live config into the GitHub client for this cycle.
func (m *Manager) syncClient(cfg *config.UpdaterConfig) {
	if gc, ok := m.gh.(*githubClient); ok {
		gc.token = cfg.Token
		gc.repo = cfg.Repo
		gc.branch = cfg.Branch
	}
}

// checkNotifications diffs GitHub state against the persisted state and posts
// embeds for newly opened PRs and new commits. First run seeds silently.
//
// Delivery is at-least-once: a PR is only marked seen (and the last-seen
// commit SHA only advances) AFTER its embed was actually sent, so a failed
// send (e.g. REST client not ready at startup) is retried on the next poll —
// and survives restarts, since the state file never records it as delivered.
func (m *Manager) checkNotifications(ctx context.Context, cfg *config.UpdaterConfig) error {
	if cfg.NotifyChannel == "" {
		return nil // notifications disabled
	}
	// A private copy: this pass makes Discord sends between state updates and
	// must not hold Manager.mu (Status and the version cache read it), so the
	// edits land in one publish at the end instead of on the shared struct.
	st, commit := m.editState()

	// ── Pull requests ──
	prs, err := m.gh.fetchOpenPRs(ctx)
	if err != nil {
		return fmt.Errorf("fetch open PRs: %w", err)
	}
	open := make(map[int]bool, len(prs))
	for _, pr := range prs {
		open[pr.Number] = true
	}
	// Prune seen numbers that are no longer open: a PR that is closed and
	// later reopened will notify again.
	for n := range st.SeenPRs {
		if !open[n] {
			delete(st.SeenPRs, n)
		}
	}
	if st.Seeded {
		for _, pr := range prs {
			if st.SeenPRs[pr.Number] {
				continue
			}
			if err := m.send(cfg.NotifyChannel, buildPREmbed(cfg, pr)); err != nil {
				m.Logger.Warn("Updater: failed to post PR notification #%d (%v) — will retry on the next poll", pr.Number, err)
				continue // NOT marked seen → retried next poll
			}
			st.SeenPRs[pr.Number] = true
		}
	}

	// ── Commits ──
	head, err := m.gh.fetchRemoteHead(ctx)
	if err != nil {
		return fmt.Errorf("fetch remote head: %w", err)
	}
	if st.Seeded && st.LastCommitSHA != "" && st.LastCommitSHA != head {
		commits, err := m.gh.fetchCommitsSince(ctx, st.LastCommitSHA)
		if err != nil {
			// History was likely rewritten (force push) — resync silently.
			m.Logger.Warn("Updater: commit compare failed (%v) — resyncing last seen SHA to %s", err, shortSHA(head))
			st.LastCommitSHA = head
		} else {
			// Newest first; stop at the first failed send and keep the state at
			// the newest successfully-sent commit, so the failed one (and any
			// older) are retried on the next poll without re-sending the ones
			// that already went out.
			lastSent := st.LastCommitSHA
			allSent := true
			for _, c := range commits {
				if isMergeCommit(c.Commit.Message) {
					continue
				}
				if err := m.send(cfg.NotifyChannel, buildCommitEmbed(cfg, c)); err != nil {
					m.Logger.Warn("Updater: failed to post commit notification %s (%v) — will retry on the next poll", shortSHA(c.SHA), err)
					allSent = false
					break
				}
				lastSent = c.SHA
			}
			if allSent {
				st.LastCommitSHA = head
			} else {
				st.LastCommitSHA = lastSent
			}
		}
	}
	// ── record state ──
	if !st.Seeded {
		// First run: record the current state silently — the install poll must
		// not spam every already-open PR and commit.
		for _, pr := range prs {
			st.SeenPRs[pr.Number] = true
		}
		st.LastCommitSHA = head
	}
	st.Seeded = true
	if err := commit(); err != nil {
		m.Logger.Warn("Updater: failed to save state: %v", err)
	}
	return nil
}

// Check fetches the tracked branch and reports how far behind local HEAD is.
// It never modifies the working tree.
func (m *Manager) Check(ctx context.Context) (*CheckResult, error) {
	cfg := m.getCfg()
	if cfg == nil || cfg.Repo == "" {
		return nil, fmt.Errorf("updater is not configured (set updater.repo first)")
	}
	branch := cfg.Branch
	if branch == "" {
		branch = "main"
	}

	fetchArgs := append(m.gitAuthArgs(cfg), "fetch", "origin", branch)
	if out, err := m.gitOutput(ctx, fetchArgs...); err != nil {
		return nil, fmt.Errorf("git fetch origin %s: %v (%s)", branch, err, strings.TrimSpace(out))
	}
	local, err := m.gitOutput(ctx, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	remote, err := m.gitOutput(ctx, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return nil, fmt.Errorf("git rev-parse FETCH_HEAD: %w", err)
	}
	local = strings.TrimSpace(local)
	remote = strings.TrimSpace(remote)

	res := &CheckResult{UpToDate: local == remote, LocalSHA: local, RemoteSHA: remote}

	// Version awareness on top of the commit check. The tag list is best-effort:
	// a repo with no tags (or a failing ls-remote) leaves ToVersion empty and the
	// caller keeps deciding on commit counts alone.
	res.FromVersion = displayVersion(m.Version())
	if tags, err := m.listTagsOnce(ctx, cfg); err != nil {
		m.Logger.Debug("Updater: tag lookup failed (%v) — reporting commits only", err)
	} else {
		res.FromVersion, res.ToVersion, res.VersionBehind = resolveVersions(m.Version(), tags)
		m.recordLatestVersion(res.ToVersion)
	}

	if !res.UpToDate {
		if out, err := m.gitOutput(ctx, "rev-list", "--count", "HEAD..FETCH_HEAD"); err == nil {
			fmt.Sscanf(strings.TrimSpace(out), "%d", &res.Behind)
		}
		if out, err := m.gitOutput(ctx, "rev-list", "HEAD..FETCH_HEAD"); err == nil {
			fields := strings.Fields(out)
			for _, s := range fields {
				if len(res.NewSHAs) >= 10 {
					break
				}
				res.NewSHAs = append(res.NewSHAs, shortSHA(s))
			}
		}
	}

	m.mu.Lock()
	m.lastCheck = time.Now()
	m.lastError = ""
	m.mu.Unlock()
	return res, nil
}

// Apply performs the full update pipeline: fast-forward pull, rebuild the core
// binary, rebuild Go plugin modules, swap the binaries and mark the process
// for re-execution (via OnApplied). The bot keeps running the old code until
// the restart actually happens.
//
// If the working tree has local changes, the pull aborts with a clear error
// and nothing is touched.
func (m *Manager) Apply(ctx context.Context) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	cfg := m.getCfg()
	if cfg == nil || cfg.Repo == "" {
		return fmt.Errorf("updater is not configured (set updater.repo first)")
	}
	branch := cfg.Branch
	if branch == "" {
		branch = "main"
	}

	// 1. Fetch + fast-forward merge.
	fetchArgs := append(m.gitAuthArgs(cfg), "fetch", "origin", branch)
	if out, err := m.gitOutput(ctx, fetchArgs...); err != nil {
		return fmt.Errorf("git fetch origin %s: %v (%s)", branch, err, strings.TrimSpace(out))
	}
	if out, err := m.gitOutput(ctx, "merge", "--ff-only", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("git merge --ff-only: %v (%s)", err, strings.TrimSpace(out))
	}

	// 2. Build the core binary, stamping it with the version declared by the
	//    freshly merged VERSION file. Without the -X injection the new binary
	//    would report "dev" and the version-aware updater could never tell it
	//    was current. The value is validated (see NormalizeVersion) and passed
	//    as an argv element — never through a shell.
	buildArgs := []string{"build"}
	if v := ReadVersionFile(m.Dir); v != "" {
		buildArgs = append(buildArgs, "-ldflags", "-X main.Version="+v)
	} else {
		m.Logger.Warn("Updater: no valid VERSION file in %s — building without a version stamp", m.Dir)
	}
	buildArgs = append(buildArgs, "-o", "bot.new", "./cmd/bot/")
	build := exec.CommandContext(ctx, "go", buildArgs...)
	build.Dir = m.Dir
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	// 3. Swap binaries: bot → bot.old, bot.new → bot. (No plugin rebuild —
	//    the dashboard and feature modules are compiled into the single
	//    binary now.)
	botPath := filepath.Join(m.Dir, "bot")
	oldPath := filepath.Join(m.Dir, "bot.old")
	newPath := filepath.Join(m.Dir, "bot.new")
	if err := os.Rename(botPath, oldPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("backup current binary: %w", err)
	}
	if err := os.Rename(newPath, botPath); err != nil {
		return fmt.Errorf("install new binary: %w", err)
	}

	m.Logger.Info("Update applied: %s installed — restarting with the new build", botPath)
	m.applyRequested.Store(true)
	if m.onApplied != nil {
		m.onApplied()
	}
	return nil
}

// Status returns a snapshot of the updater's state for [p]update status.
func (m *Manager) Status() map[string]string {
	cfg := m.getCfg()
	m.mu.Lock()
	lastCheck := m.lastCheck
	lastErr := m.lastError
	m.mu.Unlock()
	st := m.stateSnapshot() // a private copy: the poll loop writes the shared one

	enabled, repo, branch, notify, interval, autoPull := false, "", "main", "", 0, false
	if cfg != nil {
		enabled, repo, branch, notify = cfg.Enabled, cfg.Repo, cfg.Branch, cfg.NotifyChannel
		interval, autoPull = cfg.CheckInterval, cfg.AutoPull
	}
	lastSHA := st.LastCommitSHA
	if len(lastSHA) > 7 {
		lastSHA = lastSHA[:7]
	}
	return map[string]string{
		"enabled":        fmt.Sprintf("%v", enabled),
		"repo":           repo,
		"branch":         branch,
		"interval":       fmt.Sprintf("%ds", interval),
		"auto_pull":      fmt.Sprintf("%v", autoPull),
		"notify_channel": notify,
		"last_sha":       lastSHA,
		"last_check":     lastCheck.Format("2006-01-02 15:04:05"),
		"last_error":     lastErr,
		// Version reporting (Task 3): what this build is, and the newest release
		// tag seen on the tracked branch ("" until a check finds one).
		"version":        m.Version(),
		"latest_version": st.LatestVersion,
	}
}

// NotifyTest is the temporary embed tester: it posts ONE sample PR embed and
// ONE sample commit embed to the configured notify channel so the owner can
// preview both formats (markdown included) before real events flow. When a
// token is configured, the sample author row uses the real authenticated user.
func (m *Manager) NotifyTest() error {
	cfg := m.getCfg()
	if cfg == nil || cfg.NotifyChannel == "" {
		return fmt.Errorf("no notify_channel configured — set it first with `update set notify_channel <channel-id>`")
	}
	ctx := context.Background()
	m.syncClient(cfg)

	user := ghUser{
		Login:     "octocat",
		AvatarURL: "https://avatars.githubusercontent.com/u/583231?v=4",
		HTMLURL:   "https://github.com/octocat",
	}
	if u, err := m.gh.fetchUser(ctx); err == nil && u != nil && u.Login != "" {
		user = *u
	} else if err != nil {
		m.Logger.Warn("Updater: could not fetch authenticated user for test embed (%v) — using sample data", err)
	}

	repo := cfg.Repo
	if repo == "" {
		repo = "owner/misfit-bot"
	}
	samplePR := ghPR{
		Number:  3604,
		Title:   "Bipkibipki",
		Body:    samplePRBody,
		HTMLURL: "https://github.com/" + repo + "/pull/3604",
		User:    user,
	}
	sampleCommit := ghCommit{
		SHA:     "1d84cc2abc123def456",
		HTMLURL: "https://github.com/" + repo + "/commit/1d84cc2abc123def456",
		Commit:  ghCommitDetails{Message: sampleCommitMsg, Author: ghCommitAuthor{Name: user.Login}},
		Author:  &user,
	}

	if err := m.send(cfg.NotifyChannel, buildPREmbed(cfg, samplePR)); err != nil {
		return fmt.Errorf("posting sample PR embed: %w", err)
	}
	if err := m.send(cfg.NotifyChannel, buildCommitEmbed(cfg, sampleCommit)); err != nil {
		return fmt.Errorf("posting sample commit embed: %w", err)
	}
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────

// listTagsOnce resolves origin's tag refs through the (test-overridable)
// listTags hook, defaulting to git ls-remote.
func (m *Manager) listTagsOnce(ctx context.Context, cfg *config.UpdaterConfig) ([]string, error) {
	if m.listTags != nil {
		return m.listTags(ctx, cfg)
	}
	return m.listRemoteTags(ctx, cfg)
}

// listRemoteTags lists the release tags published by origin *for the tracked
// branch*. git ls-remote is read-only and needs no working tree, but tags are
// global in git — a release tagged on some hotfix branch is not an update for
// this one — so the candidates are filtered by reachability afterwards.
func (m *Manager) listRemoteTags(ctx context.Context, cfg *config.UpdaterConfig) ([]string, error) {
	args := append(m.gitAuthArgs(cfg), "ls-remote", "--tags", "origin")
	out, err := m.gitOutput(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("git ls-remote --tags origin: %v (%s)", err, strings.TrimSpace(out))
	}

	// Only version-shaped tags can be releases; filtering before the ancestry
	// test keeps the number of git invocations down to the release count.
	refs := parseTagRefs(out)
	versions := make([]tagRef, 0, len(refs))
	for _, r := range refs {
		if IsVersion(r.name) {
			versions = append(versions, r)
		}
	}

	// Check fetched the tracked branch immediately before this call, so its tip
	// is available as FETCH_HEAD and the test costs no extra network round trip.
	return m.reachableTags(ctx, versions, "FETCH_HEAD"), nil
}

// tagRef is one remote tag: the tag name and the object it ultimately points
// at — the peeled commit for annotated tags.
type tagRef struct {
	name string
	sha  string
}

// parseTagRefs turns `git ls-remote --tags` output into name/SHA pairs, in
// listing order. A peeled entry ("refs/tags/v0.1.0^{}") is not a tag of its
// own: it supplies the commit SHA of the tag with the same name.
func parseTagRefs(out string) []tagRef {
	var order []string
	shaOf := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[1], "refs/tags/") {
			continue
		}
		name := strings.TrimPrefix(fields[1], "refs/tags/")
		peeled := strings.HasSuffix(name, "^{}")
		name = strings.TrimSuffix(name, "^{}")
		if name == "" {
			continue
		}
		if _, seen := shaOf[name]; !seen {
			order = append(order, name)
		}
		if peeled || shaOf[name] == "" {
			shaOf[name] = fields[0]
		}
	}
	refs := make([]tagRef, 0, len(order))
	for _, name := range order {
		refs = append(refs, tagRef{name: name, sha: shaOf[name]})
	}
	return refs
}

// reachableTags keeps the tags whose commit is contained in upstream. A tag
// from another branch has no object in this checkout, so the ancestry lookup
// fails and the tag is dropped — which is the correct answer: a release
// published elsewhere is not an update for the branch being tracked.
func (m *Manager) reachableTags(ctx context.Context, refs []tagRef, upstream string) []string {
	var names []string
	for _, r := range refs {
		if r.sha == "" {
			continue
		}
		if _, err := m.gitOutput(ctx, "merge-base", "--is-ancestor", r.sha, upstream); err != nil {
			m.Logger.Debug("Updater: tag %s is not on %s — ignored", r.name, upstream)
			continue
		}
		names = append(names, r.name)
	}
	return names
}

// parseTagRefs turns `git ls-remote --tags` output ("<sha>\trefs/tags/v1.2.3")
// into tag names. Peeled entries for annotated tags ("<name>^{}") collapse onto
// their base tag, and everything else (SHAs, stray lines) is dropped.

// recordLatestVersion caches the newest release tag seen on the tracked branch
// so [p]info and the dashboard can report it without shelling out to git.
func (m *Manager) recordLatestVersion(to string) {
	if to == "" {
		return
	}
	if m.LatestVersion() == to {
		return // the common path: no rewrite of updater_state.json per check
	}
	m.updateState(func(st *state) { st.LatestVersion = to })
}

// announceUpdate posts the "a new release is on the way" embed right before an
// auto-update is applied, deduplicated per release: a failing Apply (or several
// polls) must not spam the channel about the same one.
func (m *Manager) announceUpdate(cfg *config.UpdaterConfig, res *CheckResult) {
	if cfg.NotifyChannel == "" || res.ToVersion == "" {
		return
	}
	// The dedupe key covers the whole target, not just the version (see
	// releaseKey), and the Discord send happens outside Manager.mu — only the
	// read and the mark take the lock.
	key := releaseKey(cfg, res.ToVersion)
	if m.releaseAnnounced(key) {
		return
	}
	if err := m.send(cfg.NotifyChannel, buildUpdateEmbed(cfg, res)); err != nil {
		m.Logger.Warn("Updater: failed to post the update announcement (%v) — retrying on the next poll", err)
		return
	}
	m.markReleaseAnnounced(key)
}

// maxAnnouncedReleases bounds the announcement history: room for a long run of
// releases (and for the occasional repo/branch/channel change) without letting
// updater_state.json grow forever.
const maxAnnouncedReleases = 20

func (m *Manager) releaseAnnounced(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.loadStateLocked().Announced {
		if k == key {
			return true
		}
	}
	return false
}

func (m *Manager) markReleaseAnnounced(key string) {
	m.updateState(func(st *state) {
		for _, k := range st.Announced {
			if k == key {
				return
			}
		}
		st.Announced = append(st.Announced, key)
		if over := len(st.Announced) - maxAnnouncedReleases; over > 0 {
			st.Announced = st.Announced[over:]
		}
	})
}

// releaseKey identifies an announced update. The updater's configuration is
// live, so the version alone is not enough: repointing the bot at another repo
// or branch, or moving the notify channel, must produce a fresh announcement
// even when the new target happens to publish the same version number.
func releaseKey(cfg *config.UpdaterConfig, version string) string {
	branch := cfg.Branch
	if branch == "" {
		branch = "main"
	}
	return cfg.Repo + "@" + branch + "#" + cfg.NotifyChannel + "@v" + version
}

// sendEmbed posts an embed to a Discord channel via the REST client.
func (m *Manager) sendEmbed(channelID string, e discord.Embed) error {
	if m.rest == nil {
		return fmt.Errorf("discord rest client unavailable")
	}
	id, err := snowflake.Parse(normalizeChannel(channelID))
	if err != nil {
		return fmt.Errorf("invalid notify channel %q: %w", channelID, err)
	}
	_, err = m.rest.CreateMessage(id, discord.MessageCreate{Embeds: []discord.Embed{e}})
	return err
}

// normalizeChannel strips a #channel mention wrapper (<#id>) from a stored
// config value. Config.Set already normalizes on write; this tolerates values
// saved before that (e.g. an older config.yml holding the raw mention).
func normalizeChannel(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<#") && strings.HasSuffix(s, ">") {
		return strings.TrimSuffix(strings.TrimPrefix(s, "<#"), ">")
	}
	return s
}

// gitAuthArgs returns the -c arguments that scope credentials to this single
// git invocation: disable the credential helper so the token never lands in
// .git/config, and inject the token via a per-request Authorization header.
func (m *Manager) gitAuthArgs(cfg *config.UpdaterConfig) []string {
	args := []string{"-c", "credential.helper="}
	if cfg.Token != "" {
		args = append(args, "-c", "http.extraheader="+authHeader(cfg.Token))
	}
	return args
}

// authHeader builds the basic-auth header git accepts for GitHub tokens.
func authHeader(token string) string {
	auth := "x-access-token:" + token
	return "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte(auth))
}

// gitOutput runs git with the bot directory as the working tree.
func (m *Manager) gitOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = m.Dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (m *Manager) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		m.lastError = ""
		return
	}
	m.lastError = err.Error()
}
