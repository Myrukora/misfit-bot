package imagefilter

import (
	"fmt"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/embed"
)

// punishment.go — REST actions + log embeds, ported from the Python module:
//   - delete the message UNLESS (punishment == none AND delete_on_none == false)
//   - mute = timeout (communication_disabled_until = now + mute_duration)
//   - kick = RemoveMember; ban = AddBan with 1 day of message deletion
//   - immunity BEFORE acting: guild owner, or author's top role ≥ bot's top role
//   - REST failures go to the log channel as a red embed, never to the
//     detection channel; terminal log always
//   - detection log embed carries score + channel + the image (proxy_url)

// executePunishment runs the configured actions for one detected message.
// Called on the manager worker (slow REST calls are fine there).
func (m *ImageFilterModule) executePunishment(j job, score float64, settings GuildConfig) {
	// Delete rule first (independent of punishment).
	shouldDelete := true
	if settings.Punishment == PunishNone && !settings.DeleteOnNone {
		shouldDelete = false
	}
	if shouldDelete {
		if err := m.ctx.Rest.DeleteMessage(parseSnowflake(j.channelID), parseSnowflake(j.messageID)); err != nil {
			m.ctx.Logger.Warn("imagefilter: no permission to delete message in guild %s: %v", j.guildID, err)
		}
	}

	action := "None"
	var failure error

	if settings.Punishment != PunishNone {
		if m.isImmune(j.guildID, j.authorID) {
			action = fmt.Sprintf("Failed (%s): User Hierarchy/Owner", settings.Punishment)
		} else {
			switch settings.Punishment {
			case PunishKick:
				err := m.ctx.Rest.RemoveMember(parseSnowflake(j.guildID), parseSnowflake(j.authorID))
				if err != nil {
					failure = err
					action = fmt.Sprintf("Failed (%s): %v", settings.Punishment, err)
				} else {
					action = "Kicked"
				}
			case PunishBan:
				err := m.ctx.Rest.AddBan(parseSnowflake(j.guildID), parseSnowflake(j.authorID), 24*time.Hour)
				if err != nil {
					failure = err
					action = fmt.Sprintf("Failed (%s): %v", settings.Punishment, err)
				} else {
					action = "Banned"
				}
			case PunishMute:
				until := time.Now().UTC().Add(time.Duration(settings.MuteDuration) * time.Second)
				mu := discord.MemberUpdate{}
				mu.CommunicationDisabledUntil = omit.NewPtr(until)
				_, err := m.ctx.Rest.UpdateMember(parseSnowflake(j.guildID), parseSnowflake(j.authorID), mu)
				if err != nil {
					failure = err
					action = fmt.Sprintf("Failed (%s): %v", settings.Punishment, err)
				} else {
					action = fmt.Sprintf("Muted (%ds)", settings.MuteDuration)
				}
			}
		}
	} else if shouldDelete {
		action = "Message Deleted (No Punishment)"
	}

	if failure != nil {
		m.logFailure(j, settings, score, failure)
		return
	}
	if settings.LogChannel != "" {
		m.logDetection(j, settings, score, action)
	}
}

// isImmune: guild owner always; otherwise the author is immune when their top
// role position ≥ the bot's top role position (hierarchy). Cache-friendly:
// reads through the bot adapter's cache, falls back to REST once.
func (m *ImageFilterModule) isImmune(guildID, authorID string) bool {
	if guildID == "" || authorID == "" {
		return false
	}
	gid := parseSnowflake(guildID)
	uid := parseSnowflake(authorID)

	// Guild owner (cached via adapter).
	if owner := m.ctx.Bot.GetGuildOwnerID(guildID); owner != "" && owner == authorID {
		return true
	}

	// Role hierarchy: author's top role vs bot's top role.
	botID := m.ctx.Bot.GetSelfUserID()
	if botID == "" || botID == authorID {
		return false
	}
	botTop := m.topRolePosition(gid, parseSnowflake(botID))
	userTop := m.topRolePosition(gid, uid)
	return userTop >= botTop && botTop >= 0
}

// topRolePosition resolves a member's highest role position via cache, with a
// REST fallback; -1 when unknown (unknown → not immune).
func (m *ImageFilterModule) topRolePosition(gid, uid snowflake.ID) int {
	if member := m.ctx.Bot.GetCachedMember(gid.String(), uid.String()); member != nil && len(member.RoleIDs) > 0 {
		top := -1
		for _, rid := range member.RoleIDs {
			if role := m.ctx.Bot.GetCachedRole(gid.String(), rid.String()); role != nil {
				if int(role.Position) > top {
					top = int(role.Position)
				}
			}
		}
		if top >= 0 {
			return top
		}
	}
	// REST fallback (rare: uncached author).
	roles, err := m.ctx.Rest.GetRoles(gid)
	if err != nil {
		return -1
	}
	pos := map[snowflake.ID]int{}
	for _, r := range roles {
		pos[r.ID] = int(r.Position)
	}
	member, err := m.ctx.Rest.GetMember(gid, uid)
	if err != nil {
		return -1
	}
	top := -1
	for _, rid := range member.RoleIDs {
		if p, ok := pos[rid]; ok && p > top {
			top = p
		}
	}
	return top
}

// logDetection posts the hit embed to the guild's configured log channel.
func (m *ImageFilterModule) logDetection(j job, settings GuildConfig, score float64, action string) {
	e := embed.New().
		WithTitle("🛑 Image spam detected").
		WithDescription(fmt.Sprintf(
			"**User:** <@%s>\n**Action:** %s\n**Similarity Score:** `%.4f`\n**Channel:** <#%s>",
			j.authorID, action, score, j.channelID)).
		WithColor(embed.ColorError).
		WithTimestamp(time.Now())
	if j.imageURL != "" {
		e = e.WithImage(j.imageURL)
	}
	e = e.WithFooterText("OpenAI CLIP Analysis (ONNX CPU)")
	_, err := m.ctx.Rest.CreateMessage(parseSnowflake(settings.LogChannel), discord.MessageCreate{
		Embeds: []discord.Embed{e},
	})
	if err != nil {
		m.ctx.Logger.Error("imagefilter: failed to send detection log to %s: %v", settings.LogChannel, err)
	}
}

// logFailure posts a punishment failure (permissions/hierarchy/REST) to the
// log channel; never to the detection channel.
func (m *ImageFilterModule) logFailure(j job, settings GuildConfig, score float64, failure error) {
	if settings.LogChannel == "" {
		return
	}
	e := embed.New().
		WithTitle("❌ Image spam punishment failed").
		WithDescription(fmt.Sprintf(
			"**User:** <@%s>\n**Punishment:** %s (FAILED)\n**Similarity Score:** `%.4f`\n**Channel:** <#%s>\n**Error:** `%v`",
			j.authorID, settings.Punishment, score, j.channelID, failure)).
		WithColor(embed.ColorError).
		WithTimestamp(time.Now())
	if j.imageURL != "" {
		e = e.WithImage(j.imageURL)
	}
	e = e.WithFooterText("OpenAI CLIP Analysis (ONNX CPU)")
	_, err := m.ctx.Rest.CreateMessage(parseSnowflake(settings.LogChannel), discord.MessageCreate{
		Embeds: []discord.Embed{e},
	})
	if err != nil {
		m.ctx.Logger.Error("imagefilter: failed to send failure log to %s: %v", settings.LogChannel, err)
	}
}

// parseSnowflake parses a string ID; 0 on garbage (REST then errors and we log).
func parseSnowflake(s string) snowflake.ID {
	id, _ := snowflake.Parse(s)
	return id
}
