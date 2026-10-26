package modules

import (
	"strings"
	"time"
)

// TicketProvider is the OPTIONAL interface a ticket module implements so the
// dashboard can list, view and close tickets without importing the module's
// package (a dashboard plugin physically CANNOT import another plugin's
// package — shared types therefore live here in core).
//
// The dashboard resolves a provider at request time:
//
//	if mod, ok := manager.Get("tickets"); ok {
//	    if tp, ok := mod.(TicketProvider); ok { ... }
//	}
//
// If either step fails, the dashboard shows a "module not installed" stub.
type TicketProvider interface {
	// ListOpenTickets returns all open tickets for a guild (dashboard list).
	ListOpenTickets(guildID string) ([]TicketSummary, error)
	// ListClosedTickets returns closed tickets newest-first for the archive UI.
	ListClosedTickets(guildID string) ([]TicketSummary, error)
	// GetTicket returns one ticket incl. its full conversation log
	// (transcript viewer). nil, nil when the ticket does not exist.
	GetTicket(guildID, ticketID string) (*Ticket, error)
	// ListTypes returns configured ticket types (dashboard editors).
	ListTypes(guildID string) ([]TypeSummary, error)
	// TicketFilePath returns the absolute path of one mirrored media file of a
	// ticket, after validating the ticket ID and the file name (no separators,
	// no "..", must resolve inside the ticket's files directory). Used by the
	// dashboard's /api/ticketfiles handler; returns an error when the guild,
	// ticket, name or file is invalid/absent.
	TicketFilePath(guildID, ticketID, name string) (string, error)
}

// Ticket is one support ticket: metadata plus the full conversation log.
// Persisted as <DataDir>/tickets/<guildID>/<ticketID>.json; attachments are
// mirrored into <DataDir>/tickets/<guildID>/<ticketID>/files/ at close so
// transcripts survive Discord CDN expiry.
type Ticket struct {
	ID             string     `json:"id"` // "<type>-<seq>", e.g. "staff-0007"
	Type           string     `json:"type"`
	Group          string     `json:"group"` // legacy alias of Type (v1 stores)
	GuildID        string     `json:"guild_id"`
	ChannelID      string     `json:"channel_id"`             // private text channel holding the conversation
	ChannelName    string     `json:"channel_name,omitempty"` // stored at creation for collision detection
	MessageID      string     `json:"message_id"`             // in-channel embed carrying the Close button
	PanelName      string     `json:"panel_name,omitempty"`
	OpenerID       string     `json:"opener_id"`
	OpenedAt       time.Time  `json:"opened_at"`
	ClosedAt       time.Time  `json:"closed_at"`                 // zero while open
	Status         string     `json:"status"`                    // "open" | "closed"
	Members        []string   `json:"members,omitempty"`         // extra members added via /add
	TranscriptPath string     `json:"transcript_path,omitempty"` // relative to DataDir; set after close
	CloseReason    string     `json:"close_reason,omitempty"`    // "user" | "channel_deleted"
	Log            []LogEntry `json:"log"`

	// Archive maintenance. RemovedMessages is the tombstone list for messages
	// deleted from a closed archive: the merge path must not resurrect one
	// from a late close-tail history fetch. ArchiveAudit records WHO removed
	// messages, when, and why — never the removed content.
	RemovedMessages []string             `json:"removed_messages,omitempty"`
	ArchiveAudit    []TicketArchiveEvent `json:"archive_audit,omitempty"`
}

// TicketArchiveEvent is one durable archive-maintenance audit entry. It
// intentionally stores message IDs + reason only; the deleted message content
// and media are never retained here.
type TicketArchiveEvent struct {
	Time       time.Time `json:"time"`
	ActorID    string    `json:"actor_id"`
	ActorName  string    `json:"actor_name,omitempty"`
	Action     string    `json:"action"` // "messages_deleted"
	MessageIDs []string  `json:"message_ids,omitempty"`
	Count      int       `json:"count"`
	Reason     string    `json:"reason"`
}

// Type returns the effective type key (v1 files only carry Group).
func (t *Ticket) EffectiveType() string {
	if t.Type != "" {
		return t.Type
	}
	return t.Group
}

// VisibleLog returns the conversation entries shown to users: the ticket's
// own opening message (pings + embed) and the synthetic close notice (its
// system-close-* message ID is the marker) are system noise, not conversation.
// The text is deliberately NOT matched: a user writing "_Ticket closed._" is
// conversation and must stay visible.
func (t *Ticket) VisibleLog() []LogEntry {
	out := make([]LogEntry, 0, len(t.Log))
	for _, e := range t.Log {
		if t.MessageID != "" && e.MsgID == t.MessageID {
			continue
		}
		if strings.HasPrefix(e.MsgID, "system-close-") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// LogCount returns the number of visible conversation entries.
func (t *Ticket) LogCount() int {
	return len(t.VisibleLog())
}

// LogEntry is one conversation event inside a ticket. Edits overwrite Content
// and set Edited; deletes keep the entry as an honest tombstone via Deleted.
type LogEntry struct {
	MsgID       string    `json:"msg_id"`
	AuthorID    string    `json:"author_id"`
	AuthorName  string    `json:"author_name"`
	IsBot       bool      `json:"is_bot,omitempty"`
	Timestamp   time.Time `json:"ts"`
	Content     string    `json:"content"`
	Attachments []Media   `json:"attachments,omitempty"`
	Embeds      []Media   `json:"embeds,omitempty"` // message embeds: GIF picker results, link previews
	Stickers    []Media   `json:"stickers,omitempty"`
	Edited      bool      `json:"edited,omitempty"`
	Deleted     bool      `json:"deleted,omitempty"`
}

// Media is one attachment/sticker referenced by a LogEntry. URL points at the
// Discord CDN; LocalPath (set once the transcript pipeline mirrors the file)
// is a path RELATIVE to the module DataDir served by the dashboard.
type Media struct {
	URL         string `json:"url"`
	LocalPath   string `json:"local_path,omitempty"` // relative to DataDir after close-mirror
	ProxyURL    string `json:"proxy_url,omitempty"`
	Kind        string `json:"kind"` // "image" | "video" | "audio" | "sticker" | "file" | "link"
	ContentType string `json:"content_type,omitempty"`
	Filename    string `json:"filename,omitempty"`
	Size        int    `json:"size,omitempty"`
}

// TicketSummary is the lightweight row shape for dashboard lists. Preview
// carries the newest playable media (image/video/audio) for the open-ticket
// cards; it is additive and safe for older API consumers.
type TicketSummary struct {
	ID       string    `json:"id"`
	Type     string    `json:"type"`
	Group    string    `json:"group"` // legacy alias filled from Type
	GuildID  string    `json:"guild_id"`
	OpenerID string    `json:"opener_id"`
	Status   string    `json:"status"`
	OpenedAt time.Time `json:"opened_at"`
	ClosedAt time.Time `json:"closed_at"`
	Preview  []Media   `json:"preview,omitempty"`
}

// TypeSummary describes one configured ticket type (v2 replacement of
// GroupSummary; GroupSummary kept below for v1 API compatibility).
type TypeSummary struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description,omitempty"`
	ButtonLabel string `json:"button_label,omitempty"`
	ButtonEmoji string `json:"button_emoji,omitempty"`
	Color       int    `json:"color,omitempty"`
}

// GroupSummary is the v1 group shape — still produced by ListGroups-style
// helpers so older dashboard builds keep rendering. Deprecated.
type GroupSummary struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
}

// TicketAdmin is the OPTIONAL interface the tickets module implements for the
// dashboard's per-guild management surface. The dashboard is the ONLY place
// ticket types, panels and per-guild settings are managed; the in-channel
// commands are limited to end-user actions (close/rename/add/remove).
//
// The dashboard resolves the admin at request time (same pattern as
// TicketProvider / ImageFilterAdmin):
//
//	if mod, ok := manager.Get("tickets"); ok {
//	    if adm, ok := mod.(TicketAdmin); ok { ... }
//	}
//
// A module that does not implement it simply has no management surface: the
// types/panels routes answer 404 and the page renders a stub.
type TicketAdmin interface {
	// Panels.
	ListPanels(guildID string) ([]PanelSummary, error)
	GetPanel(guildID, name string) (PanelSummary, error)
	CreatePanel(guildID string, in PanelInput) (PanelSummary, error)
	// UpdatePanel replaces the panel's fields and question form. The name is
	// the identity and cannot change; a channel change reposts the panel.
	UpdatePanel(guildID, name string, in PanelInput) (PanelSummary, error)
	DeletePanel(guildID, name string) error
	// PanelAction performs suspend | resume | resend on one panel.
	PanelAction(guildID, name, action string) (PanelSummary, error)

	// Types.
	ListTypeDetails(guildID string) ([]TypeDetail, error)
	GetType(guildID, key string) (TypeDetail, error)
	CreateType(guildID string, in TypeDetail) (TypeDetail, error)
	// UpdateType replaces the type's fields; the key is immutable.
	UpdateType(guildID, key string, in TypeDetail) (TypeDetail, error)
	DeleteType(guildID, key string) error

	// Per-guild settings (the transcript log channel).
	GetGuildSettings(guildID string) (TicketGuildSettings, error)
	SetGuildSettings(guildID string, in TicketGuildSettings) error

	// Archive maintenance. Both methods operate on CLOSED tickets only.
	// DeleteArchivedTicket removes the archived ticket (JSON, transcript,
	// mirrored media). DeleteArchivedMessages removes selected messages from
	// one archived ticket and requires a non-empty reason, which is written to
	// the durable TicketArchiveEvent audit (message content is not retained).
	DeleteArchivedTicket(guildID, ticketID, byUserID string) error
	DeleteArchivedMessages(guildID, ticketID string, msgIDs []string, byUserID, byUserName, reason string) (int, error)
}

// TicketGuildSettings is the per-guild ticket configuration the dashboard
// edits. Global settings (retention, modals, dashboard close) stay on the
// module's WebConfigurable panel.
type TicketGuildSettings struct {
	LogChannel string `json:"log_channel"` // "" = don't post transcripts to Discord
}

// TypeDetail is the full ticket-type shape the dashboard editor reads and
// writes. Key is immutable on update (the path key wins). Enabled + Category
// are validated together: a type cannot be enabled without a category.
type TypeDetail struct {
	Key            string   `json:"key"`
	Label          string   `json:"label"`
	Enabled        bool     `json:"enabled"`
	Category       string   `json:"category"`
	PingRoles      []string `json:"ping_roles"`
	HelperRoles    []string `json:"helper_roles"`
	AccessRoles    []string `json:"access_roles"`
	ChannelName    string   `json:"channel_name"`
	WelcomeMessage string   `json:"welcome_message"`
	EmbedBody      string   `json:"embed_body"`
	ButtonLabel    string   `json:"button_label"`
	ButtonEmoji    string   `json:"button_emoji"`
	Color          int      `json:"color"`
	AllowClose     bool     `json:"allow_close"`
}

// PanelInput is the write shape for creating/updating a panel. Name is the
// identity (immutable on update); Questions replace the open-time modal form.
type PanelInput struct {
	Name        string          `json:"name"`
	ChannelID   string          `json:"channel_id"`
	TypeKey     string          `json:"type"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	ModalTitle  string          `json:"modal_title"`
	Questions   []PanelQuestion `json:"questions"`
}

// PanelSummary is one posted panel as the dashboard renders it.
type PanelSummary struct {
	Name        string          `json:"name"`
	TypeKey     string          `json:"type"`
	ChannelID   string          `json:"channel_id"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	ModalTitle  string          `json:"modal_title,omitempty"`
	Suspended   bool            `json:"suspended"`
	Questions   []PanelQuestion `json:"questions"`
}

// PanelQuestion is one field of the open-time modal.
type PanelQuestion struct {
	Label       string `json:"label"`                 // 1..45 chars
	Placeholder string `json:"placeholder,omitempty"` // <=100 chars
	Style       string `json:"style"`                 // "short" | "paragraph"
	Required    bool   `json:"required"`
	Value       string `json:"value,omitempty"` // prefill, <=4000 chars
}

// TicketTranscript is the OPTIONAL interface a ticket module implements so
// the dashboard can regenerate a ticket's HTML transcript from the stored
// log on demand. The on-disk transcript is a CACHE of the stored log: it is
// written at close time and rebuilt whenever it is requested, so a message
// or edit that lands after the close tail can never leave a stale artifact.
//
// Resolution mirrors TicketProvider/TicketAdmin (manager.Get("tickets") +
// type assertion). A module that does not implement it has no transcript
// download: the route answers 404.
type TicketTranscript interface {
	// RefreshTranscript rebuilds the ticket's HTML transcript from its
	// CURRENT stored log, rewrites <DataDir>/tickets/<guildID>/<ticketID>.html,
	// and returns the rendered bytes. It errors when the module is not
	// loaded, an ID is invalid, or the ticket does not exist.
	RefreshTranscript(guildID, ticketID string) ([]byte, error)
}
