package modules

import (
	"time"

	"github.com/disgoorg/disgo/discord"
)

// luaEmbed creates a Discord embed from title and description strings.
func luaEmbed(title, description string) discord.Embed {
	return discord.NewEmbed().
		WithTitle(title).
		WithDescription(description).
		WithColor(0x5865F2). // Discord blurple
		WithTimestamp(time.Now())
}
