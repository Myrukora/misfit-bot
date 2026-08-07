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

	"github.com/custombot/bot/config"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
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
type CheckResult struct {
	UpToDate  bool     `json:"up_to_date"`
	Behind    int      `json:"behind"`     // commits behind the remote branch
	NewSHAs   []string `json:"new_shas"`   // abbreviated new SHAs, newest first (max 10)
	LocalSHA  string   `json:"local_sha"`
	RemoteSHA string   `json:"remote_sha"`
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

	mu             sync.Mutex // guards state, lastCheck, lastError
	state          *state
	lastCheck      time.Time
	lastError      string
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
	return m
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
	m.Logger.Info("Updater: %d new commit(s) behind %s — applying update", res.Behind, cfg.Branch)
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
	st := m.loadState()

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
	if err := m.saveState(); err != nil {
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

	// 2. Build the core binary.
	build := exec.CommandContext(ctx, "go", "build", "-o", "bot.new", "./cmd/bot/")
	build.Dir = m.Dir
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	// 3. Rebuild Go plugin sources. Per-plugin failures are warnings only —
	// they must not abort the core update.
	m.rebuildPlugins(ctx)

	// 4. Swap binaries: bot → bot.old, bot.new → bot.
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

// rebuildPlugins rebuilds every Go plugin source directory (modules/<name>/
// containing main.go) into modules/<name>.so, matching the bot's Go version
// by construction.
func (m *Manager) rebuildPlugins(ctx context.Context) {
	modulesDir := filepath.Join(m.Dir, "modules")
	entries, err := os.ReadDir(modulesDir)
	if err != nil {
		m.Logger.Warn("Updater: cannot scan modules dir for plugin rebuild: %v", err)
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mainGo := filepath.Join(modulesDir, e.Name(), "main.go")
		if _, err := os.Stat(mainGo); err != nil {
			continue // lua/python modules don't need a build
		}
		cmd := exec.CommandContext(ctx, "go", "build", "-buildmode=plugin",
			"-o", filepath.Join(modulesDir, e.Name()+".so"), "./modules/"+e.Name()+"/")
		cmd.Dir = m.Dir
		if out, err := cmd.CombinedOutput(); err != nil {
			m.Logger.Warn("Updater: plugin rebuild failed for %s: %v (%s)", e.Name(), err, strings.TrimSpace(string(out)))
		} else {
			m.Logger.Info("Updater: rebuilt plugin %s", e.Name())
		}
	}
}

// Status returns a snapshot of the updater's state for [p]update status.
func (m *Manager) Status() map[string]string {
	cfg := m.getCfg()
	m.mu.Lock()
	lastCheck := m.lastCheck
	lastErr := m.lastError
	m.mu.Unlock()
	st := m.loadState() // locks internally

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
