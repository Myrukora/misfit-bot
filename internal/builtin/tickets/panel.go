package tickets

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// panel.go — named panel registry. A panel is a posted embed advertising one
// type; the bot remembers name → {channel, message, type} so panels can be
// edited/suspended/moved by NAME (never message IDs).

// buildPanelEmbed renders one panel's embed from its config + bound type.
func buildPanelEmbed(p PanelConfig, t TypeConfig) discord.Embed {
	title := p.Title
	if title == "" {
		title = "🎫 " + t.Label
	}
	desc := p.Description
	if desc == "" {
		desc = "Open a ticket with the button below.\nA private channel will be created and our team will be notified."
	}
	color := int(t.Color)
	if color == 0 {
		color = defaultTicketColor
	}
	return discord.NewEmbed().
		WithTitle(title).
		WithDescription(desc).
		WithColor(color).
		WithFooter("misfit-bot tickets", "")
}

// buildPanelRows builds the action row for ONE panel: a single Open button
// (label+emoji from the bound type) — suspended panels render disabled.
func buildPanelRows(p PanelConfig, t TypeConfig) []discord.LayoutComponent {
	label := t.ButtonLabel
	if label == "" {
		label = t.Label
	}
	disabled := p.Suspended || !t.Enabled
	btn := discord.ButtonComponent{
		Style:    discord.ButtonStylePrimary,
		Label:    label,
		CustomID: "tickets:open:" + p.Name,
		Disabled: disabled,
	}
	if e := parseButtonEmoji(t.ButtonEmoji); e != nil {
		btn.Emoji = e
	}
	return []discord.LayoutComponent{discord.NewActionRow(btn)}
}

// parseButtonEmoji converts user config ("👍" or "<:name:id>"/":name:id:")
// into a *discord.ComponentEmoji; nil when unset/invalid.
func parseButtonEmoji(raw string) *discord.ComponentEmoji {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "<:") && strings.HasSuffix(raw, ">") {
		parts := strings.SplitN(strings.Trim(raw, "<>"), ":", 3)
		if len(parts) == 3 {
			if id, err := snowflake.Parse(parts[2]); err == nil {
				return &discord.ComponentEmoji{Name: parts[1], ID: id}
			}
		}
		return nil
	}
	return &discord.ComponentEmoji{Name: raw}
}

// postOrUpdatePanel posts the panel embed (or edits in place if we already
// know its message), then records it in the registry.
func (m *TicketsModule) postOrUpdatePanel(guildID string, p *PanelConfig) error {
	t, ok := m.typeOf(p.TypeKey)
	if !ok {
		return fmt.Errorf("panel %q references unknown type %q", p.Name, p.TypeKey)
	}
	chID, err := snowflake.Parse(p.ChannelID)
	if err != nil {
		return fmt.Errorf("panel %q has an invalid channel", p.Name)
	}
	create := discord.MessageCreate{
		Embeds:     []discord.Embed{buildPanelEmbed(*p, t)},
		Components: buildPanelRows(*p, t),
	}
	if p.MessageID != "" {
		if mid, err := snowflake.Parse(p.MessageID); err == nil {
			update := discord.MessageUpdate{Embeds: &create.Embeds, Components: &create.Components}
			if _, err := m.ctx.Rest.UpdateMessage(chID, mid, update); err == nil {
				m.mu.Lock()
				m.cfg.Panels[p.Name] = *p
				_ = m.cfg.save(m.ctx.DataDir)
				m.mu.Unlock()
				return nil
			}
		}
		p.MessageID = "" // stale — repost fresh
	}
	msg, err := m.ctx.Rest.CreateMessage(chID, create)
	if err != nil {
		return fmt.Errorf("failed to post panel %q: %w", p.Name, err)
	}
	p.MessageID = msg.ID.String()
	m.mu.Lock()
	m.cfg.Panels[p.Name] = *p
	err = m.cfg.save(m.ctx.DataDir)
	m.mu.Unlock()
	return err
}

// setPanelSuspended greys/un-greys one panel's Open button and persists the
// flag. Other panels are untouched. Returns the updated panel config.
func (m *TicketsModule) setPanelSuspended(name string, suspended bool) (PanelConfig, error) {
	m.mu.Lock()
	p, ok := m.cfg.Panels[name]
	if !ok {
		m.mu.Unlock()
		return PanelConfig{}, fmt.Errorf("unknown panel %q", name)
	}
	p.Suspended = suspended
	m.cfg.Panels[name] = p
	saveErr := m.cfg.save(m.ctx.DataDir)
	m.mu.Unlock()

	if saveErr != nil {
		return p, saveErr
	}
	// Best-effort live edit; registry already flipped so restarts stay correct.
	if t, ok := m.typeOf(p.TypeKey); ok {
		if mid, err1 := snowflake.Parse(p.MessageID); err1 == nil {
			if cid, err2 := snowflake.Parse(p.ChannelID); err2 == nil {
				row := buildPanelRows(p, t) // single source of truth for the row
				update := discord.MessageUpdate{Components: &row}
				if _, err := m.ctx.Rest.UpdateMessage(cid, mid, update); err != nil {
					m.ctx.Logger.Warn("Tickets: suspend edit failed on %s: %v", name, err)
				}
			}
		}
	}
	return p, nil
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
