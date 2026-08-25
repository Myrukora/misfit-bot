package main

import (
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// overwrite.go — channel permission overwrites, following AAA3A's proven
// recipe. Pure functions so they are unit-testable without Discord.

const (
	permView    = discord.PermissionViewChannel
	permHistory = discord.PermissionReadMessageHistory
	permSend    = discord.PermissionSendMessages
	permReact   = discord.PermissionAddReactions
	permFiles   = discord.PermissionAttachFiles
	permEmbeds  = discord.PermissionEmbedLinks

	denyClosed = permSend | permReact | permFiles | permEmbeds
)

// interactionPerms builds the allow/deny pair for a participant; closed
// tickets keep history readable but strip write perms.
func interactionPerms(closed bool) (allow, deny discord.Permissions) {
	if closed {
		return permView | permHistory, denyClosed
	}
	return permView | permHistory | permSend | permReact | permFiles | permEmbeds, 0
}

// overwritesFor computes the full overwrite set for a fresh ticket channel:
//
//	@everyone: deny everything visible
//	bot:       full management access
//	opener:    read + send (subject to closed)
//	helper roles: read + send (subject to closed)
//	members:   per-member read + send ([p]add)
func overwritesFor(guildID, openerID string, helperRoles, memberIDs []string, closed bool, botSelfID string) []discord.PermissionOverwrite {
	everyoneID, _ := snowflake.Parse(guildID) // @everyone role shares the guild ID
	out := []discord.PermissionOverwrite{
		RolePermissionOverwrite(everyoneID,
			0, permView|permHistory|permSend|permReact|permFiles|permEmbeds),
	}
	allow, deny := interactionPerms(closed)

	// The bot itself needs to manage the channel.
	if botSelfID != "" {
		if botID, err := snowflake.Parse(botSelfID); err == nil {
			out = append(out, discord.MemberPermissionOverwrite{
				UserID: botID,
				Allow: permView | permHistory | permSend | permReact | permFiles | permEmbeds |
					discord.PermissionManageChannels | discord.PermissionManageMessages,
			})
		}
	}

	if oid, err := snowflake.Parse(openerID); err == nil {
		out = append(out, discord.MemberPermissionOverwrite{UserID: oid, Allow: allow, Deny: deny})
	}
	for _, rid := range helperRoles {
		id, err := snowflake.Parse(rid)
		if err != nil {
			continue
		}
		out = append(out, discord.RolePermissionOverwrite{RoleID: id, Allow: allow, Deny: deny})
	}
	for _, mid := range memberIDs {
		id, err := snowflake.Parse(mid)
		if err != nil {
			continue
		}
		out = append(out, discord.MemberPermissionOverwrite{UserID: id, Allow: allow, Deny: deny})
	}
	return out
}

// RolePermissionOverwrite is a positional constructor keeping call sites tidy.
func RolePermissionOverwrite(roleID snowflake.ID, allow, deny discord.Permissions) discord.RolePermissionOverwrite {
	return discord.RolePermissionOverwrite{RoleID: roleID, Allow: allow, Deny: deny}
}
