package leveling

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/embed"
	"github.com/misfit/bot/internal/util"
	"github.com/misfit/bot/modules"
)

func (m *LevelingModule) slashCommands() []commands.SlashCommand {
	return []commands.SlashCommand{
		{
			Name:        "rank",
			Description: "Show a member's level and XP card.",
			Category:    "leveling",
			Usage:       "rank [user]",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionUser{Name: "user", Description: "Member to show (default: you)", Required: false},
			},
			Execute: m.executeRank,
		},
		{
			Name:        "leaderboard",
			Description: "Show this server's XP leaderboard.",
			Category:    "leveling",
			Usage:       "leaderboard [page]",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionInt{
					Name:        "page",
					Description: "Page number (10 members per page)",
					Required:    false,
					MinValue:    new(1),
					MaxValue:    new(1000),
				},
			},
			Execute: m.executeLeaderboard,
		},
	}
}

// executeRank renders the member's card. The module's disabled state does not
// hide the card: reading a rank is always allowed.
func (m *LevelingModule) executeRank(ctx *commands.Context) error {
	if ctx.GuildID == "" {
		return ctx.Respond(embed.Error("Rank", "This command only works in a server."))
	}
	if m.store == nil {
		return ctx.Respond(embed.Error("Rank", "Leveling is not loaded."))
	}
	targetID := ctx.Author.ID.String()
	if len(ctx.Args) > 0 && strings.TrimSpace(ctx.Args[0]) != "" {
		id := util.ExtractID(ctx.Args[0])
		if _, err := snowflake.Parse(id); err != nil || id == "0" {
			return ctx.Respond(embed.Error("Rank", "That is not a valid member."))
		}
		targetID = id
	}
	cfg, err := m.store.GetConfig(ctx.GuildID)
	if err != nil {
		return ctx.Respond(embed.Error("Rank", err.Error()))
	}

	entry, ok, err := m.GetEntry(ctx.GuildID, targetID)
	if err != nil {
		return ctx.Respond(embed.Error("Rank", err.Error()))
	}
	if !ok {
		level, progress, next := levelForXP(0, cfg.MaxLevel)
		entry = modules.LevelingEntry{UserID: targetID, Level: level, Progress: progress, NextLevelXP: next}
	}

	user, displayName := m.resolveUser(ctx.GuildID, targetID)
	data := rankCardData{
		Username:     "@" + user.Username,
		DisplayName:  displayName,
		AvatarURL:    avatarURL(user),
		Level:        entry.Level,
		Rank:         entry.Rank,
		XP:           entry.XP,
		Progress:     entry.Progress,
		NextLevelXP:  entry.NextLevelXP,
		Messages:     entry.Messages,
		VoiceSeconds: entry.VoiceSeconds,
		MaxLevel:     cfg.MaxLevel,
	}

	png, err := renderRankCard(data)
	if err != nil {
		m.logError("Leveling: render rank card: %v", err)
		return ctx.Respond(rankEmbed(entry))
	}
	// Web-executed commands cannot attach files; they fall back to the embed.
	if ctx.RespondWithFiles != nil {
		return ctx.RespondWithFiles([]*discord.File{{Name: "rank.png", Reader: bytes.NewReader(png)}})
	}
	return ctx.Respond(rankEmbed(entry))
}

// resolveUser resolves the member (cache, then REST member, then user) and the
// name to print on the card. A synthetic user keeps the command working when
// the member is gone.
func (m *LevelingModule) resolveUser(guildID, userID string) (discord.User, string) {
	uid, err := snowflake.Parse(userID)
	if err != nil {
		return discord.User{ID: 0}, ""
	}
	if m.ctx != nil && m.ctx.Bot != nil {
		if member := m.ctx.Bot.GetCachedMember(guildID, userID); member != nil {
			return member.User, member.EffectiveName()
		}
	}
	if m.ctx != nil && m.ctx.Rest != nil {
		if gid, err := snowflake.Parse(guildID); err == nil {
			if member, err := m.ctx.Rest.GetMember(gid, uid); err == nil && member != nil {
				return member.User, member.EffectiveName()
			}
		}
		if user, err := m.ctx.Rest.GetUser(uid); err == nil && user != nil {
			return *user, user.EffectiveName()
		}
	}
	return discord.User{ID: uid}, ""
}

// rankEmbed is the fallback when the PNG cannot be attached.
func rankEmbed(entry modules.LevelingEntry) discord.Embed {
	title := "Rank"
	if entry.Rank > 0 {
		title = fmt.Sprintf("Rank #%d", entry.Rank)
	}
	rank := "Unranked"
	if entry.Rank > 0 {
		rank = fmt.Sprintf("#%d", entry.Rank)
	}
	progress := fmt.Sprintf("%s / %s", formatCompact(entry.Progress), formatCompact(entry.NextLevelXP))
	if entry.NextLevelXP <= 0 {
		progress = "MAX"
	}
	return embed.New().
		WithTitle(title).
		WithColor(embed.ColorInfo).
		WithFields(
			discord.EmbedField{Name: "Level", Value: strconv.Itoa(entry.Level), Inline: new(true)},
			discord.EmbedField{Name: "XP", Value: fmt.Sprintf("%s (%s)", formatCompact(entry.XP), progress), Inline: new(true)},
			discord.EmbedField{Name: "Rank", Value: rank, Inline: new(true)},
			discord.EmbedField{Name: "Messages", Value: strconv.FormatInt(entry.Messages, 10), Inline: new(true)},
			discord.EmbedField{Name: "Voice", Value: formatVoice(entry.VoiceSeconds), Inline: new(true)},
		).
		WithTimestamp(time.Now())
}

// executeLeaderboard renders the top members as an embed, 10 per page.
func (m *LevelingModule) executeLeaderboard(ctx *commands.Context) error {
	if ctx.GuildID == "" {
		return ctx.Respond(embed.Error("Leaderboard", "This command only works in a server."))
	}
	if m.store == nil {
		return ctx.Respond(embed.Error("Leaderboard", "Leveling is not loaded."))
	}
	page := 1
	if len(ctx.Args) > 0 && strings.TrimSpace(ctx.Args[0]) != "" {
		if parsed, err := strconv.Atoi(strings.TrimSpace(ctx.Args[0])); err == nil && parsed > 0 {
			page = parsed
		}
	}
	if page > 1000 {
		page = 1000
	}

	entries, total, err := m.Leaderboard(ctx.GuildID, (page-1)*10, 10)
	if err != nil {
		return ctx.Respond(embed.Error("Leaderboard", err.Error()))
	}

	title := "Leaderboard"
	if m.ctx != nil && m.ctx.Bot != nil {
		if guild := m.ctx.Bot.GetCachedGuild(ctx.GuildID); guild != nil && guild.Name != "" {
			title = "Leaderboard · " + guild.Name
		}
	}

	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := "User " + entry.UserID
		if m.ctx != nil && m.ctx.Bot != nil {
			if member := m.ctx.Bot.GetCachedMember(ctx.GuildID, entry.UserID); member != nil {
				name = member.EffectiveName()
			}
		}
		lines = append(lines, fmt.Sprintf("#%d **%s**: Level %d · %s XP",
			entry.Rank, name, entry.Level, formatCompact(entry.XP)))
	}
	description := "No XP yet."
	if len(lines) > 0 {
		description = strings.Join(lines, "\n")
	}
	pages := (total + 9) / 10
	if pages < 1 {
		pages = 1
	}
	return ctx.Respond(embed.New().
		WithTitle(title).
		WithDescription(description).
		WithColor(embed.ColorInfo).
		WithFooterText(fmt.Sprintf("Page %d/%d · %d ranked members", page, pages, total)))
}
