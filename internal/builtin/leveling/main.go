package leveling

import (
	"errors"
	"math/rand/v2"
	"path/filepath"
	"sync"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

// errNotLoaded is returned by the admin surface when the module has not been
// registered (OnLoad never ran), mirroring the other builtins.
var errNotLoaded = errors.New("leveling module not loaded")

// LevelingModule awards per-guild XP for messages and voice, grants role
// rewards, renders /rank cards and /leaderboard embeds, and replays messages
// missed while the bot was offline. All settings and XP live in the module's
// SQLite database under modules/leveling/.
type LevelingModule struct {
	ctx   *modules.Context
	store *levelStore

	workersStop chan struct{}
	workersDone chan struct{}
	teardown    sync.Once

	mu        sync.Mutex
	lastErr   map[string]string
	cooldown  map[string]map[string]time.Time
	catching  map[string]bool
	catchupOK map[string]bool
	liveBuf   map[string][]bufferedMessage
	sync      map[string]*syncState
	catchup   map[string]*catchupState

	// randInt is injected by tests; production uses math/rand/v2.
	randInt func(n int) int
}

type syncState struct {
	running   bool
	total     int
	processed int
	failed    int
	cursor    int
	lastErr   string
}

type catchupState struct {
	running   bool
	processed int64
	awarded   int64
	truncated bool
	lastErr   string
}

// bufferedMessage is a live message that arrived while catch-up was still
// scanning the guild. It is replayed after catch-up so nothing is counted
// twice and nothing is lost.
type bufferedMessage struct {
	GuildID   string
	ChannelID string
	MessageID snowflake.ID
	UserID    string
	Timestamp time.Time
	RoleIDs   []string
}

// New constructs the module (registered as a compiled-in builtin).
func New() modules.Module { return &LevelingModule{} }

func (m *LevelingModule) Name() string    { return "leveling" }
func (m *LevelingModule) Version() string { return "1.0.0" }
func (m *LevelingModule) Author() string  { return "misfit" }
func (m *LevelingModule) Description() string {
	return "Per-server XP and levels, rank cards, role rewards and offline catch-up"
}

func (m *LevelingModule) Dependencies() []string { return nil }

func (m *LevelingModule) OnLoad(ctx *modules.Context) error {
	m.ctx = ctx
	m.lastErr = map[string]string{}
	m.cooldown = map[string]map[string]time.Time{}
	m.catching = map[string]bool{}
	m.catchupOK = map[string]bool{}
	m.liveBuf = map[string][]bufferedMessage{}
	m.sync = map[string]*syncState{}
	m.catchup = map[string]*catchupState{}
	if m.randInt == nil {
		m.randInt = rand.IntN
	}

	store, err := openStore(filepath.Join(ctx.DataDir, "leveling.db"), m.logError)
	if err != nil {
		return err
	}
	m.store = store

	// Build the catch-up queue BEFORE the message hook and the worker's
	// TouchSeen tick exist: both would otherwise advance last_seen_at past the
	// offline window and make catch-up skip messages sent during downtime.
	queue := m.planCatchup()

	m.ctx.Events.AddGuildMessageCreate(m.onMessageCreate)

	m.workersStop = make(chan struct{})
	m.workersDone = make(chan struct{})
	go m.runWorkers()

	if len(queue) > 0 {
		go m.runCatchupQueue(queue)
	}
	m.logInfo("Leveling module loaded (%d guild(s) catching up)", len(queue))
	return nil
}

// planCatchup decides which guilds need an offline replay. It must run before
// any live event can touch the checkpoints.
func (m *LevelingModule) planCatchup() []string {
	now := time.Now().UTC()
	ids, err := m.store.GuildIDs()
	if err != nil {
		m.logError("Leveling: list guilds: %v", err)
		return nil
	}
	var queue []string
	for _, guildID := range ids {
		cfg, err := m.store.GetConfig(guildID)
		if err != nil {
			m.logError("Leveling: load config for guild %s: %v", guildID, err)
			continue
		}
		if !cfg.Enabled {
			continue
		}
		meta, err := m.store.Meta(guildID)
		if err != nil {
			m.logError("Leveling: read state for guild %s: %v", guildID, err)
			continue
		}
		if !cfg.CatchupEnabled || meta.LastSeenAt.IsZero() {
			// Catch-up is off, or this is the first run: record "caught up as
			// of now" and never backfill pre-enable history.
			if err := m.store.TouchSeen(guildID, now); err != nil {
				m.logError("Leveling: touch last_seen for guild %s: %v", guildID, err)
			}
			m.setCatchupOK(guildID, true)
			continue
		}
		m.setCatching(guildID, true)
		queue = append(queue, guildID)
	}
	return queue
}

func (m *LevelingModule) OnUnload() error {
	m.teardown.Do(func() {
		if m.workersStop != nil {
			close(m.workersStop)
			<-m.workersDone
		}
		m.touchSeenNow()
		if m.store != nil {
			if err := m.store.Close(); err != nil {
				m.logError("Leveling: close database: %v", err)
			}
		}
	})
	return nil
}

// OnDisable implements modules.Disablable: turning the compiled-in module off
// stops its workers and closes the database, exactly like unload.
func (m *LevelingModule) OnDisable() error { return m.OnUnload() }

func (m *LevelingModule) SlashCommands() []commands.SlashCommand { return m.slashCommands() }

func (m *LevelingModule) runWorkers() {
	defer close(m.workersDone)
	minute := time.NewTicker(60 * time.Second)
	defer minute.Stop()
	for {
		select {
		case <-m.workersStop:
			return
		case <-minute.C:
			m.awardVoiceXP()
			m.touchSeenNow()
			m.pruneCooldowns(time.Now().UTC())
		}
	}
}

// pruneCooldowns drops cooldown entries idle for more than a day. Without this
// the per-user map grows with every member who ever spoke in a guild.
func (m *LevelingModule) pruneCooldowns(now time.Time) {
	const maxAge = 24 * time.Hour
	m.mu.Lock()
	defer m.mu.Unlock()
	for guildID, users := range m.cooldown {
		for userID, last := range users {
			if now.Sub(last) > maxAge {
				delete(users, userID)
			}
		}
		if len(users) == 0 {
			delete(m.cooldown, guildID)
		}
	}
}

// touchSeenNow advances last_seen_at only for guilds that are fully caught up:
// advancing it during catch-up would skip the offline window, and advancing it
// after a failed/truncated catch-up would drop that window permanently.
func (m *LevelingModule) touchSeenNow() {
	if m.store == nil {
		return
	}
	ids, err := m.store.GuildIDs()
	if err != nil {
		m.logError("Leveling: list guilds: %v", err)
		return
	}
	now := time.Now().UTC()
	for _, guildID := range ids {
		cfg, err := m.store.GetConfig(guildID)
		if err != nil || !cfg.Enabled {
			continue
		}
		m.mu.Lock()
		ok := m.catchupOK[guildID] && !m.catching[guildID]
		m.mu.Unlock()
		if !ok {
			continue
		}
		if err := m.store.TouchSeen(guildID, now); err != nil {
			m.logError("Leveling: touch last_seen for guild %s: %v", guildID, err)
		}
	}
}

func (m *LevelingModule) logInfo(msg string, args ...any) {
	if m.ctx != nil && m.ctx.Logger != nil {
		m.ctx.Logger.Info(msg, args...)
	}
}

func (m *LevelingModule) logError(msg string, args ...any) {
	if m.ctx != nil && m.ctx.Logger != nil {
		m.ctx.Logger.Error(msg, args...)
	}
}

// ── modules.LevelingAdmin ────────────────────────────────────────────────

var _ modules.LevelingAdmin = (*LevelingModule)(nil)

func (m *LevelingModule) GetGuildConfig(guildID string) (modules.LevelingConfig, error) {
	if m.store == nil {
		return modules.LevelingConfig{}, errNotLoaded
	}
	return m.store.GetConfig(guildID)
}

func (m *LevelingModule) SetGuildConfig(guildID string, cfg modules.LevelingConfig) error {
	if m.store == nil {
		return errNotLoaded
	}
	old, oldErr := m.store.GetConfig(guildID)
	if oldErr != nil {
		// A corrupt config must not block a fix from the dashboard; the
		// enable/disable edges are simply unknown, so treat it as a no-op.
		old = cfg
	}
	if err := m.store.SaveConfig(guildID, cfg); err != nil {
		return err
	}

	enabled := !old.Enabled && cfg.Enabled
	disabled := old.Enabled && !cfg.Enabled
	sameRewardList := sameRewards(old.Rewards, cfg.Rewards)

	m.mu.Lock()
	if enabled || disabled {
		// Enabling starts XP from that moment (no backfill); disabling marks
		// the period as seen so a later enable never replays it.
		m.catchupOK[guildID] = true
		delete(m.catching, guildID)
		delete(m.liveBuf, guildID)
		delete(m.cooldown, guildID)
	}
	if !sameRewardList {
		if st := m.sync[guildID]; st != nil {
			st.cursor = 0
		}
	}
	m.mu.Unlock()

	if enabled || disabled {
		if err := m.store.TouchSeen(guildID, time.Now().UTC()); err != nil {
			m.logError("Leveling: touch last_seen for guild %s: %v", guildID, err)
		}
	}
	return nil
}

func sameRewards(a, b []modules.LevelingReward) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Level != b[i].Level || a[i].RoleID != b[i].RoleID {
			return false
		}
	}
	return true
}

func (m *LevelingModule) Leaderboard(guildID string, offset, limit int) ([]modules.LevelingEntry, int, error) {
	if m.store == nil {
		return nil, 0, errNotLoaded
	}
	maxLevel := m.maxLevelFor(guildID)
	entries, err := m.store.ListEntries(guildID, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	total, err := m.store.CountEntries(guildID)
	if err != nil {
		return nil, 0, err
	}
	if offset < 0 {
		offset = 0
	}
	out := make([]modules.LevelingEntry, 0, len(entries))
	for i, e := range entries {
		out = append(out, entryToView(e, offset+i+1, maxLevel))
	}
	return out, total, nil
}

func (m *LevelingModule) GetEntry(guildID, userID string) (modules.LevelingEntry, bool, error) {
	if m.store == nil {
		return modules.LevelingEntry{}, false, errNotLoaded
	}
	uid, err := canonicalSnowflake(userID, "user")
	if err != nil {
		return modules.LevelingEntry{}, false, err
	}
	entry, ok, err := m.store.GetEntry(guildID, uid)
	if err != nil || !ok {
		return modules.LevelingEntry{}, false, err
	}
	rank, _, err := m.store.EntryRank(guildID, uid)
	if err != nil {
		return modules.LevelingEntry{}, false, err
	}
	return entryToView(entryWithID{UserID: uid, xpEntry: entry}, rank, m.maxLevelFor(guildID)), true, nil
}

func (m *LevelingModule) SetEntryXP(guildID, userID string, xp int64) (modules.LevelingEntry, error) {
	if m.store == nil {
		return modules.LevelingEntry{}, errNotLoaded
	}
	uid, err := canonicalSnowflake(userID, "user")
	if err != nil {
		return modules.LevelingEntry{}, err
	}
	if _, err := m.store.SetXP(guildID, uid, xp); err != nil {
		return modules.LevelingEntry{}, err
	}
	view, _, err := m.GetEntry(guildID, uid)
	if err != nil {
		return modules.LevelingEntry{}, err
	}
	return view, nil
}

func (m *LevelingModule) Stats(guildID string) (modules.LevelingStats, error) {
	if m.store == nil {
		return modules.LevelingStats{}, errNotLoaded
	}
	var stats modules.LevelingStats
	tracked, err := m.store.CountEntries(guildID)
	if err != nil {
		return modules.LevelingStats{}, err
	}
	stats.Tracked = tracked
	if stats.TotalXP, err = m.store.SumXP(guildID); err != nil {
		return modules.LevelingStats{}, err
	}
	if meta, err := m.store.Meta(guildID); err == nil {
		stats.LastSeenAt = meta.LastSeenAt
	}

	m.mu.Lock()
	if cs := m.catchup[guildID]; cs != nil {
		stats.CatchupRunning = cs.running
		stats.CatchupProcessed = cs.processed
		stats.CatchupAwarded = cs.awarded
		stats.CatchupTruncated = cs.truncated
		stats.CatchupError = cs.lastErr
	}
	if m.catching[guildID] {
		stats.CatchupRunning = true
	}
	if ss := m.sync[guildID]; ss != nil {
		stats.SyncRunning = ss.running
		stats.SyncTotal = ss.total
		stats.SyncProcessed = ss.processed
		stats.SyncFailed = ss.failed
		stats.SyncRemaining = ss.total - ss.processed
		stats.SyncError = ss.lastErr
	}
	m.mu.Unlock()
	return stats, nil
}

func (m *LevelingModule) LastError(guildID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr[guildID]
}

func (m *LevelingModule) setLastError(guildID, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastErr == nil {
		m.lastErr = map[string]string{}
	}
	if msg == "" {
		delete(m.lastErr, guildID)
		return
	}
	m.lastErr[guildID] = msg
}

// maxLevelFor resolves the curve cap, falling back to the default when the
// config cannot be read.
func (m *LevelingModule) maxLevelFor(guildID string) int {
	if m.store == nil {
		return defaultConfig().MaxLevel
	}
	cfg, err := m.store.GetConfig(guildID)
	if err != nil || cfg.MaxLevel < 1 {
		return defaultConfig().MaxLevel
	}
	return cfg.MaxLevel
}

func entryToView(e entryWithID, rank, maxLevel int) modules.LevelingEntry {
	level, progress, next := levelForXP(e.XP, maxLevel)
	return modules.LevelingEntry{
		UserID:       e.UserID,
		XP:           e.XP,
		Level:        level,
		Rank:         rank,
		Progress:     progress,
		NextLevelXP:  next,
		Messages:     e.Messages,
		VoiceSeconds: e.VoiceSeconds,
		TrackedRoles: append([]string(nil), e.TrackedRoles...),
		UpdatedAt:    e.UpdatedAt,
	}
}
