package reactionroles

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// PostPanel posts or updates a panel: it edits the existing message when the
// channel is unchanged, otherwise it removes the old message and posts a fresh
// one. Reactions are then added for every mapping; stale bot reactions are
// removed without touching members' own reactions.
func (m *ReactionRolesModule) PostPanel(guildID string, panelID int) error {
	if m.cfgStore == nil {
		return fmt.Errorf("reaction roles module not loaded")
	}
	cfg, err := m.cfgStore.Get(guildID)
	if err != nil {
		return err
	}
	idx := -1
	for i := range cfg.Panels {
		if cfg.Panels[i].ID == panelID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("panel %d not found", panelID)
	}
	panel := cfg.Panels[idx]
	if !panel.Enabled {
		return fmt.Errorf("enable the panel before posting it")
	}
	if panel.ChannelID == "" {
		return fmt.Errorf("choose a channel first")
	}
	if len(panel.Mappings) == 0 {
		return fmt.Errorf("add at least one reaction→role mapping")
	}
	if strings.TrimSpace(panel.Embed.Title) == "" && strings.TrimSpace(panel.Embed.Description) == "" {
		return fmt.Errorf("add an embed title or description")
	}
	for i, mapping := range panel.Mappings {
		roleID, err := snowflake.Parse(mapping.RoleID)
		if err != nil || roleID == 0 {
			return fmt.Errorf("mapping %d has an invalid role", i+1)
		}
		if err := m.ensureAssignableRole(mustParseID(guildID), roleID); err != nil {
			return fmt.Errorf("mapping %d: %w", i+1, err)
		}
	}
	channelID, err := snowflake.Parse(panel.ChannelID)
	if err != nil {
		return fmt.Errorf("invalid channel")
	}
	if m.ctx.Rest == nil {
		return fmt.Errorf("Discord REST client unavailable")
	}

	embed := buildPanelEmbed(panel.Embed)
	oldMessageID, _ := snowflake.Parse(panel.MessageID)
	oldChannelID, _ := snowflake.Parse(panel.PostedChannelID)
	var messageID snowflake.ID
	reused := false
	if oldMessageID != 0 && panel.PostedChannelID == panel.ChannelID {
		embeds := []discord.Embed{embed}
		if _, err := m.ctx.Rest.UpdateMessage(channelID, oldMessageID, discord.MessageUpdate{Embeds: &embeds}); err == nil {
			messageID = oldMessageID
			reused = true
		} else {
			m.ctx.Logger.Debug("Reaction roles: update message %s failed, posting a new one: %v", oldMessageID, err)
		}
	}
	if !reused && oldMessageID != 0 && oldChannelID != 0 && m.ctx.Rest != nil {
		if err := m.ctx.Rest.DeleteMessage(oldChannelID, oldMessageID); err != nil {
			m.ctx.Logger.Debug("Reaction roles: old message delete failed: %v", err)
		}
	}
	if !reused {
		msg, err := m.ctx.Rest.CreateMessage(channelID, discord.MessageCreate{
			Embeds:          []discord.Embed{embed},
			AllowedMentions: &discord.AllowedMentions{Parse: []discord.AllowedMentionType{}},
		})
		if err != nil {
			m.setLastError(guildID, err.Error())
			return fmt.Errorf("post panel: %w", err)
		}
		messageID = msg.ID
	}

	oldPosted := panel.PostedEmojis
	if !reused {
		oldPosted = nil // a new message carries none of the bot's reactions
	}
	newEmojis := make([]string, 0, len(panel.Mappings))
	for _, mapping := range panel.Mappings {
		newEmojis = append(newEmojis, mapping.Emoji)
	}
	newKeys := map[string]bool{}
	for _, emoji := range newEmojis {
		newKeys[emojiUniqueKey(emoji)] = true
	}
	oldKeys := map[string]bool{}
	for _, emoji := range oldPosted {
		oldKeys[emojiUniqueKey(emoji)] = true
	}
	posted := make([]string, 0, len(newEmojis))
	failed := make([]string, 0)
	for _, emoji := range newEmojis {
		if reused && oldKeys[emojiUniqueKey(emoji)] {
			posted = append(posted, emoji)
			continue
		}
		if err := m.ctx.Rest.AddReaction(channelID, messageID, emoji); err != nil {
			failed = append(failed, emoji)
			m.ctx.Logger.Error("Reaction roles: add reaction %q failed: %v", emoji, err)
			continue
		}
		posted = append(posted, emoji)
	}
	if reused {
		for _, emoji := range oldPosted {
			if !newKeys[emojiUniqueKey(emoji)] {
				if err := m.ctx.Rest.RemoveOwnReaction(channelID, messageID, emoji); err != nil {
					m.ctx.Logger.Debug("Reaction roles: remove stale reaction %q failed: %v", emoji, err)
				}
			}
		}
	}

	cfg.Panels[idx].MessageID = messageID.String()
	cfg.Panels[idx].PostedChannelID = panel.ChannelID
	cfg.Panels[idx].PostedEmojis = posted
	if err := m.cfgStore.Save(guildID, cfg); err != nil {
		return err
	}
	if len(failed) > 0 {
		msg := "could not add reactions: " + strings.Join(failed, ", ")
		m.setLastError(guildID, msg)
		return fmt.Errorf("%s", msg)
	}
	m.setLastError(guildID, "")
	return nil
}

func buildPanelEmbed(e modules.ReactionRoleEmbed) discord.Embed {
	embed := discord.NewEmbed()
	if e.Color != "" {
		if color := parseColor(e.Color); color != 0 {
			embed = embed.WithColor(color)
		}
	}
	if e.Title != "" {
		embed = embed.WithTitle(e.Title)
	}
	if e.Description != "" {
		embed = embed.WithDescription(e.Description)
	}
	if e.Footer != "" {
		embed = embed.WithFooter(e.Footer, "")
	}
	if e.ImageURL != "" {
		embed = embed.WithImage(e.ImageURL)
	}
	if e.ThumbnailURL != "" {
		embed = embed.WithThumbnail(e.ThumbnailURL)
	}
	return embed
}

func mustParseID(s string) snowflake.ID {
	id, _ := snowflake.Parse(s)
	return id
}
