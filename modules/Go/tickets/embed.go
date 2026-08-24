package main

import (
	"time"

	"github.com/disgoorg/disgo/discord"

	"github.com/misfit/bot/modules"
)

// buildOpenEmbed assembles the ticket-open embed: templated body, group
// color, opener avatar as thumbnail (top-right), author = opener identity.
func buildOpenEmbed(t *modules.Ticket, g GroupConfig, opener discord.User, guildName string) discord.Embed {
	body := renderTemplate(g.EmbedTemplate, t, g, opener, guildName)
	e := discord.NewEmbed().
		WithDescription(body).
		WithColor(int(g.Color)).
		WithTimestamp(time.Now()).
		WithThumbnail(opener.EffectiveAvatarURL(discord.WithSize(512))).
		WithAuthor(opener.EffectiveName(), "", opener.EffectiveAvatarURL(discord.WithSize(128)))
	if t != nil {
		e = e.WithFooter("Ticket "+t.ID, "")
	}
	return e
}
