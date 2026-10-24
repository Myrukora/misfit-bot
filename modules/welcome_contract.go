package modules

// WelcomeAdmin is the OPTIONAL interface the welcome module implements so the
// dashboard can manage per-guild welcome/leave messages without importing the
// module's package (same resolution pattern as TicketProvider and
// ImageFilterAdmin):
//
//	if mod, ok := manager.Get("welcome"); ok {
//	    if adm, ok := mod.(WelcomeAdmin); ok { ... }
//	}
//
// The dashboard is this module's only configuration path; the module itself
// registers guild member join/leave event hooks.
type WelcomeAdmin interface {
	// GetGuildConfig returns the guild's effective config (defaults when no
	// file exists yet).
	GetGuildConfig(guildID string) (WelcomeConfig, error)
	// SetGuildConfig validates and persists the guild's config. It returns a
	// human-readable error when a message/embed exceeds a Discord limit.
	SetGuildConfig(guildID string, cfg WelcomeConfig) error
	// SendTest posts a sample join or leave message to the event's configured
	// channel, using actorID as the sample member. event is "join" or "leave".
	SendTest(guildID, event, actorID string) error
	// LastError reports the most recent delivery failure for one event
	// ("join"/"leave"), or "" when the last delivery succeeded. In-memory
	// only (reset on restart) — surfaced on the dashboard for debugging.
	LastError(guildID, event string) string
}

// WelcomeConfig is one guild's welcome/leave configuration.
type WelcomeConfig struct {
	Join  WelcomeEvent `json:"join" yaml:"join"`
	Leave WelcomeEvent `json:"leave" yaml:"leave"`
}

// WelcomeEvent configures one event (member join or member leave).
type WelcomeEvent struct {
	Enabled   bool   `json:"enabled" yaml:"enabled"`
	ChannelID string `json:"channel_id" yaml:"channel_id"`
	Message   string `json:"message" yaml:"message"`
	// AutoDelete removes the posted message after N seconds (0 = keep it).
	AutoDelete int          `json:"auto_delete" yaml:"auto_delete"`
	Embed      WelcomeEmbed `json:"embed" yaml:"embed"`
}

// WelcomeEmbed is the dashboard-built embed. All text fields support the same
// template variables as Message. An empty/disabled embed is not sent.
type WelcomeEmbed struct {
	Enabled      bool                `json:"enabled" yaml:"enabled"`
	Color        string              `json:"color" yaml:"color"` // "#rrggbb"; empty = no accent
	Author       WelcomeEmbedAuthor  `json:"author" yaml:"author"`
	Title        string              `json:"title" yaml:"title"`
	TitleURL     string              `json:"title_url" yaml:"title_url"`
	Description  string              `json:"description" yaml:"description"`
	Fields       []WelcomeEmbedField `json:"fields" yaml:"fields"`
	ImageURL     string              `json:"image_url" yaml:"image_url"`
	ThumbnailURL string              `json:"thumbnail_url" yaml:"thumbnail_url"`
	Footer       string              `json:"footer" yaml:"footer"`
	FooterIcon   string              `json:"footer_icon" yaml:"footer_icon"`
	// Timestamp is an RFC3339 time rendered as the embed timestamp, or "".
	Timestamp string `json:"timestamp" yaml:"timestamp"`
}

// WelcomeEmbedAuthor is the embed author block.
type WelcomeEmbedAuthor struct {
	Name    string `json:"name" yaml:"name"`
	URL     string `json:"url" yaml:"url"`
	IconURL string `json:"icon_url" yaml:"icon_url"`
}

// WelcomeEmbedField is one embed field.
type WelcomeEmbedField struct {
	Name   string `json:"name" yaml:"name"`
	Value  string `json:"value" yaml:"value"`
	Inline bool   `json:"inline" yaml:"inline"`
}

// Welcome event identifiers used by WelcomeAdmin.
const (
	WelcomeEventJoin  = "join"
	WelcomeEventLeave = "leave"
)
