package leveling

import (
	"sort"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// awardVoiceXP runs once per 60s tick: every eligible member in a tracked
// voice channel earns the configured per-minute XP.
func (m *LevelingModule) awardVoiceXP() {
	if m.ctx == nil || m.ctx.VoiceStates == nil || m.store == nil {
		return
	}
	ids, err := m.store.GuildIDs()
	if err != nil {
		m.logError("Leveling: list guilds: %v", err)
		return
	}
	for _, guildID := range ids {
		cfg, err := m.store.GetConfig(guildID)
		if err != nil || !cfg.Enabled {
			continue
		}
		if !cfg.VoiceEnabled || cfg.VoiceXPPerMinute <= 0 {
			continue
		}
		states := m.ctx.VoiceStates(guildID)
		if len(states) == 0 {
			continue
		}
		m.awardGuildVoice(guildID, cfg, states)
	}
}

func (m *LevelingModule) awardGuildVoice(guildID string, cfg modules.LevelingConfig, states []discord.VoiceState) {
	var afkChannel snowflake.ID
	if m.ctx.Bot != nil {
		if guild := m.ctx.Bot.GetCachedGuild(guildID); guild != nil && guild.AfkChannelID != nil {
			afkChannel = *guild.AfkChannelID
		}
	}
	byChannel := map[string][]string{}
	for _, vs := range states {
		if vs.ChannelID == nil || *vs.ChannelID == 0 {
			continue
		}
		if vs.SelfDeaf || vs.GuildDeaf {
			continue
		}
		if afkChannel != 0 && *vs.ChannelID == afkChannel {
			continue
		}
		channelID := vs.ChannelID.String()
		if containsID(cfg.IgnoredChannels, channelID) {
			continue
		}
		userID := vs.UserID.String()
		// Uncached members (gated mode) count as eligible humans: without the
		// members intent we cannot see their bot flag or roles.
		if m.ctx.Bot != nil {
			if member := m.ctx.Bot.GetCachedMember(guildID, userID); member != nil {
				if member.User.Bot {
					continue
				}
				if m.hasIgnoredRole(cfg, member.RoleIDs) {
					continue
				}
			}
		}
		byChannel[channelID] = append(byChannel[channelID], userID)
	}

	channels := make([]string, 0, len(byChannel))
	for channelID := range byChannel {
		channels = append(channels, channelID)
	}
	sort.Strings(channels)
	amount := int64(cfg.VoiceXPPerMinute)
	for _, channelID := range channels {
		users := byChannel[channelID]
		if cfg.VoiceRequireOthers && len(users) < 2 {
			continue
		}
		sort.Strings(users)
		for _, userID := range users {
			prevXP, cur, err := m.store.AwardVoice(guildID, userID, amount)
			if err != nil {
				m.logError("Leveling: award voice for guild %s: %v", guildID, err)
				continue
			}
			if cur.XP > prevXP {
				go m.handleLevelUps(guildID, userID, prevXP, cur.XP, cfg, true)
			}
		}
	}
}

// hasIgnoredRole reports whether any of roleIDs is configured as ignored.
func (m *LevelingModule) hasIgnoredRole(cfg modules.LevelingConfig, roleIDs []snowflake.ID) bool {
	for _, roleID := range roleIDs {
		if containsID(cfg.IgnoredRoles, roleID.String()) {
			return true
		}
	}
	return false
}
