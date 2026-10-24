package modules

import "time"

// LevelingAdmin is the OPTIONAL interface the leveling builtin implements so
// the dashboard can manage it without importing the module's package (same
// resolution pattern as ModerationAdmin / TicketProvider / ImageFilterAdmin).
type LevelingAdmin interface {
	// GetGuildConfig returns the guild's effective leveling config.
	GetGuildConfig(guildID string) (LevelingConfig, error)
	// SetGuildConfig validates and persists the guild's config.
	SetGuildConfig(guildID string, cfg LevelingConfig) error
	// Leaderboard returns ranked entries and the total number of tracked
	// members. offset 0 is the top of the board.
	Leaderboard(guildID string, offset, limit int) ([]LevelingEntry, int, error)
	// GetEntry returns one member's entry; ok is false when untracked.
	GetEntry(guildID, userID string) (LevelingEntry, bool, error)
	// SetEntryXP overwrites a member's total XP.
	SetEntryXP(guildID, userID string, xp int64) (LevelingEntry, error)
	// ResetEntry deletes a member's entry and removes granted reward roles.
	ResetEntry(guildID, userID string) error
	// Stats aggregates tracked members, XP totals and worker progress.
	Stats(guildID string) (LevelingStats, error)
	// StartRewardSync backfills reward roles for the guild's members and
	// returns the number of candidates queued in this run.
	StartRewardSync(guildID string) (int, error)
	// LastError reports the most recent REST/state failure for a guild
	// ("" when the last operation succeeded). In-memory only.
	LastError(guildID string) string
}

// LevelingConfig is one guild's leveling settings. Every field is editable on
// the dashboard; XP state lives separately under xp/<guildID>.json.
type LevelingConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
	// MessageEnabled awards XP for messages in non-ignored channels.
	MessageEnabled bool `json:"message_enabled" yaml:"message_enabled"`
	// MessageXPMin/MessageXPMax bound the random award per message.
	MessageXPMin int `json:"message_xp_min" yaml:"message_xp_min"`
	MessageXPMax int `json:"message_xp_max" yaml:"message_xp_max"`
	// MessageCooldownSeconds is the per-member wait between message awards.
	MessageCooldownSeconds int `json:"message_cooldown_seconds" yaml:"message_cooldown_seconds"`
	// VoiceEnabled awards XP per minute spent in a tracked voice channel.
	VoiceEnabled bool `json:"voice_enabled" yaml:"voice_enabled"`
	// VoiceXPPerMinute is awarded once per 60s tick.
	VoiceXPPerMinute int `json:"voice_xp_per_minute" yaml:"voice_xp_per_minute"`
	// VoiceRequireOthers requires a second eligible member in the channel.
	VoiceRequireOthers bool `json:"voice_require_others" yaml:"voice_require_others"`
	// MaxLevel caps the curve; XP past it no longer levels the member.
	MaxLevel int `json:"max_level" yaml:"max_level"`
	// AnnounceEnabled posts a level-up message to AnnounceChannel. An empty
	// AnnounceChannel means silent (there is no fallback channel).
	AnnounceEnabled bool   `json:"announce_enabled" yaml:"announce_enabled"`
	AnnounceChannel string `json:"announce_channel" yaml:"announce_channel"`
	AnnounceMessage string `json:"announce_message" yaml:"announce_message"`
	// Rewards are per-level role grants; they stack and are only removed on a
	// dashboard XP reset.
	Rewards []LevelingReward `json:"rewards" yaml:"rewards"`
	// IgnoredChannels/IgnoredRoles earn no XP and add no messages.
	IgnoredChannels []string `json:"ignored_channels" yaml:"ignored_channels"`
	IgnoredRoles    []string `json:"ignored_roles" yaml:"ignored_roles"`
	// CatchupEnabled replays messages missed while the bot was offline.
	CatchupEnabled bool `json:"catchup_enabled" yaml:"catchup_enabled"`
	// CatchupMaxMessages caps the replay per channel.
	CatchupMaxMessages int `json:"catchup_max_messages" yaml:"catchup_max_messages"`
	// CatchupMaxChannels caps how many channels are scanned.
	CatchupMaxChannels int `json:"catchup_max_channels" yaml:"catchup_max_channels"`
}

// LevelingReward grants RoleID when a member reaches Level.
type LevelingReward struct {
	Level  int    `json:"level" yaml:"level"`
	RoleID string `json:"role_id" yaml:"role_id"`
}

// LevelingEntry is one member's leveling row with computed curve fields.
type LevelingEntry struct {
	UserID       string    `json:"user_id"`
	XP           int64     `json:"xp"`
	Level        int       `json:"level"`
	Rank         int       `json:"rank"` // 0 = unranked
	Progress     int64     `json:"progress"`
	NextLevelXP  int64     `json:"next_level_xp"` // 0 at max level
	Messages     int64     `json:"messages"`
	VoiceSeconds int64     `json:"voice_seconds"`
	TrackedRoles []string  `json:"tracked_roles,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// LevelingStats is the dashboard's aggregate view for a guild.
type LevelingStats struct {
	Tracked          int       `json:"tracked"`
	TotalXP          int64     `json:"total_xp"`
	LastSeenAt       time.Time `json:"last_seen_at"`
	CatchupRunning   bool      `json:"catchup_running"`
	CatchupProcessed int64     `json:"catchup_processed"`
	CatchupAwarded   int64     `json:"catchup_awarded"`
	CatchupTruncated bool      `json:"catchup_truncated"`
	CatchupError     string    `json:"catchup_error,omitempty"`
	SyncRunning      bool      `json:"sync_running"`
	SyncTotal        int       `json:"sync_total"`
	SyncProcessed    int       `json:"sync_processed"`
	SyncFailed       int       `json:"sync_failed"`
	SyncRemaining    int       `json:"sync_remaining"`
	SyncError        string    `json:"sync_error,omitempty"`
}
