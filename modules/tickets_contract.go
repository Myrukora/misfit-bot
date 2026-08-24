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
	// GetTicket returns one ticket incl. its full conversation log
	// (transcript viewer). nil, nil when the ticket does not exist.
	GetTicket(guildID, ticketID string) (*Ticket, error)
	// CloseTicket closes a ticket on behalf of byUserID. This is the SAME
	// code path the in-chat Close button uses; it edits the panel message,
	// archives the thread and updates the index.
	CloseTicket(guildID, ticketID, byUserID string) error
	// ListGroups returns the configured groups for filter UIs / status.
	ListGroups(guildID string) ([]GroupSummary, error)
}

// Ticket is one support ticket: metadata plus the full conversation log.
// Persisted as <DataDir>/tickets/<guildID>/<ticketID>.json.
type Ticket struct {
	ID        string     `json:"id"` // "<group>-<seq>", e.g. "staff-0007"
	Group     string     `json:"group"`
	GuildID   string     `json:"guild_id"`
	ChannelID string     `json:"channel_id"` // thread/channel holding the conversation
	MessageID string     `json:"message_id"` // the embed message carrying Claim/Close buttons
	OpenerID  string     `json:"opener_id"`
	ClaimerID string     `json:"claimer_id"` // "" while unclaimed
	ClaimedAt time.Time  `json:"claimed_at"`
	OpenedAt  time.Time  `json:"opened_at"`
	ClosedAt  time.Time  `json:"closed_at"` // zero while open
	Status    string     `json:"status"`    // "open" | "closed"
	Log       []LogEntry `json:"log"`
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

// Media is one attachment/sticker referenced by a LogEntry. URLs point at
// Discord CDNs; the dashboard CSP explicitly allows those hosts.
type Media struct {
	URL         string `json:"url"`
	ProxyURL    string `json:"proxy_url,omitempty"`
	Kind        string `json:"kind"` // "image" | "video" | "sticker" | "file"
	ContentType string `json:"content_type,omitempty"`
	Filename    string `json:"filename,omitempty"`
	Size        int    `json:"size,omitempty"`
}

// TicketSummary is the lightweight row shape for dashboard lists.
type TicketSummary struct {
	ID        string    `json:"id"`
	Group     string    `json:"group"`
	GuildID   string    `json:"guild_id"`
	OpenerID  string    `json:"opener_id"`
	ClaimerID string    `json:"claimer_id"`
	Status    string    `json:"status"`
	OpenedAt  time.Time `json:"opened_at"`
}

// GroupSummary describes one configured ticket group.
type GroupSummary struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
}
