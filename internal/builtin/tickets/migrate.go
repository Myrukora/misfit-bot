package tickets

import (
	"fmt"
	"os"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"gopkg.in/yaml.v3"
)

// resolveChannelGuild resolves a channel ID to its owning guild via REST.
func (m *TicketsModule) resolveChannelGuild(channelID string) (string, bool) {
	if channelID == "" {
		return "", false
	}
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return "", false
	}
	ch, err := m.ctx.Rest.GetChannel(cid)
	if err != nil || ch == nil {
		return "", false
	}
	gc, ok := ch.(discord.GuildChannel)
	if !ok {
		return "", false
	}
	return gc.GuildID().String(), true
}

// channelGuild resolves a channel ID to its owning guild, using the test
// seam when set.
func (m *TicketsModule) channelGuildOf(channelID string) (string, bool) {
	if m.channelGuild != nil {
		return m.channelGuild(channelID)
	}
	return m.resolveChannelGuild(channelID)
}

// migrateToV3 converts a legacy single-file config (v1 groups_yaml or v2)
// into per-guild files + a bot-wide module config. Idempotent: a missing
// config file or a v3 file is a no-op. The legacy file is renamed to
// config.yml.v2-migrated.
//
// Target guilds are the union of ticket dirs under tickets/ (valid snowflake
// names) and the resolved owners of panel channels and the log channel.
// Types are copied into every target guild; a panel goes to its channel's
// owning guild when resolvable, else every target guild (with a WARN); the
// log channel follows the same rule.
func (m *TicketsModule) migrateToV3(dataDir string) error {
	raw, err := os.ReadFile(moduleConfigPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var probe struct {
		Version int `yaml:"version"`
	}
	if err := yaml.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("probe legacy config: %w", err)
	}
	if probe.Version >= configVersion {
		return nil // already v3
	}
	var legacy legacyConfig
	if err := yaml.Unmarshal(raw, &legacy); err != nil {
		return fmt.Errorf("parse legacy config: %w", err)
	}
	if isV1Config(raw) {
		if err := migrateV1(&legacy); err != nil {
			return err
		}
	}

	// Target guilds: ticket dirs + resolved panel/log-channel owners.
	targets := map[string]bool{}
	if entries, err := os.ReadDir(ticketsRoot(dataDir)); err == nil {
		for _, e := range entries {
			if e.IsDir() && validGuildID(e.Name()) {
				targets[e.Name()] = true
			}
		}
	}
	panelOwner := map[string]string{}
	for name, p := range legacy.Panels {
		if p.ChannelID == "" {
			continue
		}
		if gid, ok := m.channelGuildOf(p.ChannelID); ok {
			panelOwner[name] = gid
			targets[gid] = true
		}
	}
	logOwner := ""
	if legacy.LogChannel != "" {
		if gid, ok := m.channelGuildOf(legacy.LogChannel); ok {
			logOwner = gid
			targets[gid] = true
		}
	}

	for gid := range targets {
		cfg := &Config{Version: configVersion, Types: map[string]*TypeConfig{}, Panels: map[string]PanelConfig{}}
		for k, t := range legacy.Types {
			if t == nil {
				continue
			}
			tc := *t
			cfg.Types[k] = &tc
		}
		for name, p := range legacy.Panels {
			owner := panelOwner[name]
			if owner == "" || owner == gid {
				cfg.Panels[name] = p
			}
		}
		if legacy.LogChannel != "" && (logOwner == "" || logOwner == gid) {
			cfg.LogChannel = legacy.LogChannel
		}
		if err := cfg.save(dataDir, gid); err != nil {
			return err
		}
	}
	for name, p := range legacy.Panels {
		if p.ChannelID != "" {
			if _, ok := panelOwner[name]; !ok {
				m.ctx.Logger.Warn("Tickets: panel %s channel unresolved; copied to all migrated guilds", name)
			}
		}
	}
	if legacy.LogChannel != "" && logOwner == "" {
		m.ctx.Logger.Warn("Tickets: log channel unresolved; copied to all migrated guilds")
	}

	// Rename the legacy file and write the fresh module config.
	legacyPath := moduleConfigPath(dataDir)
	if err := os.Rename(legacyPath, legacyPath+".v2-migrated"); err != nil {
		return err
	}
	mod := &ModuleConfig{
		Version:        configVersion,
		Retention:      legacy.Retention,
		AllowDashClose: legacy.AllowDashClose,
		ModalsEnabled:  nil,
	}
	if err := mod.save(dataDir); err != nil {
		return err
	}
	m.ctx.Logger.Info("Tickets: migrated config to per-guild files (%d guilds, %d panels)",
		len(targets), len(legacy.Panels))
	return nil
}
