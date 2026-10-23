package tickets

import (
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// open.go — ticket creation: a PRIVATE TEXT CHANNEL under the type's category
// (v2; v1 used threads). Named from the type's channel-name template. The
// ticket ID lives in the channel topic so in-channel commands need no args.

// ticketButtons builds the Close action row inside a ticket channel.
func ticketButtons(t *modules.Ticket, g TypeConfig) []discord.LayoutComponent {
	if !g.AllowCloseOn() {
		return nil
	}
	return []discord.LayoutComponent{discord.NewActionRow(discord.ButtonComponent{
		Style: discord.ButtonStyleDanger, Label: "Close",
		CustomID: "tickets:close:" + t.ID,
	})}
}

func boolPtr(b bool) *bool { return &b }

// channelSlug lowercases and strips everything Discord doesn't accept in a
// channel name (letters, digits, dash, underscore; spaces/dots become dashes).
// No length cap: callers truncate the assembled name once.
func channelSlug(name string) string {
	base := strings.ToLower(strings.SplitN(name, "#", 2)[0])
	var clean strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			clean.WriteRune(r)
		case r == ' ' || r == '.':
			clean.WriteRune('-')
		}
	}
	return strings.Trim(clean.String(), "-")
}

// sanitizeChannelName makes a full channel name safe: slug + collapsed dashes
// + a non-empty fallback, capped so the -2/-3 collision suffix still fits.
func sanitizeChannelName(name string) string {
	s := channelSlug(name)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if s == "" {
		s = "ticket"
	}
	if r := []rune(s); len(r) > 80 {
		s = strings.TrimRight(string(r[:80]), "-")
	}
	return s
}

// defaultChannelName is the template used when a type doesn't set one.
const defaultChannelName = "{user}-{MM}-{DD}-{YY}"

// buildChannelName renders a type's channel-name template into a Discord-safe
// name. Tokens: {user}/{user.name}/{user.id}, {type}/{group} (key),
// {type.label}, {ticket.id}, {dd} {MM} {yyyy} {YY}, {hh} {mm} {ss}. An empty
// template keeps the historical {user}-{MM}-{DD}-{YY}; duplicate live names get
// -2/-3… suffixes.
func buildChannelName(tmpl, openerName string, g TypeConfig, tk *modules.Ticket, at time.Time, taken map[string]bool) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = defaultChannelName
	}
	openerID, ticketID := "", ""
	if tk != nil {
		openerID, ticketID = tk.OpenerID, tk.ID
	}
	repl := strings.NewReplacer(
		"{user.id}", openerID,
		"{user.name}", channelSlug(openerName),
		"{user}", channelSlug(openerName),
		"{type.label}", g.Label,
		"{group.label}", g.Label,
		"{type}", g.Key,
		"{group}", g.Key,
		"{ticket.id}", ticketID,
		"{dd}", at.Format("02"),
		"{DD}", at.Format("02"),
		"{MM}", at.Format("01"),
		"{yyyy}", at.Format("2006"),
		"{YY}", at.Format("06"),
		"{hh}", at.Format("15"),
		"{mm}", at.Format("04"),
		"{ss}", at.Format("05"),
	)
	base := sanitizeChannelName(repl.Replace(tmpl))
	name := base
	for n := 2; taken[name]; n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	taken[name] = true
	return name
}

// takenChannelNames lists channel names already used by live tickets in this
// guild, via the store's own lock (never touch store.tickets directly).
func (m *TicketsModule) takenChannelNames() map[string]bool {
	out := map[string]bool{}
	for _, tk := range m.store.openTicketsSnapshot() {
		for _, t := range tk {
			if t.ChannelName != "" {
				out[t.ChannelName] = true
			}
		}
	}
	return out
}

// memberHasRole checks a single role membership via the bot's member fetch.
func (m *TicketsModule) memberHasRole(guildID, userID, roleID string) bool {
	return m.memberHasAnyRole(guildID, userID, []string{roleID})
}

// memberHasAnyRole fetches the member ONCE and compares against the wanted
// role set (avoids N REST calls for N roles).
func (m *TicketsModule) memberHasAnyRole(guildID, userID string, roleIDs []string) bool {
	gid, e1 := snowflake.Parse(guildID)
	uid, e2 := snowflake.Parse(userID)
	if e1 != nil || e2 != nil || len(roleIDs) == 0 {
		return false
	}
	mem, err := m.ctx.Rest.GetMember(gid, uid)
	if err != nil || mem == nil {
		return false
	}
	held := make(map[snowflake.ID]bool, len(mem.RoleIDs))
	for _, r := range mem.RoleIDs {
		held[r] = true
	}
	for _, want := range roleIDs {
		if rid, err := snowflake.Parse(want); err == nil && held[rid] {
			return true
		}
	}
	return false
}

// overwritesFor wires the pure builder to module state (bot self ID).
func (m *TicketsModule) overwritesFor(guildID, openerID string, g TypeConfig, members []string, closed bool) []discord.PermissionOverwrite {
	m.mu.RLock()
	self := m.botSelfID
	m.mu.RUnlock()
	return overwritesFor(guildID, openerID, g.HelperRoles, members, closed, self)
}

// openTicket creates the private channel, posts ping + welcome embed and
// persists the ticket. Sequence is RESERVED before any API work (v1 lesson).
// answers (from the open-time modal) are posted as a follow-up message and
// recorded in the ticket log.
func (m *TicketsModule) openTicket(g TypeConfig, opener discord.User, guildID, panelName string, answers []answer) (*modules.Ticket, error) {
	seq := m.store.reserveSeq(guildID, g.Key)
	release := func() { m.store.releaseSeq(guildID, g.Key, seq) }

	ticket := &modules.Ticket{
		ID: fmt.Sprintf("%s-%04d", g.Key, seq), Type: g.Key, Group: g.Key,
		GuildID: guildID, OpenerID: opener.ID.String(), Status: "open",
		OpenedAt: time.Now().UTC(), PanelName: panelName,
	}
	category, err := snowflake.Parse(g.Category)
	if err != nil {
		release()
		return nil, fmt.Errorf("type %q has an invalid category", g.Key)
	}
	if len(g.AccessRoles) > 0 && !m.openerHasAccess(guildID, opener.ID.String(), g.AccessRoles) {
		release()
		return nil, fmt.Errorf("you don't have permission to open this type of ticket")
	}

	name := buildChannelName(g.ChannelName, opener.EffectiveName(), g, ticket, time.Now(), m.takenChannelNames())
	ticket.ChannelName = name
	ch, err := m.ctx.Rest.CreateGuildChannel(snowflake.MustParse(guildID), discord.GuildTextChannelCreate{
		Name:                 name,
		Topic:                "misfit-ticket:" + ticket.ID,
		ParentID:             category,
		PermissionOverwrites: m.overwritesFor(guildID, opener.ID.String(), g, nil, false),
	})
	if err != nil {
		release()
		return nil, fmt.Errorf("failed to create ticket channel: %w", err)
	}
	ticket.ChannelID = ch.ID().String()

	guildName := m.resolveGuildName(guildID)
	fresh := m.fetchOpenerUser(guildID, ticket.OpenerID, opener)
	welcome := renderTemplate(
		firstNonEmpty(g.WelcomeMsg, "Welcome {user.mention}! Please describe your issue."),
		ticket, groupFromType(g), fresh, guildName)
	create := discord.MessageCreate{
		Content:    buildPingLine(g.PingRoles, ticket.OpenerID) + "\n" + welcome,
		Embeds:     []discord.Embed{buildOpenEmbed(ticket, g, fresh, guildName)},
		Components: ticketButtons(ticket, g),
	}
	if welcomeMsg, err := m.ctx.Rest.CreateMessage(ch.ID(), create); err != nil {
		m.ctx.Logger.Error("Tickets: welcome post failed in %s: %v", ticket.ChannelID, err)
	} else if welcomeMsg != nil {
		ticket.MessageID = welcomeMsg.ID.String()
	}

	// Open-time answers: post as a follow-up and record in the ticket log.
	if len(answers) > 0 {
		content := buildAnswersContent(opener.EffectiveName(), answers)
		resp, err := m.ctx.Rest.CreateMessage(ch.ID(), discord.MessageCreate{
			Content:         content,
			AllowedMentions: &discord.AllowedMentions{Parse: []discord.AllowedMentionType{}},
		})
		if err == nil {
			ticket.Log = append(ticket.Log, modules.LogEntry{
				MsgID:      resp.ID.String(),
				AuthorID:   opener.ID.String(),
				AuthorName: m.ctx.Bot.GetName(),
				IsBot:      true,
				Timestamp:  time.Now().UTC(),
				Content:    content,
			})
		} else {
			m.ctx.Logger.Warn("Tickets: answers post failed in %s: %v", ticket.ChannelID, err)
		}
	}
	if err := m.store.save(ticket); err != nil {
		release()
		// The channel already exists but has no ticket record: leaving it
		// behind orphans a channel whose topic points at a nonexistent ticket.
		if delErr := m.ctx.Rest.DeleteChannel(ch.ID()); delErr != nil {
			m.ctx.Logger.Warn("Tickets: failed to delete orphaned channel %s: %v", ticket.ChannelID, delErr)
		}
		return nil, fmt.Errorf("failed to persist ticket: %w", err)
	}

	// Log-channel "opened" event.
	title, desc := openLogEmbed(ticket, g, panelName)
	m.postLogEvent(guildID, title, desc, 0x57F287)
	return ticket, nil
}

// openerHasAccess reports whether the user holds any access role OR is
// owner/elevated (they always pass; mods pass via helper roles normally).
// The member record is fetched ONCE and the role set compared locally.
func (m *TicketsModule) openerHasAccess(guildID, userID string, accessRoles []string) bool {
	if m.ctx.Bot.IsOwner(userID) || m.ctx.Bot.IsElevated(userID) {
		return true
	}
	return m.memberHasAnyRole(guildID, userID, accessRoles)
}

func buildPingLine(pingRoles []string, openerID string) string {
	var b strings.Builder
	for _, r := range pingRoles {
		b.WriteString("<@&" + r + "> ")
	}
	b.WriteString("<@" + openerID + ">")
	return b.String()
}
