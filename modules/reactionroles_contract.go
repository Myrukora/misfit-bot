package modules

// ReactionRolesAdmin is the OPTIONAL interface the reaction-roles builtin
// implements so the dashboard can manage panels without importing the module
// (same resolution pattern as the other builtin contracts).
type ReactionRolesAdmin interface {
	// GetGuildConfig returns all panels for a guild.
	GetGuildConfig(guildID string) (ReactionRolesConfig, error)
	// CreatePanel assigns an ID, validates and persists a new panel.
	CreatePanel(guildID string, panel ReactionPanel) (ReactionPanel, error)
	// UpdatePanel replaces a panel's editable fields, preserving its posted
	// message linkage (MessageID/PostedChannelID/PostedEmojis).
	UpdatePanel(guildID string, panel ReactionPanel) (ReactionPanel, error)
	// DeletePanel removes a panel and optionally its posted Discord message.
	DeletePanel(guildID string, panelID int, deleteMessage bool) error
	// PostPanel posts or updates the panel's embed and reactions.
	PostPanel(guildID string, panelID int) error
	// LastError reports the last role-grant or panel-post failure ("" = ok).
	LastError(guildID string) string
}

// ReactionRolesConfig is one guild's set of panels.
type ReactionRolesConfig struct {
	NextID int             `json:"next_id" yaml:"next_id"`
	Panels []ReactionPanel `json:"panels" yaml:"panels"`
}

// ReactionPanel is one embed + reaction→role mapping set.
type ReactionPanel struct {
	ID      int    `json:"id" yaml:"id"`
	Name    string `json:"name" yaml:"name"`
	Enabled bool   `json:"enabled" yaml:"enabled"`
	// ChannelID is where the panel is posted.
	ChannelID string `json:"channel_id" yaml:"channel_id"`
	// RemoveOnUnreact removes the role when the member removes the reaction.
	RemoveOnUnreact bool                  `json:"remove_on_unreact" yaml:"remove_on_unreact"`
	Embed           ReactionRoleEmbed     `json:"embed" yaml:"embed"`
	Mappings        []ReactionRoleMapping `json:"mappings" yaml:"mappings"`

	// Posted state — owned by the module; preserved across dashboard edits.
	MessageID       string   `json:"message_id,omitempty" yaml:"message_id,omitempty"`
	PostedChannelID string   `json:"posted_channel_id,omitempty" yaml:"posted_channel_id,omitempty"`
	PostedEmojis    []string `json:"posted_emojis,omitempty" yaml:"posted_emojis,omitempty"`
}

// ReactionRoleEmbed is the panel's embed content.
type ReactionRoleEmbed struct {
	Title        string `json:"title" yaml:"title"`
	Description  string `json:"description" yaml:"description"`
	Color        string `json:"color" yaml:"color"` // "#rrggbb"; empty = no accent
	Footer       string `json:"footer" yaml:"footer"`
	ImageURL     string `json:"image_url" yaml:"image_url"`
	ThumbnailURL string `json:"thumbnail_url" yaml:"thumbnail_url"`
}

// ReactionRoleMapping binds one emoji to one role. Emoji is the canonical
// Discord reaction string: the Unicode character(s) or "name:id" for a custom
// server emoji.
type ReactionRoleMapping struct {
	Emoji  string `json:"emoji" yaml:"emoji"`
	RoleID string `json:"role_id" yaml:"role_id"`
}
