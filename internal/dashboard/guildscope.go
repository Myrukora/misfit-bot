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
//
// The check is fail-closed: when the member or any of their roles is missing
// from the gateway cache, it resolves them over REST once. If that lookup
// fails, the write is refused rather than allowed, because an uncached
// hierarchy comparison could otherwise let a staff member grant a role that
// outranks them (reaction roles and leveling rewards both grant roles).
func (m *DashboardModule) checkRoleGrant(us *userSession, guildID, roleID string) error {
	roleID = strings.TrimSpace(roleID)
	if roleID == "" {
		return nil
	}
	if us == nil {
		return fmt.Errorf("not logged in")
	}
	userID := us.userID.String()
	if m.bot != nil && (m.bot.IsOwner(userID) || m.bot.GetGuildOwnerID(guildID) == userID) {
		return nil
	}
	if m.client == nil || m.client.Caches == nil {
		return fmt.Errorf("could not verify the role hierarchy, try again")
	}
	gid, err := snowflake.Parse(guildID)
	if err != nil || gid == 0 {
		return fmt.Errorf("invalid guild id")
	}
	rid, err := snowflake.Parse(roleID)
	if err != nil || rid == 0 {
		return fmt.Errorf("invalid role id")
	}
	role, ok := m.client.Caches.Role(gid, rid)
	var knownRoles []discord.Role
	if !ok {
		knownRoles, err = m.restRoles(gid)
		if err != nil {
			return fmt.Errorf("could not verify the role hierarchy, try again")
		}
		found := false
		for i := range knownRoles {
			if knownRoles[i].ID == rid {
				role = knownRoles[i]
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("could not verify the role hierarchy, try again")
		}
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
	var roleIDs []snowflake.ID
	if member, ok := m.client.Caches.Member(gid, uid); ok {
		roleIDs = member.RoleIDs
	} else {
		member, err := m.fetchMember(gid, uid)
		if err != nil || member == nil {
			return fmt.Errorf("could not verify your role hierarchy, try again")
		}
		roleIDs = member.RoleIDs
	}
	top, err := m.highestRolePosition(gid, roleIDs, knownRoles)
	if err != nil {
		return fmt.Errorf("could not verify your role hierarchy, try again")
	}
	if role.Position >= top {
		return fmt.Errorf("you cannot grant a role at or above your own highest role")
	}
	return nil
}

// fetchMember resolves one guild member over REST (used only on a cache miss).
func (m *DashboardModule) fetchMember(guildID, userID snowflake.ID) (*discord.Member, error) {
	if m.client == nil || m.client.Rest == nil {
		return nil, fmt.Errorf("REST unavailable")
	}
	return m.client.Rest.GetMember(guildID, userID)
}

// restRoles fetches the guild's full role list over REST.
func (m *DashboardModule) restRoles(guildID snowflake.ID) ([]discord.Role, error) {
	if m.client == nil || m.client.Rest == nil {
		return nil, fmt.Errorf("REST unavailable")
	}
	return m.client.Rest.GetRoles(guildID)
}

// highestRolePosition returns the highest role position held by a member,
// resolving roles over REST when any of them is missing from the cache. known
// lets a caller pass a role list it already fetched so a single request does
// not pull the same guild twice. It returns an error instead of a partial
// result so callers fail closed.
func (m *DashboardModule) highestRolePosition(guildID snowflake.ID, roleIDs []snowflake.ID, known []discord.Role) (int, error) {
	positions := make(map[snowflake.ID]int, len(roleIDs))
	for _, role := range known {
		positions[role.ID] = role.Position
	}
	missing := false
	for _, id := range roleIDs {
		if _, ok := positions[id]; ok {
			continue
		}
		if role, ok := m.client.Caches.Role(guildID, id); ok {
			positions[id] = role.Position
		} else {
			missing = true
		}
	}
	if missing && known == nil {
		roles, err := m.restRoles(guildID)
		if err != nil {
			return 0, err
		}
		for _, role := range roles {
			positions[role.ID] = role.Position
		}
	}
	top := 0
	for _, id := range roleIDs {
		if pos, ok := positions[id]; ok && pos > top {
			top = pos
		}
	}
	return top, nil
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
