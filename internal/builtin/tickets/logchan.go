package tickets

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// Log-channel record colors: opened is neutral blurple, closed is Discord
// green, matching the status wording the dashboard uses.
const (
	logColorOpened = 0x5865F2
	logColorClosed = 0x57F287
)

// postLogEmbed posts one embed to the guild's log channel and returns the new
// message's ID. Callers that replace the post later (the opened record) store
// that ID on the ticket. "" means no log channel is configured or the post
// failed.
func (m *TicketsModule) postLogEmbed(guildID string, embed discord.Embed) string {
	cfg := m.guildConfig(guildID)
	m.mu.RLock()
	logCh := cfg.LogChannel
	m.mu.RUnlock()
	if logCh == "" {
		return ""
	}
	chID, err := snowflake.Parse(logCh)
	if err != nil {
		return ""
	}
	msg, err := m.ctx.Rest.CreateMessage(chID, discord.MessageCreate{Embeds: []discord.Embed{embed}})
	if err != nil {
		m.ctx.Logger.Warn("Tickets: failed to post log event for %s: %v", guildID, err)
		return ""
	}
	if msg == nil {
		return ""
	}
	return msg.ID.String()
}

// postLogEvent posts a plain title/description notice (archive maintenance
// events). Fire-and-forget: the message ID is discarded.
func (m *TicketsModule) postLogEvent(guildID, title, desc string, color int) {
	m.postLogEmbed(guildID, discord.NewEmbed().WithTitle(title).WithDescription(desc).WithColor(color))
}

// lookupUser resolves a user ID for display in a log record. Failures (and a
// nil REST client) yield the zero user, so the record falls back to a mention
// instead of blocking the close.
func (m *TicketsModule) lookupUser(userID string) discord.User {
	uid, err := snowflake.Parse(strings.TrimSpace(userID))
	if err != nil || m.ctx.Rest == nil {
		return discord.User{}
	}
	u, err := m.ctx.Rest.GetUser(uid)
	if err != nil || u == nil {
		return discord.User{}
	}
	return *u
}

// ── record embeds (pure; shared with tests) ───────────────────────────────

// mentionOrID renders a user reference the way the log records want it: a
// mention when the value is a real snowflake (Discord resolves it to the
// username on its own, no intents needed), the raw value otherwise, and
// "unknown" when there is nothing at all. The raw-ID branch is the same
// fallback the previous bot hit when it could not resolve members.
func mentionOrID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "unknown"
	}
	if _, err := snowflake.Parse(id); err != nil {
		return id
	}
	return "<@" + id + ">"
}

// logCreatorLine is the first line of both records: "Ticket created by X
// (`staff-0001`)". A resolved opener is named in bold, otherwise the mention
// (or the raw id) keeps the line from going anonymous.
func logCreatorLine(tk *modules.Ticket, opener discord.User) string {
	name := ""
	if opener.ID != 0 {
		name = strings.TrimSpace(opener.EffectiveName())
	}
	if name == "" {
		name = mentionOrID(tk.OpenerID)
	} else {
		name = "**" + name + "**"
	}
	return fmt.Sprintf("Ticket created by %s (`%s`)", name, tk.ID)
}

// logPanelLine names the panel that produced the ticket, falling back to the
// type key so tickets posted before panels existed still say what they are.
func logPanelLine(tk *modules.Ticket, g TypeConfig) string {
	panel := tk.PanelName
	if panel == "" {
		panel = g.Key
	}
	if panel == "" {
		panel = tk.EffectiveType()
	}
	return "**Panel:** `" + panel + "`"
}

// discordTime renders a Discord timestamp token. Style "F" is the long form
// ("Saturday, November 14, 2026 at 1:00 PM") the previous bot's logs showed.
func discordTime(t time.Time, style string) string {
	if t.IsZero() {
		return "unknown"
	}
	return fmt.Sprintf("<t:%d:%s>", t.Unix(), style)
}

// logThumbnail points at the opener's avatar, the way the previous bot's
// closed record did. Empty for an unknown opener, and for a partial user with
// no discriminator (disgo's default-avatar URL needs one, so it returns "").
func logThumbnail(opener discord.User) string {
	if opener.ID == 0 {
		return ""
	}
	return opener.EffectiveAvatarURL(discord.WithSize(128))
}

// openedLogEmbed renders the "Ticket Opened" record. It gets deleted and
// replaced by closedLogEmbed when the ticket closes, so it carries the live
// channel link the closed record cannot.
func openedLogEmbed(tk *modules.Ticket, g TypeConfig, opener discord.User) discord.Embed {
	desc := logCreatorLine(tk, opener)
	if tk.ChannelID != "" {
		desc += " in <#" + tk.ChannelID + ">"
	}
	desc += ".\n" + logPanelLine(tk, g) +
		"\n**Opened on:** " + discordTime(tk.OpenedAt, "F")
	return discord.NewEmbed().
		WithTitle("Ticket Opened").
		WithDescription(desc).
		WithColor(logColorOpened)
}

// closedLogEmbed renders the "Ticket Closed" record in the layout the previous
// bot used: creator line, then panel, opened, closed, closed-by and reason on
// their own lines, with the opener's avatar as the thumbnail.
func closedLogEmbed(tk *modules.Ticket, g TypeConfig, closedBy string, opener discord.User) discord.Embed {
	closedAt := tk.ClosedAt
	if closedAt.IsZero() {
		closedAt = time.Now().UTC()
	}
	desc := logCreatorLine(tk, opener) + " has been closed.\n" + logPanelLine(tk, g) +
		"\n**Opened on:** " + discordTime(tk.OpenedAt, "F") +
		"\n**Closed on:** " + discordTime(closedAt, "F")
	switch {
	case tk.CloseReason == "channel_deleted":
		desc += "\n**Closed by:** channel deleted"
	default:
		desc += "\n**Closed by:** " + mentionOrID(closedBy)
	}
	reason := strings.TrimSpace(tk.CloseReasonNote)
	if reason == "" {
		reason = "None"
	}
	desc += "\n**Reason:** " + truncateRunes(reason, 1000)
	e := discord.NewEmbed().
		WithTitle("Ticket Closed").
		WithDescription(desc).
		WithColor(logColorClosed)
	if thumb := logThumbnail(opener); thumb != "" {
		e = e.WithThumbnail(thumb)
	}
	return e
}

// deleteOpenedLogRecord removes the "Ticket Opened" log message now that the
// closed record replaced it, then clears the stored ID so a retried close tail
// cannot delete an unrelated message. An already-gone message counts as
// deleted; any other failure leaves the two records side by side, which is what
// the log looked like before this existed.
func (m *TicketsModule) deleteOpenedLogRecord(tk *modules.Ticket, logCh snowflake.ID) {
	if tk.LogMsgID == "" {
		return
	}
	mid, err := snowflake.Parse(tk.LogMsgID)
	if err != nil {
		return
	}
	if err := m.ctx.Rest.DeleteMessage(logCh, mid); err != nil {
		var rerr *rest.Error
		if !errors.As(err, &rerr) || rerr.Code != rest.JSONErrorCodeUnknownMessage {
			m.ctx.Logger.Warn("Tickets: deleting the opened log record for %s failed: %v", tk.ID, err)
			return
		}
	}
	_, _, _ = m.store.mutate(tk.GuildID, tk.ID, func(cur *modules.Ticket) bool {
		if cur.LogMsgID == "" {
			return false
		}
		cur.LogMsgID = ""
		return true
	})
}

// registerChannels subscribes the guild-channel-delete hook so a deleted
// ticket channel is finalized (closed + transcript) automatically.
func (m *TicketsModule) registerChannels() {
	m.ctx.Events.AddGuildChannelDelete(func(e *events.GuildChannelDelete) {
		defer m.recoverLog("channel-delete")
		guildID := e.GuildID.String()
		channelID := e.ChannelID.String()
		tk := m.ticketByChannel(guildID, channelID)
		if tk == nil {
			return
		}
		m.finalizeDeletedTicket(tk, "channel_deleted")
	})
}

// finalizeDeletedTicket closes a ticket whose channel was deleted: marks it
// closed with the given reason and runs the close tail (attachment mirror,
// HTML transcript, log channel) in a recovered goroutine. History fetch is
// skipped — the channel no longer exists to page.
func (m *TicketsModule) finalizeDeletedTicket(tk *modules.Ticket, reason string) {
	// Idempotent close: the close itself is a store mutation, so exactly one
	// caller (a user close or an earlier finalize) can win. A loser must bail
	// BEFORE editing buttons / saving / spawning the tail — otherwise the
	// transcript and the "Ticket closed" log post would run twice.
	post, changed, err := m.store.mutate(tk.GuildID, tk.ID, func(cur *modules.Ticket) bool {
		return m.markClosed(cur, "", "", reason, "")
	})
	if err != nil || post == nil || !changed {
		return
	}
	g, _ := m.typeOf(post.GuildID, post.EffectiveType())
	m.editClosedButtons(post, g)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.ctx.Logger.Error("Tickets: panic finalizing deleted ticket %s: %v", post.ID, r)
			}
		}()
		m.finalizeTicket(post, g, "", reason, closeOptions{skipLock: true, skipHistory: true}, true)
	}()
}

// reconcileOpenTickets runs once at startup (after m.reconcileDelay) and
// checks every open ticket's channel: if the channel no longer exists
// (UnknownChannel / 404) the ticket is finalized as channel-deleted. Other
// errors are ignored (transient). Exits on stop.
func (m *TicketsModule) reconcileOpenTickets(stop <-chan struct{}) {
	select {
	case <-time.After(m.reconcileDelay):
	case <-stop:
		return
	}
	changed := 0
	for _, guildTickets := range m.store.openTicketsSnapshot() {
		for _, tk := range guildTickets {
			if tk.ChannelID == "" {
				continue
			}
			chID, err := snowflake.Parse(tk.ChannelID)
			if err != nil {
				continue
			}
			_, err = m.ctx.Rest.GetChannel(chID)
			if err == nil {
				continue
			}
			var rerr *rest.Error
			if !errors.As(err, &rerr) {
				continue
			}
			isDeleted := rerr.Code == rest.JSONErrorCodeUnknownChannel ||
				(rerr.Response != nil && rerr.Response.StatusCode == http.StatusNotFound)
			if !isDeleted {
				continue
			}
			m.finalizeDeletedTicket(tk, "channel_deleted")
			changed++
		}
	}
	if changed > 0 {
		m.ctx.Logger.Info("Tickets: reconciled %d open ticket(s) with deleted channels", changed)
	}
}

// retentionLoop prunes closed tickets past the retention window: first run
// after m.retentionFirstDelay, then every m.retentionInterval. Exits on stop.
func (m *TicketsModule) retentionLoop(stop <-chan struct{}) {
	select {
	case <-time.After(m.retentionFirstDelay):
	case <-stop:
		return
	}
	ticker := time.NewTicker(m.retentionInterval)
	defer ticker.Stop()
	for {
		n := m.store.pruneClosed(m.module.RetentionDays())
		if n > 0 {
			m.ctx.Logger.Info("Tickets: retention sweep removed %d closed ticket(s)", n)
		}
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}
