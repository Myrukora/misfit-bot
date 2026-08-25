package modules

import "time"

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
	// CloseTicket closes a ticket on behalf of byUserID: locks the channel,
	// builds the HTML transcript, posts it to the log channel and stores it
	// with all attachments under the ticket's data folder. Same code path as
	// the in-chat [p]close command and Close button.
	CloseTicket(guildID, ticketID, byUserID string) error
	// ListTypes returns configured ticket types (dashboard editors).
	ListTypes(guildID string) ([]TypeSummary, error)
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
	ChannelID      string     `json:"channel_id"` // private text channel holding the conversation
	MessageID      string     `json:"message_id"` // in-channel embed carrying Claim/Close buttons
	PanelName      string     `json:"panel_name,omitempty"`
	OpenerID       string     `json:"opener_id"`
	ClaimerID      string     `json:"claimer_id"` // "" while unclaimed
	ClaimedAt      time.Time  `json:"claimed_at"`
	OpenedAt       time.Time  `json:"opened_at"`
	ClosedAt       time.Time  `json:"closed_at"`                 // zero while open
	Status         string     `json:"status"`                    // "open" | "closed"
	Members        []string   `json:"members,omitempty"`         // extra members added via [p]add
	TranscriptPath string     `json:"transcript_path,omitempty"` // relative to DataDir; set after close
	Log            []LogEntry `json:"log"`
}

// Type returns the effective type key (v1 files only carry Group).
func (t *Ticket) EffectiveType() string {
	if t.Type != "" {
		return t.Type
	}
	return t.Group
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
	Kind        string `json:"kind"` // "image" | "video" | "audio" | "sticker" | "file"
	ContentType string `json:"content_type,omitempty"`
	Filename    string `json:"filename,omitempty"`
	Size        int    `json:"size,omitempty"`
}

// TicketSummary is the lightweight row shape for dashboard lists.
type TicketSummary struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Group     string    `json:"group"` // legacy alias filled from Type
	GuildID   string    `json:"guild_id"`
	OpenerID  string    `json:"opener_id"`
	ClaimerID string    `json:"claimer_id"`
	Status    string    `json:"status"`
	OpenedAt  time.Time `json:"opened_at"`
	ClosedAt  time.Time `json:"closed_at"`
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
