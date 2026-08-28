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
// (v2; v1 used threads). Named {opener}-{MM-DD-YY}, collision-suffixed. The
// ticket ID lives in the channel topic so [p]close/[p]claim need no args.

// ticketButtons builds the Claim/Close action row inside a ticket channel.
func ticketButtons(t *modules.Ticket, g TypeConfig, claimLabel string) []discord.LayoutComponent {
	var buttons []discord.InteractiveComponent
	if g.AllowClaimOn() {
		buttons = append(buttons, discord.ButtonComponent{
			Style: discord.ButtonStylePrimary, Label: claimLabel,
			CustomID: "tickets:claim:" + t.ID, Disabled: claimLabel != "Claim",
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

func boolPtr(b bool) *bool { return &b }

// sanitizeChannelName maps an opener display name to a Discord-safe slug.
func sanitizeChannelName(name string) string {
	base := strings.ToLower(strings.SplitN(name, "#", 2)[0])
	var clean strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			clean.WriteRune(r)
		case r == ' ':
			clean.WriteRune('-')
		}
	}
	s := strings.Trim(clean.String(), "-")
	if s == "" {
		s = "ticket"
	}
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

// buildChannelName = "{opener}-{MM-DD-YY}"; -2/-3… suffix on same-day reuse.
func buildChannelName(openerName string, at time.Time, taken map[string]bool) string {
	base := fmt.Sprintf("%s-%s", sanitizeChannelName(openerName), at.Format("01-02-06"))
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
func (m *TicketsModule) openTicket(g TypeConfig, opener discord.User, guildID, panelName string) (*modules.Ticket, error) {
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

	name := buildChannelName(opener.EffectiveName(), time.Now(), m.takenChannelNames())
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
		Content: buildPingLine(g.PingRoles, ticket.OpenerID) + "\n" + welcome,
		Embeds:  []discord.Embed{buildOpenEmbed(ticket, g, fresh, guildName)},
	}
	if _, err := m.ctx.Rest.CreateMessage(ch.ID(), create); err != nil {
		m.ctx.Logger.Error("Tickets: welcome post failed in %s: %v", ticket.ChannelID, err)
	}
	if err := m.store.save(ticket); err != nil {
		release()
		return nil, fmt.Errorf("failed to persist ticket: %w", err)
	}
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
