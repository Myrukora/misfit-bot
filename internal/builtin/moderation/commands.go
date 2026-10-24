package moderation

import (
	"fmt"
	"strconv"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/embed"
	"github.com/misfit/bot/internal/util"
	"github.com/misfit/bot/modules"
)

func intPtr(n int) *int { return &n }

func (m *ModerationModule) slashCommands() []commands.SlashCommand {
	userOpt := func(desc string) discord.ApplicationCommandOptionUser {
		return discord.ApplicationCommandOptionUser{Name: "user", Description: desc, Required: true}
	}
	reasonOpt := func(required bool) discord.ApplicationCommandOptionString {
		return discord.ApplicationCommandOptionString{
			Name: "reason", Description: "Why this action is being taken", Required: required, MaxLength: intPtr(450),
		}
	}
	return []commands.SlashCommand{
		{
			Name:         "ban",
			Description:  "Ban a member from this server.",
			Category:     "moderation",
			RequiredPerm: discord.PermissionBanMembers,
			Options: []discord.ApplicationCommandOption{
				userOpt("Member to ban"),
				reasonOpt(false),
			},
			Execute: func(ctx *commands.Context) error {
				cfg, gid, err := m.configFor(ctx)
				if err != nil {
					return ctx.Respond(embed.Error("Moderation", err.Error()))
				}
				target, err := parseUserArg(ctx.Args, 0)
				if err != nil {
					return ctx.Respond(embed.Error("Ban failed", err.Error()))
				}
				c, err := m.ban(cfg, gid, ctx.Author.ID, ctx.Author.Tag(), target, argAt(ctx.Args, 1))
				if err != nil {
					return ctx.Respond(embed.Error("Ban failed", err.Error()))
				}
				return ctx.Respond(embed.Success("Member banned", actionSummary(c)))
			},
		},
		{
			Name:         "unban",
			Description:  "Lift a ban using the user ID.",
			Category:     "moderation",
			RequiredPerm: discord.PermissionBanMembers,
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{Name: "user_id", Description: "ID of the banned user", Required: true},
				reasonOpt(false),
			},
			Execute: func(ctx *commands.Context) error {
				cfg, gid, err := m.configFor(ctx)
				if err != nil {
					return ctx.Respond(embed.Error("Moderation", err.Error()))
				}
				raw := util.ExtractID(argAt(ctx.Args, 0))
				target, err := snowflake.Parse(raw)
				if err != nil || target == 0 {
					return ctx.Respond(embed.Error("Unban failed", "That is not a valid user ID."))
				}
				c, err := m.unban(cfg, gid, ctx.Author.ID, ctx.Author.Tag(), target, argAt(ctx.Args, 1))
				if err != nil {
					return ctx.Respond(embed.Error("Unban failed", err.Error()))
				}
				return ctx.Respond(embed.Success("Member unbanned", actionSummary(c)))
			},
		},
		{
			Name:         "kick",
			Description:  "Kick a member from this server.",
			Category:     "moderation",
			RequiredPerm: discord.PermissionKickMembers,
			Options: []discord.ApplicationCommandOption{
				userOpt("Member to kick"),
				reasonOpt(false),
			},
			Execute: func(ctx *commands.Context) error {
				cfg, gid, err := m.configFor(ctx)
				if err != nil {
					return ctx.Respond(embed.Error("Moderation", err.Error()))
				}
				target, err := parseUserArg(ctx.Args, 0)
				if err != nil {
					return ctx.Respond(embed.Error("Kick failed", err.Error()))
				}
				c, err := m.kick(cfg, gid, ctx.Author.ID, ctx.Author.Tag(), target, argAt(ctx.Args, 1))
				if err != nil {
					return ctx.Respond(embed.Error("Kick failed", err.Error()))
				}
				return ctx.Respond(embed.Success("Member kicked", actionSummary(c)))
			},
		},
		{
			Name:         "timeout",
			Description:  "Timeout a member for a number of minutes.",
			Category:     "moderation",
			RequiredPerm: discord.PermissionModerateMembers,
			Options: []discord.ApplicationCommandOption{
				userOpt("Member to time out"),
				discord.ApplicationCommandOptionInt{
					Name: "minutes", Description: "Duration in minutes (max 40320 = 28 days)", Required: true,
					MinValue: intPtr(1), MaxValue: intPtr(maxTimeoutMinutes),
				},
				reasonOpt(false),
			},
			Execute: func(ctx *commands.Context) error {
				cfg, gid, err := m.configFor(ctx)
				if err != nil {
					return ctx.Respond(embed.Error("Moderation", err.Error()))
				}
				target, err := parseUserArg(ctx.Args, 0)
				if err != nil {
					return ctx.Respond(embed.Error("Timeout failed", err.Error()))
				}
				minutes, err := strconv.Atoi(argAt(ctx.Args, 1))
				if err != nil {
					return ctx.Respond(embed.Error("Timeout failed", "Duration must be a number of minutes."))
				}
				c, err := m.timeout(cfg, gid, ctx.Author.ID, ctx.Author.Tag(), target, minutes, argAt(ctx.Args, 2))
				if err != nil {
					return ctx.Respond(embed.Error("Timeout failed", err.Error()))
				}
				return ctx.Respond(embed.Success("Member timed out", actionSummary(c)))
			},
		},
		{
			Name:         "warn",
			Description:  "Warn a member and record it in the audit log.",
			Category:     "moderation",
			RequiredPerm: discord.PermissionModerateMembers,
			Options: []discord.ApplicationCommandOption{
				userOpt("Member to warn"),
				reasonOpt(true),
			},
			Execute: func(ctx *commands.Context) error {
				cfg, gid, err := m.configFor(ctx)
				if err != nil {
					return ctx.Respond(embed.Error("Moderation", err.Error()))
				}
				target, err := parseUserArg(ctx.Args, 0)
				if err != nil {
					return ctx.Respond(embed.Error("Warning failed", err.Error()))
				}
				out, err := m.warn(cfg, gid, ctx.Author.ID, ctx.Author.Tag(), target, argAt(ctx.Args, 1))
				if err != nil {
					return ctx.Respond(embed.Error("Warning failed", err.Error()))
				}
				desc := fmt.Sprintf("**%s** (`%s`) was warned.\nReason: %s\nTotal active warnings: **%d**\nCase #%d",
					out.Case.TargetTag, out.Case.TargetID, reasonOrNone(out.Case.Reason), out.Count, out.Case.ID)
				if out.Note != "" {
					desc += "\n" + out.Note
				}
				return ctx.Respond(embed.Warning("Warning issued", desc))
			},
		},
		{
			Name:         "warnings",
			Description:  "List a member's active warnings.",
			Category:     "moderation",
			RequiredPerm: discord.PermissionModerateMembers,
			Options: []discord.ApplicationCommandOption{
				userOpt("Member whose warnings to list"),
			},
			Execute: func(ctx *commands.Context) error {
				_, gid, err := m.configFor(ctx)
				if err != nil {
					return ctx.Respond(embed.Error("Moderation", err.Error()))
				}
				target, err := parseUserArg(ctx.Args, 0)
				if err != nil {
					return ctx.Respond(embed.Error("Warnings", err.Error()))
				}
				warns, err := m.warningList(gid.String(), target.String())
				if err != nil {
					return ctx.Respond(embed.Error("Warnings", err.Error()))
				}
				body := formatWarnings(warns)
				if len([]rune(body)) > 3800 {
					body = truncate(body, 3800)
				}
				return ctx.Respond(embed.Info(fmt.Sprintf("Warnings for %s", m.userTag(gid, target)), body))
			},
		},
		{
			Name:         "unwarn",
			Description:  "Remove one warning by case number.",
			Category:     "moderation",
			RequiredPerm: discord.PermissionModerateMembers,
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionInt{
					Name: "case_id", Description: "Warning case number", Required: true,
					MinValue: intPtr(1), MaxValue: intPtr(1_000_000_000),
				},
				reasonOpt(false),
			},
			Execute: func(ctx *commands.Context) error {
				_, gid, err := m.configFor(ctx)
				if err != nil {
					return ctx.Respond(embed.Error("Moderation", err.Error()))
				}
				caseID, err := parseCaseArg(ctx.Args, 0)
				if err != nil {
					return ctx.Respond(embed.Error("Unwarn failed", err.Error()))
				}
				c, err := m.removeWarning(gid.String(), caseID, ctx.Author.ID, ctx.Author.Tag(), argAt(ctx.Args, 1))
				if err != nil {
					return ctx.Respond(embed.Error("Unwarn failed", err.Error()))
				}
				return ctx.Respond(embed.Success("Warning removed", fmt.Sprintf(
					"Warning #%d against **%s** was removed.", c.ID, c.TargetTag)))
			},
		},
	}
}

// slashCommands is the module's command surface.
func (m *ModerationModule) SlashCommands() []commands.SlashCommand { return m.slashCommands() }

func actionSummary(c modules.ModerationCase) string {
	return fmt.Sprintf("**%s** (`%s`)\nReason: %s\nCase #%d",
		c.TargetTag, c.TargetID, reasonOrNone(c.Reason), c.ID)
}
