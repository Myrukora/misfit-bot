// Package discordutil holds small Discord-type rendering helpers shared by the
// dashboard and the MCP server without either package importing the other.
package discordutil

import "github.com/disgoorg/disgo/discord"

// ChannelTypeName renders a channel type as a short human label.
func ChannelTypeName(t discord.ChannelType) string {
	switch t {
	case discord.ChannelTypeGuildText:
		return "Text"
	case discord.ChannelTypeGuildVoice:
		return "Voice"
	case discord.ChannelTypeGuildCategory:
		return "Category"
	case discord.ChannelTypeGuildNews:
		return "Announcement"
	case discord.ChannelTypeGuildStageVoice:
		return "Stage"
	case discord.ChannelTypeGuildForum:
		return "Forum"
	case discord.ChannelTypeGuildPublicThread:
		return "Public Thread"
	case discord.ChannelTypeGuildPrivateThread:
		return "Private Thread"
	case discord.ChannelTypeGuildNewsThread:
		return "Announcement Thread"
	default:
		return "Other"
	}
}
