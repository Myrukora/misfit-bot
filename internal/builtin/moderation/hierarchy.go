package moderation

import (
	"fmt"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// getGuild returns the cached guild or fetches it with counts.
func (m *ModerationModule) getGuild(guildID snowflake.ID) (*discord.Guild, error) {
	if g := m.ctx.Bot.GetCachedGuild(guildID.String()); g != nil {
		return g, nil
	}
	if m.ctx.Rest != nil {
		if g, err := m.ctx.Rest.GetGuild(guildID, false); err == nil && g != nil {
			return &g.Guild, nil
		}
	}
	return nil, fmt.Errorf("server is not cached")
}

// getMember returns a member from cache, falling back to REST.
func (m *ModerationModule) getMember(guildID, userID snowflake.ID) (*discord.Member, error) {
	if member := m.ctx.Bot.GetCachedMember(guildID.String(), userID.String()); member != nil {
		return member, nil
	}
	if m.ctx.Rest != nil {
		if member, err := m.ctx.Rest.GetMember(guildID, userID); err == nil && member != nil {
			return member, nil
		}
	}
	return nil, fmt.Errorf("member is not in this server")
}

// getMemberOptional is getMember without an error for an absent member.
func (m *ModerationModule) getMemberOptional(guildID, userID snowflake.ID) *discord.Member {
	member, _ := m.getMember(guildID, userID)
	return member
}

// memberTopPosition is the highest role position the member holds (0 when only
// @everyone). Role objects come from the guild role cache.
func (m *ModerationModule) memberTopPosition(guildID snowflake.ID, member *discord.Member) int {
	if member == nil {
		return 0
	}
	top := 0
	for _, roleID := range member.RoleIDs {
		role := m.ctx.Bot.GetCachedRole(guildID.String(), roleID.String())
		if role != nil && role.Position > top {
			top = role.Position
		}
	}
	return top
}

// ensureCanWarn rejects warnings on yourself and the bot (role hierarchy does
// not matter: a warning is a record, not a Discord punishment).
func (m *ModerationModule) ensureCanWarn(_ snowflake.ID, moderatorID, targetID snowflake.ID) error {
	selfID, _ := snowflake.Parse(m.ctx.Bot.GetSelfUserID())
	if targetID == moderatorID {
		return fmt.Errorf("you cannot warn yourself")
	}
	if targetID == selfID {
		return fmt.Errorf("you cannot warn me")
	}
	if moderatorID == 0 {
		return fmt.Errorf("invalid moderator")
	}
	return nil
}

// ensureCanPunish enforces Discord's role hierarchy before a kick/ban/timeout:
// the target must be below both the moderator and the bot, the target cannot
// be the server owner, yourself or the bot.
func (m *ModerationModule) ensureCanPunish(guildID, moderatorID, targetID snowflake.ID) error {
	selfID, err := snowflake.Parse(m.ctx.Bot.GetSelfUserID())
	if err != nil {
		return fmt.Errorf("bot identity unavailable")
	}
	if targetID == moderatorID {
		return fmt.Errorf("you cannot moderate yourself")
	}
	if targetID == selfID {
		return fmt.Errorf("you cannot moderate me")
	}
	guild, err := m.getGuild(guildID)
	if err != nil {
		return err
	}
	if guild.OwnerID == targetID {
		return fmt.Errorf("you cannot moderate the server owner")
	}
	modMember, err := m.getMember(guildID, moderatorID)
	if err != nil {
		return fmt.Errorf("could not resolve your member record")
	}
	botMember, err := m.getMember(guildID, selfID)
	if err != nil {
		return fmt.Errorf("could not resolve my member record")
	}
	target := m.getMemberOptional(guildID, targetID)
	if target != nil {
		targetTop := m.memberTopPosition(guildID, target)
		if guild.OwnerID != moderatorID && targetTop >= m.memberTopPosition(guildID, modMember) {
			return fmt.Errorf("you cannot moderate a member with an equal or higher role")
		}
		if guild.OwnerID != selfID && targetTop >= m.memberTopPosition(guildID, botMember) {
			return fmt.Errorf("my role is not above that member's highest role")
		}
	}
	return nil
}

// ensureBotPermission checks the bot's own guild permissions before calling
// the mutating endpoint, so the moderator gets a clear error instead of an
// opaque Discord 403.
func (m *ModerationModule) ensureBotPermission(guildID snowflake.ID, perm discord.Permissions) error {
	selfID := m.ctx.Bot.GetSelfUserID()
	if m.ctx.Bot.GetUserPermissions(selfID, guildID.String()).Has(perm) {
		return nil
	}
	return fmt.Errorf("I am missing the **%s** permission", permissionLabel(perm))
}

func permissionLabel(perm discord.Permissions) string {
	switch perm {
	case discord.PermissionBanMembers:
		return "Ban Members"
	case discord.PermissionKickMembers:
		return "Kick Members"
	case discord.PermissionModerateMembers:
		return "Timeout Members"
	default:
		return "required"
	}
}
