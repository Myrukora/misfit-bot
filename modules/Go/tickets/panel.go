package main

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// Small local embed builders — the shared `embed` package targets prefix
// commands; interaction replies want plain embeds with our own colors.
func embedSuccess(title, desc string) discord.Embed {
	return discord.NewEmbed().WithDescription("**" + title + "**\n" + desc).WithColor(0x57F287)
}

func embedError(title, desc string) discord.Embed {
	return discord.NewEmbed().WithDescription("**" + title + "**\n" + desc).WithColor(0xED4245)
}

func embedInfo(title, desc string) discord.Embed {
	return discord.NewEmbed().WithDescription("**" + title + "**\n" + desc).WithColor(0x5865F2)
}

// buildPanelEmbed renders the staff control-panel embed for a guild.
func (m *TicketsModule) buildPanelEmbed(guildID string) discord.Embed {
	groups := m.groupsSnapshot()
	var b strings.Builder
	b.WriteString("Open a ticket with the buttons below.\n")
	enabled := 0
	for _, g := range groups {
		state := "🟢"
		if !g.Enabled {
			state = "🔴"
		}
		fmt.Fprintf(&b, "%s **%s** (`%s`)\n", state, g.Label, g.Key)
		if g.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		b.WriteString("\n*No groups are enabled yet — configure them in the dashboard.*")
	}
	return discord.NewEmbed().
		WithTitle("🎫 Tickets").
		WithDescription(b.String()).
		WithColor(defaultTicketColor).
		WithFooter("misfit-bot tickets", "")
}

// buildPanelRow builds the action rows: an Open button per group + Refresh.
func (m *TicketsModule) buildPanelRows(guildID string) []discord.LayoutComponent {
	groups := m.groupsSnapshot()
	buttons := []discord.InteractiveComponent{}
	for _, g := range groups {
		disabled := !g.Enabled
		buttons = append(buttons, discord.ButtonComponent{
			Style:    discord.ButtonStylePrimary,
			Label:    g.Label,
			CustomID: "tickets:open:" + g.Key,
			Disabled: disabled,
		})
		if len(buttons) == 5 { // Discord max per row
			break
		}
	}
	rows := []discord.LayoutComponent{}
	if len(buttons) > 0 {
		rows = append(rows, discord.NewActionRow(buttons...))
	}
	rows = append(rows, discord.NewActionRow(discord.ButtonComponent{
		Style:    discord.ButtonStyleSecondary,
		Label:    "Refresh",
		CustomID: "tickets:panel",
	}))
	return rows
}

// postOrUpdatePanel posts the control panel into control_channel (or edits
// the existing one). Returns the message ID.
func (m *TicketsModule) postOrUpdatePanel(guildID string) (string, error) {
	m.mu.RLock()
	gcfg, ok := m.cfg.Guilds[guildID]
	panelID := m.panelMsgIDs[guildID]
	m.mu.RUnlock()
	if !ok || gcfg.ControlChannel == "" {
		return "", fmt.Errorf("control_channel is not configured for this server")
	}
	chID, err := snowflake.Parse(gcfg.ControlChannel)
	if err != nil {
		return "", fmt.Errorf("control_channel is invalid")
	}

	create := discord.MessageCreate{
		Embeds:     []discord.Embed{m.buildPanelEmbed(guildID)},
		Components: m.buildPanelRows(guildID),
	}
	if panelID != "" {
		if pid, err := snowflake.Parse(panelID); err == nil {
			update := discord.MessageUpdate{Embeds: &create.Embeds, Components: &create.Components}
			if _, err := m.ctx.Rest.UpdateMessage(chID, pid, update); err == nil {
				return panelID, nil
			}
		}
	}
	msg, err := m.ctx.Rest.CreateMessage(chID, create)
	if err != nil {
		return "", fmt.Errorf("failed to post control panel: %w", err)
	}
	id := msg.ID.String()
	m.mu.Lock()
	m.panelMsgIDs[guildID] = id
	m.mu.Unlock()
	return id, nil
}
