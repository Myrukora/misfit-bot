package tickets

import (
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// tokenFetchTimeout bounds REST-driven template work conceptually; disgo
// v0.19.6 owns HTTP timeouts internally, so this only gates retry loops if
// any are added later.
const tokenFetchTimeout = 5 * time.Second

// renderTemplate substitutes {token} placeholders in a group's embed template.
// Pure tokens (ticket facts, opener fields passed in) never touch REST; only
// guild name resolution MAY hit the API, and callers that already know the
// guild name pass it in. Unknown tokens are left verbatim; nil ticket is safe.
func renderTemplate(tmpl string, t *modules.Ticket, g GroupConfig, opener discord.User, guildName string) string {
	if tmpl == "" {
		return ""
	}
	displayName := opener.EffectiveName()
	mention := "<@" + opener.ID.String() + ">"

	repl := strings.NewReplacer(
		"{user}", displayName,
		"{user.mention}", mention,
		"{user.id}", opener.ID.String(),
		"{user.name}", displayName,
		"{user.avatar}", avatarURL(opener),
		"{guild.name}", guildName,
		"{guild.id}", guildIDString(t),
		"{ticket.id}", ticketString(t),
		"{ticket.group}", g.Key,
		"{group}", g.Label,
		"{ticket.channel}", channelString(t),
		"{opener}", displayName,
		"{opener.mention}", mention,
	)
	return repl.Replace(tmpl)
}

// resolveGuildName fetches the guild name via REST; on failure it returns ""
// so templates just omit it rather than erroring the open flow. (disgo
// v0.19.6 REST calls carry no context — its HTTP client owns timeouts.)
func (m *TicketsModule) resolveGuildName(guildID string) string {
	id, err := snowflake.Parse(guildID)
	if err != nil {
		return ""
	}
	guild, err := m.ctx.Rest.GetGuild(id, false)
	if err != nil || guild == nil {
		return ""
	}
	return guild.Name
}

// fetchOpenerUser re-fetches the opener for a fresh avatar/name, falling back
// to the interaction's member user on any error.
func (m *TicketsModule) fetchOpenerUser(guildID, userID string, fallback discord.User) discord.User {
	gid, err1 := snowflake.Parse(guildID)
	uid, err2 := snowflake.Parse(userID)
	if err1 != nil || err2 != nil {
		return fallback
	}
	member, err := m.ctx.Rest.GetMember(gid, uid)
	if err != nil || member == nil {
		return fallback
	}
	return member.User
}

func avatarURL(u discord.User) string {
	return u.EffectiveAvatarURL(discord.WithSize(512))
}

func ticketString(t *modules.Ticket) string {
	if t == nil {
		return ""
	}
	return t.ID
}

func guildIDString(t *modules.Ticket) string {
	if t == nil {
		return ""
	}
	return t.GuildID
}

func channelString(t *modules.Ticket) string {
	if t == nil {
		return ""
	}
	return "<#" + t.ChannelID + ">"
}
