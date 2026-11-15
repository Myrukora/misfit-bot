package tickets

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	"github.com/misfit/bot/modules"
)

// answer is one answered open-time question (label + free-text value).
type answer struct {
	Label string
	Value string
}

// modalCustomIDPrefix marks the open-question modal's custom_id.
const modalCustomIDPrefix = "tickets:openmodal:"

// closeModalCustomIDPrefix marks the close-reason modal's custom_id. The
// ticket ID rides in the custom_id because a modal submit carries no link back
// to the message it came from.
const closeModalCustomIDPrefix = "tickets:closemodal:"

// closeReasonFieldID is the custom_id of the single text input in the close
// modal.
const closeReasonFieldID = "reason"

// maxCloseReason caps the stored free-text close reason. Discord's own modal
// input cap is 4000; the log embed only shows the first 1000.
const maxCloseReason = 1000

// buildCloseModal constructs the close-reason modal for one ticket.
func buildCloseModal(ticketID string) discord.ModalCreate {
	input := discord.NewTextInput(closeReasonFieldID, discord.TextInputStyleParagraph)
	input = input.WithPlaceholder("Why is this ticket being closed?").WithRequired(true)
	return discord.NewModalCreate(closeModalCustomIDPrefix+ticketID, "Close ticket").
		AddLabel("Reason for closing", input)
}

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
// component event for e — an open button whose panel uses a modal, or a close
// button whose type asks for a close reason. The dispatcher defers the
// interaction only when no loaded module handles it raw. MUST return false
// when the module is not loaded so an unload can never leave a component
// unacknowledged.
func (m *TicketsModule) HandlesRawComponent(e *events.ComponentInteractionCreate) bool {
	if m.ctx == nil || !m.isLoaded() {
		return false
	}
	data, ok := e.Data.(discord.ButtonInteractionData)
	if !ok {
		return false
	}
	guildID := guildIDOf(e)
	if guildID == "" {
		return false
	}
	customID := data.CustomID()
	const openPrefix = "tickets:open:"
	if strings.HasPrefix(customID, openPrefix) {
		return m.panelUsesModal(guildID, strings.TrimPrefix(customID, openPrefix))
	}
	const closePrefix = "tickets:close:"
	if strings.HasPrefix(customID, closePrefix) {
		ticketID := strings.TrimPrefix(customID, closePrefix)
		tk, err := m.store.load(guildID, ticketID)
		if err != nil || tk == nil || tk.Status != "open" {
			return false
		}
		return m.typeAsksCloseReason(tk.GuildID, tk.EffectiveType())
	}
	return false
}

// typeAsksCloseReason reports whether a type prompts for a reason before
// closing. A vanished type means no prompt (fail closed to the historical
// immediate-close flow).
func (m *TicketsModule) typeAsksCloseReason(guildID, typeKey string) bool {
	g, ok := m.typeOf(guildID, typeKey)
	return ok && g.CloseReasonOn()
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

// registerModal subscribes the modal-submit hook for open-question and
// close-reason modals.
func (m *TicketsModule) registerModal() {
	m.ctx.Events.AddModalSubmit(func(e *events.ModalSubmitInteractionCreate) {
		defer m.recoverLog("modal-submit")
		if strings.HasPrefix(e.Data.CustomID, closeModalCustomIDPrefix) {
			m.onCloseModalSubmit(e)
			return
		}
		m.onOpenModalSubmit(e)
	})
}

// canCloseAsUser is the interaction-free twin of canCloseTicket, for paths
// that have no ComponentInteractionCreate (modal submits). It mirrors the same
// rules: the opener may close while the type allows it; everyone else needs a
// helper role, Manage Messages, or bot owner/elevated. Everything here is
// REST-based, so it works without the Server Members intent.
func (m *TicketsModule) canCloseAsUser(tk *modules.Ticket, userID string) bool {
	if userID == "" {
		return false
	}
	if userID == tk.OpenerID {
		return m.openerMayClose(tk)
	}
	if m.ctx.Bot != nil && (m.ctx.Bot.IsOwner(userID) || m.ctx.Bot.IsElevated(userID)) {
		return true
	}
	if isHelperByType(m, tk, userID) {
		return true
	}
	if perms, ok := m.memberPermissions(tk.GuildID, userID); ok && perms.Has(discord.PermissionManageMessages) {
		return true
	}
	return false
}

// onCloseModalSubmit handles a submitted close-reason modal: it closes the
// ticket with the typed reason and updates the deferred reply. Permission is
// re-checked here even though the button that opened the modal was already
// authorized: the submit is a SEPARATE interaction, and the closer may have
// lost their helper role (or the opener may have lost the right) in between.
func (m *TicketsModule) onCloseModalSubmit(e *events.ModalSubmitInteractionCreate) {
	ticketID := strings.TrimPrefix(e.Data.CustomID, closeModalCustomIDPrefix)
	if e.GuildID() == nil {
		_ = e.CreateMessage(discord.MessageCreate{
			Content: "Tickets only work inside a server.",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}
	guildID := e.GuildID().String()
	_ = e.DeferCreateMessage(true)

	updateResult := func(content string) {
		if _, err := m.ctx.Rest.UpdateInteractionResponse(e.ApplicationID(), e.Token(), discord.MessageUpdate{
			Content: new(content),
		}); err != nil {
			m.ctx.Logger.Warn("Tickets: failed to update close result: %v", err)
		}
	}

	tk, err := m.store.load(guildID, ticketID)
	if err != nil || tk == nil || tk.Status != "open" {
		updateResult("This ticket is already closed.")
		return
	}
	userID := e.User().ID.String()
	if !m.canCloseAsUser(tk, userID) {
		updateResult("Only helpers or the ticket opener can close tickets.")
		return
	}
	note := strings.TrimSpace(e.Data.Text(closeReasonFieldID))
	if note == "" {
		// Discord's "required" only rejects an empty box, so whitespace slips
		// through. Refuse rather than closing with a blank reason, which would
		// defeat the point of asking.
		updateResult("A reason is required to close this ticket — press Close and enter one.")
		return
	}
	if runes := []rune(note); len(runes) > maxCloseReason {
		note = string(runes[:maxCloseReason])
	}
	if err := m.closeTicketFromInteraction(guildID, ticketID, userID, note); err != nil {
		updateResult("Failed to close: " + err.Error())
		return
	}
	updateResult("Ticket **" + ticketID + "** closed — transcript is being saved.")
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
