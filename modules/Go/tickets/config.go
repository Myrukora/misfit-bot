package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/misfit/bot/modules"
)

const configVersion = 2
const defaultRetentionDays = 30
const defaultTicketColor = 0x5865F2

// TypeConfig is what a ticket IS: where channels spawn, who is pinged, who can
// help, and how the open-embed + button look.
type TypeConfig struct {
	Key         string     `yaml:"key" json:"key"`
	Label       string     `yaml:"label" json:"label"`
	Enabled     bool       `yaml:"enabled" json:"enabled"`
	Category    string     `yaml:"category" json:"category"` // ticket channels spawn under this category
	PingRoles   []string   `yaml:"ping_roles" json:"ping_roles"`
	HelperRoles []string   `yaml:"helper_roles" json:"helper_roles"` // see/claim/close (+ mods always pass)
	AccessRoles []string   `yaml:"access_roles" json:"access_roles"` // may OPEN; empty = everyone
	WelcomeMsg  string     `yaml:"welcome_msg" json:"welcome_msg"`
	EmbedBody   string     `yaml:"embed_body" json:"embed_body"`
	ButtonLabel string     `yaml:"button_label" json:"button_label"`
	ButtonEmoji string     `yaml:"button_emoji" json:"button_emoji"`
	Color       colorValue `yaml:"color" json:"color"`
	AllowClaim  *bool      `yaml:"allow_claim" json:"allow_claim"`
	AllowClose  *bool      `yaml:"allow_close" json:"allow_close"`

	Seq int `yaml:"-" json:"-"` // next ticket number (reserved via store)
}

func (g TypeConfig) AllowClaimOn() bool { return g.AllowClaim == nil || *g.AllowClaim }
func (g TypeConfig) AllowCloseOn() bool { return g.AllowClose == nil || *g.AllowClose }

// PanelConfig is one POSTED embed advertising a type. The bot remembers it so
// panels can be edited/suspended/resumed by name — never by message ID.
type PanelConfig struct {
	Name        string `yaml:"name" json:"name"`
	ChannelID   string `yaml:"channel_id" json:"channel_id"`
	MessageID   string `yaml:"message_id" json:"message_id"`
	TypeKey     string `yaml:"type" json:"type"`
	Title       string `yaml:"title,omitempty" json:"title,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Suspended   bool   `yaml:"suspended,omitempty" json:"suspended"`
}

// Config is the tickets module's persisted settings, version 2.
type Config struct {
	Version        int                    `yaml:"version"`
	Types          map[string]*TypeConfig `yaml:"types"`
	Panels         map[string]PanelConfig `yaml:"panels"`
	LogChannel     string                 `yaml:"log_channel"`
	Retention      retentionDays          `yaml:"storage_retention_days"`
	AllowDashClose bool                   `yaml:"allow_dashboard_close"`

	parsed bool // sanity: loadConfig always leaves Types non-nil
}

// RetentionDays resolves retention with the omitted-field default applied.
func (c *Config) RetentionDays() int {
	if c.Retention.set {
		return c.Retention.value
	}
	return defaultRetentionDays
}

// retentionDays is an int that records whether the YAML key was present.
// A YAML null (e.g. from MarshalYAML of an unset value) counts as unset so a
// save→reload round-trip can never silently disable the default retention.
type retentionDays struct {
	value int
	set   bool
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (r *retentionDays) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag == "!!null" {
		r.set = false
		r.value = 0
		return nil
	}
	r.set = true
	if node.Kind == yaml.ScalarNode {
		if n, err := strconv.Atoi(strings.TrimSpace(node.Value)); err == nil {
			r.value = n
		}
	}
	return nil
}

// MarshalYAML omits the key entirely when it was never set.
func (r retentionDays) MarshalYAML() (any, error) {
	if !r.set {
		return nil, nil
	}
	return r.value, nil
}

// colorValue accepts hex strings ("0x5865F2", "#5865f2", "5865F2") and plain
// ints in YAML, so owners can type colors naturally. Invalid values unmarshal
// to 0 and fall back to blurple at parse time (never an error, never black).
type colorValue int

// UnmarshalYAML implements yaml.Unmarshaler.
func (c *colorValue) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if n, err := strconv.ParseInt(node.Value, 0, 32); err == nil && n >= 0 && n <= 0xFFFFFF {
			*c = colorValue(n)
			return nil
		}
		s := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(node.Value, "#"), "0x"), "0X")
		if n, err := strconv.ParseInt(s, 16, 32); err == nil && n >= 0 && n <= 0xFFFFFF {
			*c = colorValue(n)
			return nil
		}
		*c = 0 // invalid → fallback at parse time
		return nil
	default:
		*c = 0
		return nil
	}
}

func configPath(dataDir string) string { return filepath.Join(dataDir, "config.yml") }

func cfgGuildsRoot(dataDir string) string { return filepath.Join(dataDir, "tickets") }

// loadConfig reads the module config, migrating v1 groups_yaml on the fly.
func loadConfig(dataDir string) (*Config, error) {
	cfg := &Config{
		Version: configVersion,
		Types:   map[string]*TypeConfig{},
		Panels:  map[string]PanelConfig{},
	}
	raw, err := os.ReadFile(configPath(dataDir))
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tickets config: %w", err)
	}
	// Detect v1: legacy top-level groups_yaml key.
	if isV1Config(raw) {
		if err := migrateV1(raw, cfg); err != nil {
			return nil, err
		}
	} else if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse tickets config: %w", err)
	}
	if err := validateTypes(cfg.Types); err != nil {
		return nil, err
	}
	if cfg.Types == nil {
		cfg.Types = map[string]*TypeConfig{}
	}
	if cfg.Panels == nil {
		cfg.Panels = map[string]PanelConfig{}
	}
	for k, p := range cfg.Panels { // name/key consistency with map key
		p.Name = k
		cfg.Panels[k] = p
	}
	cfg.parsed = true
	return cfg, nil
}

func isV1Config(raw []byte) bool {
	var probe struct {
		GroupsYAML string `yaml:"groups_yaml"`
		Version    int    `yaml:"version"`
	}
	_ = yaml.Unmarshal(raw, &probe)
	return probe.Version == 0 && probe.GroupsYAML != ""
}

// migrateV1 converts a v1 groups_yaml into v2 types. parent_channel becomes
// category; everything else carries over; unknown fields are dropped.
func migrateV1(raw []byte, cfg *Config) error {
	var old struct {
		GroupsYAML     string               `yaml:"groups_yaml"`
		LogChannel     string               `yaml:"log_channel"`
		Retention      retentionDays        `yaml:"storage_retention_days"`
		AllowDashClose bool                 `yaml:"allow_dashboard_close"`
		Guilds         map[string]*struct { // ignored: per-guild control channel gone
			ControlChannel string `yaml:"control_channel"`
		} `yaml:"guilds"`
	}
	if err := yaml.Unmarshal(raw, &old); err != nil {
		return fmt.Errorf("parse v1 tickets config: %w", err)
	}
	cfg.LogChannel = old.LogChannel
	cfg.Retention = old.Retention
	cfg.AllowDashClose = old.AllowDashClose

	groups, err := parseGroupsYAML(old.GroupsYAML)
	if err != nil {
		return fmt.Errorf("migrating groups_yaml: %w", err)
	}
	for _, g := range groups {
		gc := g // copy
		cfg.Types[g.Key] = &TypeConfig{
			Key: gc.Key, Label: gc.Label, Enabled: gc.Enabled,
			Category: gc.ParentChannel, PingRoles: gc.PingRoles,
			EmbedBody: gc.EmbedTemplate, Color: gc.Color,
			AllowClaim: gc.AllowClaim, AllowClose: gc.AllowClose,
			ButtonLabel: gc.Label,
		}
	}
	return nil
}

func validateTypes(types map[string]*TypeConfig) error {
	for k, t := range types {
		if t == nil {
			delete(types, k) // YAML "key:" with no body → drop, never reach consumers
			continue
		}
		if strings.TrimSpace(t.Key) == "" {
			t.Key = k
		}
		if t.Enabled && strings.TrimSpace(t.Category) == "" {
			return fmt.Errorf("type %q: enabled but no category set", k)
		}
	}
	return nil
}

// save persists the config atomically (0600 — types carry server internals).
func (c *Config) save(dataDir string) error {
	c.Version = configVersion
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

// ── validation helpers shared by CLI/dashboard setters ────────────────────

// validPanelName restricts panel names to safe identifier chars — they become
// custom_id payloads AND map keys.
func validPanelName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// validTypeKey applies the same charset rules to type keys.
func validTypeKey(key string) bool { return validPanelName(key) }

// validEmoji accepts unicode emoji or <:name:id> / :name:id: custom forms.
func validEmoji(e string) bool {
	e = strings.TrimSpace(e)
	if e == "" {
		return true // empty = none
	}
	if strings.HasPrefix(e, "<:") && strings.HasSuffix(e, ">") {
		parts := strings.SplitN(strings.Trim(e, "<>"), ":", 3)
		return len(parts) == 3 && parts[1] != "" && parts[2] != ""
	}
	if strings.HasPrefix(e, ":") && strings.HasSuffix(e, ":") {
		return strings.Count(strings.Trim(e, ":"), ":") == 0 && len(e) > 2
	}
	// Unicode: cheap sanity — no whitespace/control chars, not ASCII-only word.
	if strings.ContainsAny(e, " \t\n") {
		return false
	}
	for _, r := range e {
		if r < 0x2000 { // real emoji live above the ASCII/Latin blocks
			return false
		}
	}
	return true
}

// ensure TypeSummary/GroupSummary stay referenced (contract surface).
var _ = modules.TypeSummary{}
