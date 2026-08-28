package tickets

import (
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/embed"
	"github.com/misfit/bot/modules"
)

var discordPermManageMessages = discord.PermissionManageMessages

func timeNowUTC() time.Time { return time.Now().UTC() }

// memberPermissions computes a member's base guild permissions via REST.
// Discord does NOT include @everyone in member.roles, so its bits are added
// explicitly (role ID == guild ID).
func (m *TicketsModule) memberPermissions(guildID, userID string) (discord.Permissions, bool) {
	gid, e1 := snowflake.Parse(guildID)
	uid, e2 := snowflake.Parse(userID)
	if e1 != nil || e2 != nil {
		return 0, false
	}
	mem, err := m.ctx.Rest.GetMember(gid, uid)
	if err != nil || mem == nil {
		return 0, false
	}
	guild, err := m.ctx.Rest.GetGuild(gid, false)
	if err != nil || guild == nil {
		return 0, false
	}
	var perms discord.Permissions
	for _, r := range guild.Roles {
		if r.ID == gid { // @everyone shares the guild ID
			perms |= r.Permissions
			continue
		}
		for _, mr := range mem.RoleIDs {
			if r.ID == mr {
				perms |= r.Permissions
			}
		}
	}
	if perms.Has(discord.PermissionAdministrator) {
		return discord.PermissionsAll, true
	}
	return perms, perms != 0
}

// applyMemberOverwrite updates ONE member's overwrite on the ticket channel.
// disgo's UpdateChannel REPLACES the whole permission_overwrites list, so we
// must read the current set, modify just this member's entry, and write the
// complete list back — otherwise @everyone/bot/opener/helper overwrites are
// wiped and everyone loses access to the ticket.
func (m *TicketsModule) applyMemberOverwrite(tk *modules.Ticket, g TypeConfig, memberID string, grant bool) error {
	cid, e1 := snowflake.Parse(tk.ChannelID)
	uid, e2 := snowflake.Parse(memberID)
	if e1 != nil || e2 != nil {
		return fmt.Errorf("bad IDs")
	}
	ch, err := m.ctx.Rest.GetChannel(cid)
	if err != nil || ch == nil {
		return fmt.Errorf("channel fetch failed: %w", err)
	}
	if _, ok := ch.(discord.GuildTextChannel); !ok {
		return fmt.Errorf("not a text channel")
	}
	// Rebuild the full overwrite set from config, then layer the member change.
	members := make([]string, 0, len(tk.Members)+1)
	for _, id := range tk.Members {
		if id != memberID {
			members = append(members, id)
		}
	}
	if grant {
		members = append(members, memberID)
	}
	full := m.overwritesFor(tk.GuildID, tk.OpenerID, g, members, tk.Status != "open")
	if !grant {
		// Removed members get an explicit view-deny so they lose access even
		// if role overwrites would otherwise let them in.
		full = append(full, discord.MemberPermissionOverwrite{UserID: uid, Deny: permView | permHistory})
	}
	_, err = m.ctx.Rest.UpdateChannel(cid, discord.GuildTextChannelUpdate{
		PermissionOverwrites: &full,
	})
	return err
}

// inchannel.go — [p]close / [p]claim / [p]add / [p]remove: usable ONLY inside
// a ticket channel (ticket resolved from the channel topic, zero arguments).
// Gates: close/claim need helper-role/mods/owner-elevated; opener may close
// their own ticket when the type allows it.

// ticketFromChannel resolves the open ticket owning this channel via topic.
func (m *TicketsModule) ticketFromChannel(ctx *commands.Context) *modules.Ticket {
	if ctx.GuildID == "" || ctx.ChannelID == "" {
		return nil
	}
	cid, err := snowflake.Parse(ctx.ChannelID)
	if err != nil {
		return nil
	}
	ch, err := m.ctx.Rest.GetChannel(cid)
	if err != nil || ch == nil {
		return nil
	}
	tc, ok := ch.(interface{ Topic() string })
	if !ok {
		return nil
	}
	const prefix = "misfit-ticket:"
	topic := tc.Topic()
	if !strings.HasPrefix(topic, prefix) {
		return nil
	}
	ticketID := strings.TrimPrefix(topic, prefix)
	if !validTicketID(ticketID) {
		return nil
	}
	tk, err := m.store.load(ctx.GuildID, ticketID)
	if err != nil || tk == nil {
		return nil
	}
	return tk
}

// isHelper reports whether userID may manage this ticket: helper role of its
// type, moderator perms in the channel, or owner/elevated.
func (m *TicketsModule) isHelper(ctx *commands.Context, tk *modules.Ticket, userID string) bool {
	if m.ctx.Bot.IsOwner(userID) || m.ctx.Bot.IsElevated(userID) {
		return true
	}
	g, ok := m.typeOf(tk.EffectiveType())
	if !ok {
		return false
	}
	if m.memberHasAnyRole(ctx.GuildID, userID, g.HelperRoles) {
		return true
	}
	// Moderators (ManageMessages) always pass.
	if perms, ok := m.memberPermissions(ctx.GuildID, userID); ok && perms.Has(discordPermManageMessages) {
		return true
	}
	return false
}

func isHelperByType(m *TicketsModule, tk *modules.Ticket, userID string) bool {
	g, ok := m.typeOf(tk.EffectiveType())
	if !ok {
		return false
	}
	return m.memberHasAnyRole(tk.GuildID, userID, g.HelperRoles)
}

func (m *TicketsModule) inChannelCommands() []commands.Command {
	closeCmd := commands.Command{
		Name: "close", Description: "Close this ticket (inside the ticket channel only)",
		Usage: "close", Category: "Tickets",
		Execute: func(ctx *commands.Context) error {
			tk := m.ticketFromChannel(ctx)
			if tk == nil || tk.Status != "open" {
				return ctx.Respond(embed.Error("❌ Error", "This command only works inside an **open** ticket channel."))
			}
			userID := ctx.Author.ID.String()
			isOpener := userID == tk.OpenerID
			if !isOpener && !m.isHelper(ctx, tk, userID) {
				return ctx.Respond(embed.Error("❌ Error", "Only helpers or the ticket opener can close."))
			}
			if err := m.CloseTicket(ctx.GuildID, tk.ID, userID); err != nil {
				return ctx.Respond(embed.Error("❌ Error", "Failed to close: "+err.Error()))
			}
			return ctx.Respond(embed.Success("🔒 Ticket closed", "`"+tk.ID+"` — transcript saved to the log channel and dashboard."))
		},
	}
	claimCmd := commands.Command{
		Name: "claim", Description: "Claim this ticket (helpers only)",
		Usage: "claim", Category: "Tickets",
		Execute: func(ctx *commands.Context) error {
			tk := m.ticketFromChannel(ctx)
			if tk == nil || tk.Status != "open" {
				return ctx.Respond(embed.Error("❌ Error", "This command only works inside an **open** ticket channel."))
			}
			userID := ctx.Author.ID.String()
			if !m.isHelper(ctx, tk, userID) {
				return ctx.Respond(embed.Error("❌ Error", "Only helpers can claim tickets."))
			}
			m.mu.Lock()
			if tk.ClaimerID != "" {
				claimed := tk.ClaimerID
				m.mu.Unlock()
				return ctx.Respond(embed.Warning("⚠️ Already claimed", "Claimed by <@"+claimed+">."))
			}
			tk.ClaimerID = userID
			tk.ClaimedAt = timeNowUTC()
			m.mu.Unlock()
			g, _ := m.typeOf(tk.EffectiveType())
			if tk.MessageID != "" {
				m.editTicketButtons(tk, g, "Claimed by "+ctx.Author.EffectiveName())
			}
			_ = m.store.save(tk)
			return ctx.Respond(embed.Success("✋ Claimed", "<@"+userID+"> is handling this ticket."))
		},
	}
	addCmd := commands.Command{
		Name: "add", Description: "Add a member to this ticket",
		Usage: "add <@member>", Category: "Tickets",
		Execute: func(ctx *commands.Context) error {
			return m.addRemoveMember(ctx, true)
		},
	}
	removeCmd := commands.Command{
		Name: "remove", Description: "Remove a member from this ticket",
		Usage: "remove <@member>", Category: "Tickets",
		Execute: func(ctx *commands.Context) error {
			return m.addRemoveMember(ctx, false)
		},
	}
	return []commands.Command{closeCmd, claimCmd, addCmd, removeCmd}
}

// addRemoveMember grants/revokes a member's individual overwrite + records it.
func (m *TicketsModule) addRemoveMember(ctx *commands.Context, add bool) error {
	tk := m.ticketFromChannel(ctx)
	if tk == nil {
		return ctx.Respond(embed.Error("❌ Error", "This command only works inside a ticket channel."))
	}
	if len(ctx.Args) < 1 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "[prefix]"+map[bool]string{true: "add ", false: "remove "}[add]+"<@member>"))
	}
	userID := ctx.Author.ID.String()
	if !m.isHelper(ctx, tk, userID) {
		return ctx.Respond(embed.Error("❌ Error", "Only helpers can add/remove members."))
	}
	target := extractSnowflake(strings.Join(ctx.Args, " "))
	if target == "" {
		return ctx.Respond(embed.Error("❌ Error", "Mention the member (@user) or paste their ID."))
	}
	if target == tk.OpenerID {
		return ctx.Respond(embed.Error("❌ Error", "The ticket owner can't be removed."))
	}

	m.mu.Lock()
	idx := -1
	for i, id := range tk.Members {
		if id == target {
			idx = i
			break
		}
	}
	if add && idx >= 0 {
		m.mu.Unlock()
		return ctx.Respond(embed.Warning("⚠️", "<@"+target+"> is already in this ticket."))
	}
	if !add && idx < 0 {
		m.mu.Unlock()
		return ctx.Respond(embed.Warning("⚠️", "<@"+target+"> is not in this ticket."))
	}
	if add {
		tk.Members = append(tk.Members, target)
	} else {
		tk.Members = append(tk.Members[:idx], tk.Members[idx+1:]...)
	}
	err := m.store.save(tk)
	m.mu.Unlock()
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", err.Error()))
	}

	// Live overwrite update on the channel.
	g, _ := m.typeOf(tk.EffectiveType())
	verb := "added to"
	if !add {
		verb = "removed from"
	}
	if err := m.applyMemberOverwrite(tk, g, target, add); err != nil {
		m.ctx.Logger.Warn("Tickets: overwrite update failed for %s: %v", target, err)
	}
	action := "added"
	if !add {
		action = "removed"
	}
	return ctx.Respond(embed.Success("✅ Member "+action, "<@"+target+"> "+verb+" ticket `"+tk.ID+"`."))
}
