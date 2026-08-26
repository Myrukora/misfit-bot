package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/misfit/bot/config"
	"gopkg.in/yaml.v3"
)

// DashboardConfig is persisted as config.yml next to the module
// (modules/Go/dashboard/config.yml, 0600).
// It holds the OAuth client secret, so the file is written with 0600.
type DashboardConfig struct {
	Listen        string   `yaml:"listen"`
	PublicURL     string   `yaml:"public_url"`
	ClientID      string   `yaml:"client_id"`
	ClientSecret  string   `yaml:"client_secret"`
	SessionSecret string   `yaml:"session_secret"`
	AllowedGuilds []string `yaml:"allowed_guilds"`
	// ExecMode picks which command implementation the dashboard's Run button
	// executes and which kind the commands tab displays: "prefix" (default) or
	// "slash". Prefix text commands REQUIRE Discord's Message Content intent
	// to be usable in Discord; slash works without it and mirrors what users
	// type natively.
	ExecMode string `yaml:"exec_mode"`
	// ExecAllowlist is an allowlist of command names the dashboard's Run button
	// may execute (empty = allow all). This is the security boundary: when set,
	// /api/exec refuses any command not in the list, so an owner can lock the
	// dashboard down to a safe subset even if it's reachable on the network.
	ExecAllowlist []string `yaml:"exec_allowlist"`
}

// defaultConfig returns the default dashboard module configuration.
func defaultConfig() *DashboardConfig {
	return &DashboardConfig{
		Listen:        "127.0.0.1:8080", // localhost-only by default for safety
		AllowedGuilds: []string{},
		ExecMode:      "prefix",
	}
}

// cfgPath resolves the module config file path.
func cfgPath(dir string) string { return filepath.Join(dir, "config.yml") }

// migrateLegacyConfig performs the one-time move of the dashboard config from
// the pre-restructure location (<bot config dir>/module_configs/dashboard/
// config.yml) into the module's own folder. It only runs when the new file
// does not exist yet (fresh installs skip it), and it never deletes the
// legacy file — the owner can clean it up after confirming the migration.
// Returns whether a migration happened.
func migrateLegacyConfig(dataDir, botConfigDir string) (bool, error) {
	dest := cfgPath(dataDir)
	if _, err := os.Stat(dest); err == nil {
		return false, nil // already migrated / fresh install
	} else if !os.IsNotExist(err) {
		return false, err // unexpected stat error — surface it, don't guess
	}
	src := filepath.Join(botConfigDir, "module_configs", "dashboard", "config.yml")
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // no legacy config
		}
		return false, err
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return false, err
	}
	// Create the destination EXCLUSIVELY: a config created by another
	// process between the Stat above and here must never be overwritten.
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return false, nil // another process migrated first — treat as skipped
	}
	if err != nil {
		return false, err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(dest) // never leave a partial file blocking a retry
		return false, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(dest)
		return false, err
	}
	return true, nil
}

// loadConfig reads the module config, creating defaults when the file is missing.
func loadConfig(dir string) (*DashboardConfig, error) {
	c := defaultConfig()
	data, err := os.ReadFile(cfgPath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse dashboard config: %w", err)
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	} else {
		c.Listen = config.NormalizeListen(c.Listen)
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	if c.AllowedGuilds == nil {
		c.AllowedGuilds = []string{}
	}
	if c.ExecMode != "slash" {
		c.ExecMode = "prefix"
	}
	c.ExecAllowlist = normalizeAllowlist(c.ExecAllowlist)
	return c, nil
}

// normalizeAllowlist trims whitespace and drops empty entries from the exec
// allowlist, returning a non-nil slice. Order is preserved; duplicates collapse
// to first occurrence so the set is stable across reads.
func normalizeAllowlist(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range in {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// Save persists the dashboard config to disk (0600).
func (c *DashboardConfig) Save(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath(dir), data, 0600)
}

// Set applies one dashboard config key with validation.
func (c *DashboardConfig) Set(key, value string) error {
	if c.AllowedGuilds == nil {
		c.AllowedGuilds = []string{}
	}
	switch key {
	case "listen":
		c.Listen = config.NormalizeListen(value)
	case "public_url":
		value = strings.TrimRight(value, "/")
		if value != "" && !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
			return fmt.Errorf("public_url must start with http:// or https://")
		}
		c.PublicURL = value
	case "client_id":
		c.ClientID = value
	case "client_secret":
		c.ClientSecret = value
	case "session_secret":
		c.SessionSecret = value
	case "allowed_guilds":
		fields := strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\n' || r == '	'
		})
		c.AllowedGuilds = fields
	case "exec_mode":
		if value != "prefix" && value != "slash" {
			return fmt.Errorf("exec_mode must be \"prefix\" or \"slash\"")
		}
		c.ExecMode = value
	default:
		return fmt.Errorf("unknown dashboard config key: %q", key)
	}
	return nil
}

// ensureSessionSecret generates and persists a random 32-byte session secret
// if none is set, so the signed session cookie is safe even on first run.
func (c *DashboardConfig) ensureSessionSecret(dir string) error {
	if c.SessionSecret != "" {
		return nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	c.SessionSecret = hex.EncodeToString(b)
	return c.Save(dir)
}
