package tickets

import (
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"

	"github.com/misfit/bot/modules"
)

// buildOpenEmbed assembles the in-channel open embed: templated body, type
// color, opener avatar thumbnail + author identity, ticket ID footer.
func buildOpenEmbed(t *modules.Ticket, g TypeConfig, opener discord.User, guildName string) discord.Embed {
	gv := groupFromType(g)
	body := renderTemplate(g.EmbedBody, t, gv, opener, guildName)
	if body == "" {
		body = renderTemplate("{user} opened a **{group}** ticket.", t, gv, opener, guildName)
	}
	color := int(g.Color)
	if color == 0 {
		color = defaultTicketColor
	}
	e := discord.NewEmbed().
		WithDescription(body).
		WithColor(color).
		WithTimestamp(time.Now()).
		WithThumbnail(opener.EffectiveAvatarURL(discord.WithSize(512))).
		WithAuthor(opener.EffectiveName(), "", opener.EffectiveAvatarURL(discord.WithSize(128)))
	if t != nil {
		e = e.WithFooter("Ticket "+t.ID, "")
	}
	return e
}

// ── small local embed builders (interaction replies) ─────────────────────

func embedSuccess(title, desc string) discord.Embed {
	return discord.NewEmbed().WithDescription("**" + title + "**\n" + desc).WithColor(0x57F287)
}

func embedError(title, desc string) discord.Embed {
	return discord.NewEmbed().WithDescription("**" + title + "**\n" + desc).WithColor(0xED4245)
}

func embedInfo(title, desc string) discord.Embed {
	return discord.NewEmbed().WithDescription("**" + title + "**\n" + desc).WithColor(0x5865F2)
}

// groupFromType adapts a TypeConfig to the template engine's GroupConfig
// view. Label falls back to the key so {group}/{type} never render empty.
func groupFromType(g TypeConfig) GroupConfig {
	label := g.Label
	if strings.TrimSpace(label) == "" {
		label = g.Key
	}
	return GroupConfig{Key: g.Key, Label: label}
}
