package tickets

import (
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// registerButtons subscribes the single ComponentInteraction router.
// CustomID scheme: "tickets:open:<group>", "tickets:claim:<id>",
// "tickets:close:<id>", "tickets:panel" (refresh). Unknown prefixes are
// ignored so several modules can share the event hook safely.
func (m *TicketsModule) registerButtons() {
	m.ctx.Events.AddComponentInteraction(func(e *events.ComponentInteractionCreate) {
		defer func() {
			if r := recover(); r != nil {
				m.ctx.Logger.Error("Tickets: panic in button handler: %v", r)
			}
		}()
		cid, ok := e.Data.(discord.ButtonInteractionData)
		if !ok {
			return // selects/modals are not ours
		}
		customID := cid.CustomID()
		if !strings.HasPrefix(customID, "tickets:") {
			return
		}
		parts := strings.SplitN(strings.TrimPrefix(customID, "tickets:"), ":", 2)
		switch {
		case len(parts) == 2 && parts[0] == "open":
			m.onOpenButton(e, parts[1])
		case len(parts) == 2 && parts[0] == "claim":
			m.onClaimButton(e, parts[1])
		case len(parts) == 2 && parts[0] == "close":
			m.onCloseButton(e, parts[1])
		}
	})
}

func guildIDOf(e *events.ComponentInteractionCreate) string {
	if gid := e.GuildID(); gid != nil {
		return gid.String()
	}
	return ""
}

func actorUser(e *events.ComponentInteractionCreate) discord.User { return e.User() }

func effectiveName(e *events.ComponentInteractionCreate) string {
	if mem := e.Member(); mem != nil && mem.Nick != nil && *mem.Nick != "" {
		return *mem.Nick
	}
	return e.User().EffectiveName()
}

func ephemeralReply(e *events.ComponentInteractionCreate, title, desc string) {
	e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{embedInfo(title, desc)},
		Flags:  discord.MessageFlagEphemeral,
	})
}

func (m *TicketsModule) ephemeralErr(e *events.ComponentInteractionCreate, msg string) {
	ephemeralReply(e, "Tickets", msg)
}

// ── open ──────────────────────────────────────────────────────────────────

func (m *TicketsModule) onOpenButton(e *events.ComponentInteractionCreate, panelName string) {
	guildID := guildIDOf(e)
	if guildID == "" {
		m.ephemeralErr(e, "Tickets only work inside a server.")
		return
	}
	m.mu.RLock()
	panel, ok := m.cfg.Panels[panelName]
	m.mu.RUnlock()
	if !ok {
		m.ephemeralErr(e, "This panel no longer exists.")
		return
	}
	if panel.Suspended {
		m.ephemeralErr(e, "This panel is currently suspended.")
		return
	}
	g, ok := m.typeOf(panel.TypeKey)
	if !ok || !g.Enabled {
		m.ephemeralErr(e, "This ticket type is currently disabled.")
		return
	}
	opener := actorUser(e)
	if opener.ID == 0 {
		m.ephemeralErr(e, "Could not resolve your user — try again.")
		return
	}
	ticket, err := m.openTicket(g, opener, guildID, panel.Name)
	if err != nil {
		m.ephemeralErr(e, err.Error())
		return
	}
	e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{embedSuccess("Ticket opened",
			fmt.Sprintf("Your ticket **%s** continues here: <#%s>", ticket.ID, ticket.ChannelID))},
		Flags: discord.MessageFlagEphemeral,
	})
}

// ── claim ─────────────────────────────────────────────────────────────────

func (m *TicketsModule) onClaimButton(e *events.ComponentInteractionCreate, ticketID string) {
	guildID := guildIDOf(e)
	tk, err := m.store.load(guildID, ticketID)
	if err != nil || tk == nil || tk.Status != "open" {
		m.ephemeralErr(e, "This ticket no longer exists or is closed.")
		return
	}
	userID := e.User().ID.String()
	name := effectiveName(e)

	m.mu.Lock()
	if tk.ClaimerID != "" {
		claimed := tk.ClaimerID
		m.mu.Unlock()
		m.ephemeralErr(e, fmt.Sprintf("Already claimed by <@%s>.", claimed))
		return
	}
	tk.ClaimerID = userID
	tk.ClaimedAt = time.Now().UTC()
	m.mu.Unlock()

	g, _ := m.typeOf(tk.EffectiveType())
	if tk.MessageID != "" {
		m.editTicketButtons(tk, g, "Claimed by "+name)
	}
	_ = m.store.save(tk)
	e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{embedSuccess("Claimed",
			fmt.Sprintf("<@%s> claimed this ticket.", userID))},
	})
}

// ── close ─────────────────────────────────────────────────────────────────

func (m *TicketsModule) onCloseButton(e *events.ComponentInteractionCreate, ticketID string) {
	guildID := guildIDOf(e)
	if !m.canManage(e) {
		m.ephemeralErr(e, "You need the **Manage Messages** permission to close tickets.")
		return
	}
	tk, err := m.store.load(guildID, ticketID)
	if err != nil || tk == nil || tk.Status != "open" {
		m.ephemeralErr(e, "This ticket is already closed.")
		return
	}
	// Acknowledge FIRST (3s interaction deadline), then run the close; the
	// transcript pipeline continues in the background inside CloseTicket.
	if err := m.closeTicketFromInteraction(guildID, ticketID, e.User().ID.String()); err != nil {
		m.ephemeralErr(e, "Failed to close: "+err.Error())
		return
	}
	e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{embedSuccess("Closed", "Ticket **"+ticketID+"** closed — transcript is being saved.")},
	})
}

// closeTicketFromInteraction runs CloseTicket with panic recovery so a close
// failure never takes down the interaction handler.
func (m *TicketsModule) closeTicketFromInteraction(guildID, ticketID, userID string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			m.ctx.Logger.Error("Tickets: panic closing %s: %v", ticketID, r)
			err = fmt.Errorf("internal error")
		}
	}()
	return m.CloseTicket(guildID, ticketID, userID)
}

// canManage checks ManageMessages (in-guild member perms) OR bot-level
// owner/elevated.
func (m *TicketsModule) canManage(e *events.ComponentInteractionCreate) bool {
	u := e.User().ID.String()
	if m.ctx.Bot.IsOwner(u) || m.ctx.Bot.IsElevated(u) {
		return true
	}
	if mem := e.Member(); mem != nil {
		return mem.Permissions.Has(discord.PermissionManageMessages)
	}
	return false
}

// ── panel refresh ────────────────────────────────────────────────────────

func (m *TicketsModule) ephemeralReplyOK(e *events.ComponentInteractionCreate, msg string) {
	e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{embedSuccess("Tickets", msg)},
		Flags:  discord.MessageFlagEphemeral,
	})
}

// ── shared message edits ─────────────────────────────────────────────────

// editTicketButtons rewrites the in-channel action row: claimed state or
// full disable after close.
func (m *TicketsModule) editTicketButtons(tk *modules.Ticket, g TypeConfig, claimLabel string) {
	if tk.MessageID == "" || tk.ChannelID == "" {
		return
	}
	mid, err1 := snowflake.Parse(tk.MessageID)
	cid, err2 := snowflake.Parse(tk.ChannelID)
	if err1 != nil || err2 != nil {
		return
	}
	row := ticketButtons(tk, g, claimLabel)
	update := discord.MessageUpdate{Components: &row}
	if _, err := m.ctx.Rest.UpdateMessage(cid, mid, update); err != nil {
		m.ctx.Logger.Warn("Tickets: failed to edit buttons on %s: %v", tk.ID, err)
	}
}

// editClosedButtons disables every button after close (greyed "Closed").
func (m *TicketsModule) editClosedButtons(tk *modules.Ticket, g TypeConfig) {
	if tk.MessageID == "" || tk.ChannelID == "" {
		return
	}
	buttons := []discord.InteractiveComponent{}
	if g.AllowClaimOn() {
		buttons = append(buttons, discord.ButtonComponent{
			Style: discord.ButtonStyleSecondary, Label: "Closed",
			CustomID: "tickets:claim:" + tk.ID, Disabled: true,
		})
	}
	if g.AllowCloseOn() {
		buttons = append(buttons, discord.ButtonComponent{
			Style: discord.ButtonStyleDanger, Label: "Close",
			CustomID: "tickets:close:" + tk.ID, Disabled: true,
		})
	}
	if len(buttons) == 0 {
		return
	}
	row := []discord.LayoutComponent{discord.NewActionRow(buttons...)}
	mid, err1 := snowflake.Parse(tk.MessageID)
	cid, err2 := snowflake.Parse(tk.ChannelID)
	if err1 != nil || err2 != nil {
		return
	}
	update := discord.MessageUpdate{Components: &row}
	if _, err := m.ctx.Rest.UpdateMessage(cid, mid, update); err != nil {
		m.ctx.Logger.Warn("Tickets: failed to grey buttons on %s: %v", tk.ID, err)
	}
}
