package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/custombot/bot/config"
	"gopkg.in/yaml.v3"
)

// DashboardConfig is persisted as module_configs/dashboard/config.yml.
// It holds the OAuth client secret, so the file is written with 0600.
type DashboardConfig struct {
	Listen        string   `yaml:"listen"`
	PublicURL     string   `yaml:"public_url"`
	ClientID      string   `yaml:"client_id"`
	ClientSecret  string   `yaml:"client_secret"`
	SessionSecret string   `yaml:"session_secret"`
	AllowedGuilds []string `yaml:"allowed_guilds"`
}

func defaultConfig() *DashboardConfig {
	return &DashboardConfig{
		Listen:        "127.0.0.1:8080", // localhost-only by default for safety
		AllowedGuilds: []string{},
	}
}

func cfgPath(dir string) string { return filepath.Join(dir, "config.yml") }

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
	return c, nil
}

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
			return r == ',' || r == ' ' || r == '\n' || r == '\t'
		})
		c.AllowedGuilds = fields
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
