package modules

import "time"

// ModerationAdmin is the OPTIONAL interface the moderation builtin implements
// so the dashboard can manage it without importing the module's package (same
// resolution pattern as TicketProvider / ImageFilterAdmin / WelcomeAdmin).
type ModerationAdmin interface {
	// GetGuildConfig returns the guild's effective moderation config.
	GetGuildConfig(guildID string) (ModerationConfig, error)
	// SetGuildConfig validates and persists the guild's config.
	SetGuildConfig(guildID string, cfg ModerationConfig) error
	// ListCases returns cases newest-first, filtered and limited.
	ListCases(guildID string, filter ModerationCaseFilter) ([]ModerationCase, error)
	// RemoveWarning marks one warning case removed (dashboard "unwarn").
	RemoveWarning(guildID string, caseID int, byID, byTag, reason string) error
	// LastError reports the most recent audit/notification delivery failure
	// for a guild ("" when the last post succeeded). In-memory only.
	LastError(guildID string) string
}

// ModerationConfig is one guild's moderation settings.
type ModerationConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
	// AuditChannel receives one embed per moderation action (channel or thread).
	AuditChannel string `json:"audit_channel" yaml:"audit_channel"`
	// WarnChannel receives warn notifications (channel or thread). Empty falls
	// back to AuditChannel.
	WarnChannel string `json:"warn_channel" yaml:"warn_channel"`
	// WarnNotify posts a warn notification to WarnChannel.
	WarnNotify bool `json:"warn_notify" yaml:"warn_notify"`
	// WarnExpiryDays expires warnings after N days (0 = never).
	WarnExpiryDays int `json:"warn_expiry_days" yaml:"warn_expiry_days"`
	// BanDeleteSeconds is how much recent message history a ban deletes
	// (0..604800, Discord's maximum).
	BanDeleteSeconds int `json:"ban_delete_seconds" yaml:"ban_delete_seconds"`
	// Rules escalate automatically when a user reaches a warning threshold.
	Rules []ModerationRule `json:"rules" yaml:"rules"`
}

// ModerationRule is one automatic escalation threshold.
type ModerationRule struct {
	Threshold       int    `json:"threshold" yaml:"threshold"`
	Action          string `json:"action" yaml:"action"` // timeout | kick | ban
	DurationMinutes int    `json:"duration_minutes" yaml:"duration_minutes"`
}

// ModerationCase is one audit-log entry.
type ModerationCase struct {
	ID            int        `json:"id" yaml:"id"`
	GuildID       string     `json:"guild_id" yaml:"guild_id"`
	Type          string     `json:"type" yaml:"type"` // warn | ban | kick | timeout | unban
	TargetID      string     `json:"target_id" yaml:"target_id"`
	TargetTag     string     `json:"target_tag" yaml:"target_tag"`
	ModeratorID   string     `json:"moderator_id" yaml:"moderator_id"`
	ModeratorTag  string     `json:"moderator_tag" yaml:"moderator_tag"`
	Reason        string     `json:"reason" yaml:"reason"`
	CreatedAt     time.Time  `json:"created_at" yaml:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
	Removed       bool       `json:"removed" yaml:"removed"`
	RemovedAt     *time.Time `json:"removed_at,omitempty" yaml:"removed_at,omitempty"`
	RemovedByID   string     `json:"removed_by_id,omitempty" yaml:"removed_by_id,omitempty"`
	RemovedByTag  string     `json:"removed_by_tag,omitempty" yaml:"removed_by_tag,omitempty"`
	RemovedReason string     `json:"removed_reason,omitempty" yaml:"removed_reason,omitempty"`
	// Automated marks an action taken by a warn rule rather than a moderator.
	Automated     bool `json:"automated" yaml:"automated"`
	RuleThreshold int  `json:"rule_threshold,omitempty" yaml:"rule_threshold,omitempty"`
	WarningID     int  `json:"warning_id,omitempty" yaml:"warning_id,omitempty"`
}

// ModerationCaseFilter selects cases for the dashboard.
type ModerationCaseFilter struct {
	Type   string
	UserID string
	Limit  int
}

// Moderation case types.
const (
	ModerationTypeWarn    = "warn"
	ModerationTypeBan     = "ban"
	ModerationTypeKick    = "kick"
	ModerationTypeTimeout = "timeout"
	ModerationTypeUnban   = "unban"
)

// Moderation rule actions.
const (
	ModerationActionTimeout = "timeout"
	ModerationActionKick    = "kick"
	ModerationActionBan     = "ban"
)
