package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// ticketButtons builds the Claim/Close action row for a ticket panel message.
// Disabled state is applied on edit when claimed/closed.
func ticketButtons(t *modules.Ticket, g GroupConfig, claimLabel string) []discord.LayoutComponent {
	var buttons []discord.InteractiveComponent
	if g.AllowClaimOn() {
		disabled := claimLabel != "Claim"
		buttons = append(buttons, discord.ButtonComponent{
			Style: discord.ButtonStylePrimary, Label: claimLabel,
			CustomID: "tickets:claim:" + t.ID, Disabled: disabled,
		})
	}
	if g.AllowCloseOn() {
		buttons = append(buttons, discord.ButtonComponent{
			Style: discord.ButtonStyleDanger, Label: "Close",
			CustomID: "tickets:close:" + t.ID,
		})
	}
	if len(buttons) == 0 {
		return nil
	}
	return []discord.LayoutComponent{discord.NewActionRow(buttons...)}
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

// openTicket creates the private thread, posts ping line + embed + buttons
// and persists the ticket.
func (m *TicketsModule) openTicket(g GroupConfig, opener discord.User, guildID string) (*modules.Ticket, error) {
	m.mu.RLock()
	seq := m.store.nextSeq(guildID, g.Key)
	m.mu.RUnlock()

	ticket := &modules.Ticket{
		ID: fmt.Sprintf("%s-%04d", g.Key, seq), Group: g.Key, GuildID: guildID,
		OpenerID: opener.ID.String(), Status: "open", OpenedAt: time.Now().UTC(),
	}
	parent, err := snowflake.Parse(g.ParentChannel)
	if err != nil {
		return nil, fmt.Errorf("group %q has an invalid parent_channel", g.Key)
	}
	name := strings.SplitN(opener.EffectiveName(), "#", 2)[0]
	thread, err := m.ctx.Rest.CreateThread(parent, discord.GuildPrivateThreadCreate{
		Name:      fmt.Sprintf("%s-%s-%s", g.Label, name, ticket.ID),
		Invitable: boolPtr(false),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create ticket thread: %w", err)
	}
	ticket.ChannelID = thread.ID().String()

	guildName := m.resolveGuildName(guildID)
	fresh := m.fetchOpenerUser(guildID, ticket.OpenerID, opener)
	create := discord.MessageCreate{
		Content:    buildPingLine(g, ticket.OpenerID),
		Embeds:     []discord.Embed{buildOpenEmbed(ticket, g, fresh, guildName)},
		Components: ticketButtons(ticket, g, "Claim"),
	}
	msg, err := m.ctx.Rest.CreateMessage(thread.ID(), create)
	if err != nil {
		m.ctx.Logger.Error("Tickets: failed to post open embed in %s: %v", ticket.ChannelID, err)
	} else {
		ticket.MessageID = msg.ID.String()
	}
	if err := m.store.save(ticket); err != nil {
		return nil, fmt.Errorf("failed to persist ticket: %w", err)
	}
	return ticket, nil
}

func buildPingLine(g GroupConfig, openerID string) string {
	var b strings.Builder
	for _, r := range g.PingRoles {
		b.WriteString("<@&" + r + "> ")
	}
	b.WriteString("<@" + openerID + ">")
	return b.String()
}
