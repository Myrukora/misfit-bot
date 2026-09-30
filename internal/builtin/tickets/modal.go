package tickets

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
)

// answer is one answered open-time question (label + free-text value).
type answer struct {
	Label string
	Value string
}

// modalCustomIDPrefix marks the open-question modal's custom_id.
const modalCustomIDPrefix = "tickets:openmodal:"

// buildOpenModal constructs the Discord modal for a panel's open-time
// questions. The title is the panel's ModalTitle, falling back to the panel
// title then the type label, truncated to Discord's 45-char modal-title cap.
func buildOpenModal(p PanelConfig, t TypeConfig, panelName string) discord.ModalCreate {
	title := firstNonEmpty(p.ModalTitle, firstNonEmpty(p.Title, t.Label))
	title = truncateRunes(title, 45)
	m := discord.NewModalCreate(modalCustomIDPrefix+panelName, title)
	for i, q := range p.Questions {
		style := discord.TextInputStyleShort
		if q.Style == "paragraph" {
			style = discord.TextInputStyleParagraph
		}
		input := discord.NewTextInput(fmt.Sprintf("q%d", i), style)
		if q.Placeholder != "" {
			input = input.WithPlaceholder(q.Placeholder)
		}
		input = input.WithRequired(q.Required)
		if q.Value != "" {
			input = input.WithValue(q.Value)
		}
		m = m.AddLabel(q.Label, input)
	}
	return m
}

// truncateRunes truncates s to at most n runes.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// panelUsesModal reports whether the panel currently opens via a question
// modal (modals enabled globally AND the panel has questions).
func (m *TicketsModule) panelUsesModal(guildID, panelName string) bool {
	if !m.isLoaded() {
		return false
	}
	cfg := m.guildConfig(guildID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := cfg.Panels[panelName]
	if !ok {
		return false
	}
	return m.module.ModalsOn() && len(p.Questions) > 0
}

// HandlesRawComponent reports whether this module wants the raw (un-deferred)
// component event for e — i.e. an open button whose panel uses a modal. The
// dispatcher defers the interaction only when no loaded module handles it
// raw. MUST return false when the module is not loaded so an unload can never
// leave a component unacknowledged.
func (m *TicketsModule) HandlesRawComponent(e *events.ComponentInteractionCreate) bool {
	if m.ctx == nil || !m.isLoaded() {
		return false
	}
	data, ok := e.Data.(discord.ButtonInteractionData)
	if !ok {
		return false
	}
	const prefix = "tickets:open:"
	if !strings.HasPrefix(data.CustomID(), prefix) {
		return false
	}
	panelName := strings.TrimPrefix(data.CustomID(), prefix)
	guildID := guildIDOf(e)
	if guildID == "" {
		return false
	}
	return m.panelUsesModal(guildID, panelName)
}

// onOpenButtonWithModal sends the open-question modal for the panel. If the
// modal cannot be sent the interaction is still unresponded, so the fallback
// is the initial (ephemeral) response — not a followup.
func (m *TicketsModule) onOpenButtonWithModal(e *events.ComponentInteractionCreate, guildID, panelName string) {
	cfg := m.guildConfig(guildID)
	m.mu.RLock()
	p, ok := cfg.Panels[panelName]
	m.mu.RUnlock()
	if !ok {
		m.ephemeralErr(e, "This panel no longer exists.")
		return
	}
	g, _ := m.typeOf(guildID, p.TypeKey)
	if err := e.Modal(buildOpenModal(p, g, panelName)); err != nil {
		m.ctx.Logger.Warn("Tickets: failed to send open modal for %s: %v", panelName, err)
		_ = e.CreateMessage(discord.MessageCreate{
			Content: "Could not open the ticket form — please press the button again.",
			Flags:   discord.MessageFlagEphemeral,
		})
	}
}

// registerModal subscribes the modal-submit hook for open-question modals.
func (m *TicketsModule) registerModal() {
	m.ctx.Events.AddModalSubmit(func(e *events.ModalSubmitInteractionCreate) {
		defer m.recoverLog("modal-submit")
		m.onOpenModalSubmit(e)
	})
}

// onOpenModalSubmit handles a submitted open-question modal: it defers an
// ephemeral response, opens the ticket with the answers, and updates the
// deferred message with the result. Post-deferral updates go through
// Rest.UpdateInteractionResponse (PATCH /webhooks/{app}/{token}/messages/@original)
// — e.UpdateMessage would POST a second initial callback, which Discord
// rejects (40060) because DeferCreateMessage already consumed the one grant.
func (m *TicketsModule) onOpenModalSubmit(e *events.ModalSubmitInteractionCreate) {
	data := e.Data
	if !strings.HasPrefix(data.CustomID, modalCustomIDPrefix) {
		return
	}
	panelName := strings.TrimPrefix(data.CustomID, modalCustomIDPrefix)
	if e.GuildID() == nil {
		_ = e.CreateMessage(discord.MessageCreate{
			Content: "Tickets only work inside a server.",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}
	guildID := e.GuildID().String()
	_ = e.DeferCreateMessage(true)

	// updateResult edits the deferred "thinking…" message. Logged, never
	// swallowed: a failed edit would leave the user on "thinking…" forever.
	updateResult := func(content string) {
		if _, err := m.ctx.Rest.UpdateInteractionResponse(e.ApplicationID(), e.Token(), discord.MessageUpdate{
			Content: new(content),
		}); err != nil {
			m.ctx.Logger.Warn("Tickets: failed to update modal result: %v", err)
		}
	}

	cfg := m.guildConfig(guildID)
	m.mu.RLock()
	p, ok := cfg.Panels[panelName]
	m.mu.RUnlock()
	if !ok {
		updateResult("This panel is no longer configured.")
		return
	}
	g, ok := m.typeOf(guildID, p.TypeKey)
	if !ok || !g.Enabled {
		updateResult("This ticket type is currently disabled.")
		return
	}

	answers := make([]answer, 0, len(p.Questions))
	for i, q := range p.Questions {
		answers = append(answers, answer{Label: q.Label, Value: data.Text(fmt.Sprintf("q%d", i))})
	}

	tk, err := m.openTicket(g, e.User(), guildID, p.Name, answers)
	if err != nil {
		updateResult("Could not open the ticket: " + err.Error())
		return
	}
	updateResult(fmt.Sprintf("Ticket <#%s> opened as `%s`.", tk.ChannelID, tk.ID))
}

// buildAnswersContent renders the answered questions as a message body,
// truncated to Discord's 2000-char content cap (ellipsis included: 1999 +
// "…" = 2000 total, so the result never exceeds the cap).
func buildAnswersContent(openerName string, answers []answer) string {
	var b strings.Builder
	b.WriteString("**Answers from " + openerName + "**")
	for _, a := range answers {
		val := a.Value
		if val == "" {
			val = "—"
		}
		b.WriteString("\n\n**" + a.Label + "**\n" + val)
	}
	s := b.String()
	if len([]rune(s)) > 2000 {
		runes := []rune(s)
		s = string(runes[:1999]) + "…"
	}
	return s
}
