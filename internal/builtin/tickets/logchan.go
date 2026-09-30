package tickets

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// postLogEvent posts a single embed to the guild's configured log channel.
// No-op when no log channel is set.
func (m *TicketsModule) postLogEvent(guildID, title, desc string, color int) {
	cfg := m.guildConfig(guildID)
	m.mu.RLock()
	logCh := cfg.LogChannel
	m.mu.RUnlock()
	if logCh == "" {
		return
	}
	chID, err := snowflake.Parse(logCh)
	if err != nil {
		return
	}
	embed := discord.NewEmbed().WithTitle(title).WithDescription(desc).WithColor(color)
	if _, err := m.ctx.Rest.CreateMessage(chID, discord.MessageCreate{Embeds: []discord.Embed{embed}}); err != nil {
		m.ctx.Logger.Warn("Tickets: failed to post log event for %s: %v", guildID, err)
	}
}

// openLogEmbed builds the "Ticket opened" log-channel embed text (pure;
// shared by the open path and tests).
func openLogEmbed(tk *modules.Ticket, g TypeConfig, panelName string) (title, desc string) {
	title = "🎫 Ticket opened"
	desc = fmt.Sprintf("**%s** (`%s`) · <#%s> · opened by <@%s>", g.Label, tk.ID, tk.ChannelID, tk.OpenerID)
	if panelName != "" {
		desc += "\nPanel: `" + panelName + "`"
	}
	return title, desc
}

// closeLogEmbed builds the "Ticket closed" log-channel embed text (pure;
// shared by the close path and tests).
func closeLogEmbed(tk *modules.Ticket, g TypeConfig, closedBy string) (title, desc string) {
	title = "Ticket closed"
	label := g.Label
	if label == "" {
		label = tk.EffectiveType()
	}
	claimLine := ""
	if tk.ClaimerID != "" {
		claimLine = fmt.Sprintf(" · claimed by <@%s>", tk.ClaimerID)
	}
	reasonLine := " · closed by <@" + closedBy + ">"
	if tk.CloseReason == "channel_deleted" {
		reasonLine = " · channel deleted"
	}
	desc = fmt.Sprintf("**%s** (`%s`) · opened <t:%d:R> by <@%s>%s%s",
		label, tk.ID, tk.OpenedAt.Unix(), tk.OpenerID, claimLine, reasonLine)
	return title, desc
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
		return m.markClosed(cur, "", "", reason)
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
