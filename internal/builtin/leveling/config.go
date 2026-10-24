package leveling

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

const (
	maxMessageXP        = 1000
	maxMessageCooldown  = 86400
	maxVoiceXPPerMinute = 1000
	maxMaxLevel         = 1000
	maxRewards          = 100
	maxAnnounceMessage  = 1000
	maxCatchupMessages  = 20000
	maxCatchupChannels  = 500
)

// defaultConfig is returned for a guild with no saved settings. Leveling is
// OFF until a server enables it; the other defaults describe what happens once
// it is on.
func defaultConfig() modules.LevelingConfig {
	return modules.LevelingConfig{
		Enabled:                false,
		MessageEnabled:         true,
		MessageXPMin:           15,
		MessageXPMax:           25,
		MessageCooldownSeconds: 60,
		VoiceEnabled:           true,
		VoiceXPPerMinute:       10,
		VoiceRequireOthers:     true,
		MaxLevel:               500,
		AnnounceEnabled:        true,
		AnnounceChannel:        "",
		AnnounceMessage:        "{user} reached **level {level}**!",
		CatchupEnabled:         true,
		CatchupMaxMessages:     2000,
		CatchupMaxChannels:     50,
	}
}

func normalizeConfig(cfg modules.LevelingConfig) (modules.LevelingConfig, error) {
	cfg.AnnounceChannel = strings.TrimSpace(cfg.AnnounceChannel)
	cfg.AnnounceMessage = strings.TrimSpace(cfg.AnnounceMessage)

	ignoredChannels, err := normalizeIDList(cfg.IgnoredChannels, "ignored channel")
	if err != nil {
		return cfg, err
	}
	ignoredRoles, err := normalizeIDList(cfg.IgnoredRoles, "ignored role")
	if err != nil {
		return cfg, err
	}
	cfg.IgnoredChannels = ignoredChannels
	cfg.IgnoredRoles = ignoredRoles

	if len(cfg.Rewards) > maxRewards {
		return cfg, fmt.Errorf("at most %d reward roles are allowed", maxRewards)
	}
	// A role is only ever granted once (at its lowest configured level), so a
	// duplicate role ID at a higher level would never fire.
	seenRole := map[string]bool{}
	rewards := make([]modules.LevelingReward, 0, len(cfg.Rewards))
	for _, r := range cfg.Rewards {
		r.RoleID = strings.TrimSpace(r.RoleID)
		if r.RoleID == "" && r.Level == 0 {
			continue // empty editor row
		}
		roleID, err := snowflakeIDString(r.RoleID)
		if err != nil {
			return cfg, fmt.Errorf("reward role: %w", err)
		}
		r.RoleID = roleID
		if roleID != "" && seenRole[roleID] {
			continue
		}
		seenRole[r.RoleID] = true
		rewards = append(rewards, r)
	}
	sort.SliceStable(rewards, func(i, j int) bool {
		if rewards[i].Level != rewards[j].Level {
			return rewards[i].Level < rewards[j].Level
		}
		return rewards[i].RoleID < rewards[j].RoleID
	})
	cfg.Rewards = rewards
	return cfg, nil
}

// normalizeIDList trims and de-duplicates snowflake IDs, rejecting invalid
// entries instead of silently dropping them.
func normalizeIDList(in []string, what string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		id, err := snowflakeIDString(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func validateConfig(cfg modules.LevelingConfig) error {
	if cfg.MessageXPMin < 1 || cfg.MessageXPMin > maxMessageXP {
		return fmt.Errorf("message XP minimum must be between 1 and %d", maxMessageXP)
	}
	if cfg.MessageXPMax < 1 || cfg.MessageXPMax > maxMessageXP {
		return fmt.Errorf("message XP maximum must be between 1 and %d", maxMessageXP)
	}
	if cfg.MessageXPMin > cfg.MessageXPMax {
		return fmt.Errorf("message XP minimum must not exceed the maximum")
	}
	if cfg.MessageCooldownSeconds < 0 || cfg.MessageCooldownSeconds > maxMessageCooldown {
		return fmt.Errorf("message cooldown must be between 0 and %d seconds", maxMessageCooldown)
	}
	if cfg.VoiceXPPerMinute < 0 || cfg.VoiceXPPerMinute > maxVoiceXPPerMinute {
		return fmt.Errorf("voice XP per minute must be between 0 and %d", maxVoiceXPPerMinute)
	}
	if cfg.MaxLevel < 1 || cfg.MaxLevel > maxMaxLevel {
		return fmt.Errorf("max level must be between 1 and %d", maxMaxLevel)
	}
	if cfg.AnnounceChannel != "" {
		if err := validateSnowflake(cfg.AnnounceChannel); err != nil {
			return fmt.Errorf("announcement channel: %w", err)
		}
	}
	if utf8.RuneCountInString(cfg.AnnounceMessage) > maxAnnounceMessage {
		return fmt.Errorf("announcement message must be at most %d characters", maxAnnounceMessage)
	}
	for i, r := range cfg.Rewards {
		if r.Level < 1 || r.Level > cfg.MaxLevel {
			return fmt.Errorf("reward %d: level must be between 1 and %d", i+1, cfg.MaxLevel)
		}
		if err := validateSnowflake(r.RoleID); err != nil {
			return fmt.Errorf("reward %d: %w", i+1, err)
		}
	}
	if cfg.CatchupMaxMessages < 1 || cfg.CatchupMaxMessages > maxCatchupMessages {
		return fmt.Errorf("catch-up message cap must be between 1 and %d", maxCatchupMessages)
	}
	if cfg.CatchupMaxChannels < 1 || cfg.CatchupMaxChannels > maxCatchupChannels {
		return fmt.Errorf("catch-up channel cap must be between 1 and %d", maxCatchupChannels)
	}
	return nil
}

func validateSnowflake(s string) error {
	id, err := snowflake.Parse(strings.TrimSpace(s))
	if err != nil || id == 0 {
		return fmt.Errorf("invalid id %q", s)
	}
	return nil
}

func snowflakeIDString(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	id, err := snowflake.Parse(s)
	if err != nil || id == 0 {
		return "", fmt.Errorf("invalid id %q", s)
	}
	return id.String(), nil
}

func validGuildID(s string) bool {
	if s == "" || s == "0" {
		return false
	}
	id, err := snowflake.Parse(s)
	return err == nil && id != 0 && id.String() == s
}

// canonicalSnowflake validates an ID and returns its canonical decimal form,
// so every stored key and lookup uses the same representation.
func canonicalSnowflake(s, what string) (string, error) {
	id, err := snowflake.Parse(strings.TrimSpace(s))
	if err != nil || id == 0 {
		return "", fmt.Errorf("invalid %s id %q", what, s)
	}
	return id.String(), nil
}

// containsID reports whether id is in list (both already normalized).
func containsID(list []string, id string) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}
