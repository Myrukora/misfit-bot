package dashboard

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// ── Guild-scoped authority helpers ────────────────────────────────────────
//
// The dashboard routes are authorized for one guild, but module settings can
// name channels and roles in arbitrary guilds. These helpers bind those
// resource IDs to the configured guild where the bot cache makes that
// possible. Unknown IDs are left to the module's own snowflake validation
// (the cache is expected to contain every channel/role of every bot guild);
// known IDs from another guild are always rejected.

// checkChannelInGuild rejects a known channel that belongs to another guild.
func (m *DashboardModule) checkChannelInGuild(guildID, channelID string) error {
	channelID = strings.TrimSpace(channelID)
	if strings.HasPrefix(channelID, "<#") && strings.HasSuffix(channelID, ">") {
		channelID = strings.TrimSuffix(strings.TrimPrefix(channelID, "<#"), ">")
	}
	if channelID == "" {
		return nil
	}
	cid, err := snowflake.Parse(channelID)
	if err != nil || cid == 0 {
		return nil // the module validates the snowflake and rejects bad IDs
	}
	if m.client == nil {
		return nil
	}
	if m.client.Caches != nil {
		if ch, ok := m.client.Caches.Channel(cid); ok {
			if ch.GuildID().String() != guildID {
				return fmt.Errorf("channel does not belong to this server")
			}
			return nil
		}
	}
	if m.client.Rest == nil {
		return nil
	}
	ch, err := m.client.Rest.GetChannel(cid)
	if err != nil || ch == nil {
		return nil
	}
	gch, ok := ch.(discord.GuildChannel)
	if !ok || gch.GuildID().String() != guildID {
		return fmt.Errorf("channel does not belong to this server")
	}
	return nil
}

// checkRoleGrant rejects a configured role that is at or above the requesting
// user's highest role. The guild owner and the bot operator bypass; holding
// ManageRoles/Administrator does not bypass Discord's role-position hierarchy.
// Unknown roles/members fall through to the module/Discord validation because
// the bot cache is expected to contain complete guild role/member data; if it
// is unavailable the dashboard is in gated mode and staff cannot reach this
// path.
func (m *DashboardModule) checkRoleGrant(us *userSession, guildID, roleID string) error {
	roleID = strings.TrimSpace(roleID)
	if roleID == "" {
		return nil
	}
	if us == nil {
		return fmt.Errorf("not logged in")
	}
	userID := us.userID.String()
	if m.bot.IsOwner(userID) || m.bot.GetGuildOwnerID(guildID) == userID {
		return nil
	}
	if m.client == nil || m.client.Caches == nil {
		return nil
	}
	gid, err := snowflake.Parse(guildID)
	if err != nil || gid == 0 {
		return nil
	}
	rid, err := snowflake.Parse(roleID)
	if err != nil || rid == 0 {
		return nil
	}
	role, ok := m.client.Caches.Role(gid, rid)
	if !ok {
		return nil
	}
	if role.ID == gid {
		return fmt.Errorf("the @everyone role cannot be granted")
	}
	if role.Managed {
		return fmt.Errorf("the role is managed by an integration")
	}
	uid, err := snowflake.Parse(userID)
	if err != nil || uid == 0 {
		return fmt.Errorf("invalid user id")
	}
	member, ok := m.client.Caches.Member(gid, uid)
	if !ok {
		return nil
	}
	top := 0
	for _, memberRoleID := range member.RoleIDs {
		if memberRole, ok := m.client.Caches.Role(gid, memberRoleID); ok && memberRole.Position > top {
			top = memberRole.Position
		}
	}
	if role.Position >= top {
		return fmt.Errorf("you cannot grant a role at or above your own highest role")
	}
	return nil
}

// punishmentAllowed reports whether the requesting dashboard user may configure
// the image filter to apply the given punishment. The bot operator bypasses;
// everyone else needs the matching Discord permission (the guild owner and
// Administrator receive PermissionsAll from MemberPermissions).
func (m *DashboardModule) punishmentAllowed(us *userSession, guildID, punishment string) bool {
	if us == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(punishment)) {
	case "", "none":
		return true
	case "ban":
		return m.bot.IsOwner(us.userID.String()) || m.bot.GetUserPermissions(us.userID.String(), guildID).Has(discord.PermissionBanMembers)
	case "kick":
		return m.bot.IsOwner(us.userID.String()) || m.bot.GetUserPermissions(us.userID.String(), guildID).Has(discord.PermissionKickMembers)
	case "mute":
		return m.bot.IsOwner(us.userID.String()) || m.bot.GetUserPermissions(us.userID.String(), guildID).Has(discord.PermissionModerateMembers)
	default:
		return false
	}
}
