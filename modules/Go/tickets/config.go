package main

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the tickets module's persisted settings. Global keys live at the
// top level; guild-scoped ones (control panel channel, log channel) are keyed
// by guildID. The raw groups YAML is stored verbatim so the dashboard textarea
// round-trips exactly what the owner typed.
type Config struct {
	GroupsYAML       string               `yaml:"groups_yaml"`
	RetentionDays    int                  `yaml:"storage_retention_days"` // 0 = keep forever
	AllowDashClose   bool                 `yaml:"allow_dashboard_close"`
	Guilds           map[string]*GuildCfg `yaml:"guilds"`
	parsed           []GroupConfig        // cache derived from GroupsYAML
	defaultRetention bool                 // true until retention was set explicitly
}

// GuildCfg holds per-guild settings.
type GuildCfg struct {
	ControlChannel string `yaml:"control_channel"` // staff panel channel
	LogChannel     string `yaml:"log_channel"`     // optional closed-ticket summaries
}

func configPath(dataDir string) string {
	return filepath.Join(dataDir, "config.yml")
}

// loadConfig reads the module config, applying defaults for anything absent.
func loadConfig(dataDir string) (Config, error) {
	cfg := Config{
		RetentionDays:    defaultRetentionDays,
		AllowDashClose:   false,
		Guilds:           map[string]*GuildCfg{},
		defaultRetention: true,
	}
	raw, err := os.ReadFile(configPath(dataDir))
	if os.IsNotExist(err) {
		groups, perr := parseGroupsYAML("")
		if perr != nil {
			return cfg, perr
		}
		cfg.parsed = groups
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read tickets config: %w", err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse tickets config: %w", err)
	}
	if cfg.Guilds == nil {
		cfg.Guilds = map[string]*GuildCfg{}
	}
	if cfg.RetentionDays == 0 && cfg.defaultRetention {
		cfg.RetentionDays = defaultRetentionDays
	}
	groups, err := parseGroupsYAML(cfg.GroupsYAML)
	if err != nil {
		return cfg, fmt.Errorf("stored groups_yaml invalid: %w", err)
	}
	cfg.parsed = groups
	return cfg, nil
}

// save persists the config atomically (tmp + rename).
func (c *Config) save(dataDir string) error {
	out, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	path := configPath(dataDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// guildCfg returns the per-guild settings block, creating it on demand.
func (c *Config) guildCfg(guildID string) *GuildCfg {
	g, ok := c.Guilds[guildID]
	if !ok || g == nil {
		g = &GuildCfg{}
		c.Guilds[guildID] = g
	}
	return g
}
