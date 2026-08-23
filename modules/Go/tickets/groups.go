package main

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultRetentionDays = 30
	defaultTicketColor   = 0x5865F2 // blurple
)

// GroupConfig is one ticket group: its own settings, panel button and
// enable/disable switch.
type GroupConfig struct {
	Key           string     `yaml:"key" json:"key"`
	Label         string     `yaml:"label" json:"label"`
	Enabled       bool       `yaml:"enabled" json:"enabled"`
	ParentChannel string     `yaml:"parent_channel" json:"parent_channel"`
	PingRoles     []string   `yaml:"ping_roles" json:"ping_roles"`
	EmbedTemplate string     `yaml:"embed_template" json:"embed_template"`
	Color         colorValue `yaml:"color" json:"color"`             // accepts "0x5865F2", "#5865f2", 5865F2 or 5793138
	AllowClaim    *bool      `yaml:"allow_claim" json:"allow_claim"` // nil = default true
	AllowClose    *bool      `yaml:"allow_close" json:"allow_close"`

	Seq int `yaml:"seq,omitempty" json:"-"` // next ticket number per group (persisted)
}

// AllowClaimOn/AllowCloseOn resolve the pointer fields with defaults.
func (g GroupConfig) AllowClaimOn() bool { return g.AllowClaim == nil || *g.AllowClaim }
func (g GroupConfig) AllowCloseOn() bool { return g.AllowClose == nil || *g.AllowClose }

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

// parseGroupsYAML validates + parses the groups textarea value. Defaults are
// applied here so both the config loader and the dashboard setter share one
// code path. Structural errors (duplicates, enabled-without-parent, empty
// key) reject the WHOLE list — the caller keeps the previous value.
func parseGroupsYAML(in string) ([]GroupConfig, error) {
	var groups []GroupConfig
	s := strings.TrimSpace(in)
	if s == "" {
		return nil, nil
	}
	if err := yaml.Unmarshal([]byte(s), &groups); err != nil {
		return nil, fmt.Errorf("groups_yaml is not valid YAML: %w", err)
	}
	seen := map[string]bool{}
	for i := range groups {
		g := &groups[i]
		if strings.TrimSpace(g.Key) == "" {
			return nil, fmt.Errorf("group #%d: key is required", i+1)
		}
		g.Key = strings.ToLower(strings.TrimSpace(g.Key))
		if seen[g.Key] {
			return nil, fmt.Errorf("duplicate group key %q", g.Key)
		}
		seen[g.Key] = true
		if g.Label == "" {
			g.Label = g.Key
		}
		if g.Enabled && strings.TrimSpace(g.ParentChannel) == "" {
			return nil, fmt.Errorf("group %q: parent_channel is required while enabled", g.Key)
		}
		if g.EmbedTemplate == "" {
			g.EmbedTemplate = "{user} opened a **{group}** ticket."
		}
		if g.Color == 0 {
			g.Color = defaultTicketColor // invalid/absent → blurple, never black
		}
	}
	return groups, nil
}

// colorFromHex parses user-typed "#5865F2"/"0x5865F2"/"5865F2" forms.
func colorFromHex(s string) (int, error) {
	s = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "#"), "0x")
	v, err := strconv.ParseInt(s, 16, 32)
	if err != nil || v < 0 || v > 0xFFFFFF {
		return 0, fmt.Errorf("invalid color %q", s)
	}
	return int(v), nil
}

// ── Group registry helpers (on TicketsModule) ────────────────────────────

// group returns the current parsed config for one group key.
func (m *TicketsModule) group(key string) (GroupConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, g := range m.cfg.parsed {
		if g.Key == key {
			return g, true
		}
	}
	return GroupConfig{}, false
}

// groupsSnapshot returns a copy of the parsed group list.
func (m *TicketsModule) groupsSnapshot() []GroupConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]GroupConfig, len(m.cfg.parsed))
	copy(out, m.cfg.parsed)
	return out
}

// setGroupsYAML validates + stores a new groups list (dashboard setter path).
func (m *TicketsModule) setGroupsYAML(guildID, yamlText string) error {
	groups, err := parseGroupsYAML(yamlText)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.cfg.GroupsYAML
	m.cfg.GroupsYAML = yamlText
	m.cfg.parsed = groups
	if err := m.cfg.save(m.ctx.DataDir); err != nil {
		m.cfg.GroupsYAML = old
		m.cfg.parsed = nil
		parseAgain, _ := parseGroupsYAML(old)
		m.cfg.parsed = parseAgain
		return fmt.Errorf("save failed: %w", err)
	}
	return nil
}
