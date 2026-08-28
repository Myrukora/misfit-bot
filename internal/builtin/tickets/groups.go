package tickets

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// groups.go — v1 legacy shims kept ONLY for migrateV1 (parse old groups_yaml)
// and the v1 dashboard textarea setter, which now routes into v2 types.
// New code should use TypeConfig + the CLI/webconfig v2 paths.

// GroupConfig is the v1 ticket group shape.
type GroupConfig struct {
	Key           string     `yaml:"key" json:"key"`
	Label         string     `yaml:"label" json:"label"`
	Enabled       bool       `yaml:"enabled" json:"enabled"`
	ParentChannel string     `yaml:"parent_channel" json:"parent_channel"`
	PingRoles     []string   `yaml:"ping_roles" json:"ping_roles"`
	EmbedTemplate string     `yaml:"embed_template" json:"embed_template"`
	Color         colorValue `yaml:"color" json:"color"`
	AllowClaim    *bool      `yaml:"allow_claim" json:"allow_claim"` // nil = default true
	AllowClose    *bool      `yaml:"allow_close" json:"allow_close"`

	Seq int `yaml:"seq,omitempty" json:"-"`
}

func (g GroupConfig) AllowClaimOn() bool { return g.AllowClaim == nil || *g.AllowClaim }
func (g GroupConfig) AllowCloseOn() bool { return g.AllowClose == nil || *g.AllowClose }

// parseGroupsYAML validates + parses the legacy groups textarea. Structural
// errors reject the WHOLE list — callers keep the previous value.
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
			g.Color = colorValue(defaultTicketColor)
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

// ── Type registry helpers (on TicketsModule) ─────────────────────────────

// typeOf returns the current config for one type key.
func (m *TicketsModule) typeOf(key string) (TypeConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.cfg.Types[key]
	if !ok || t == nil {
		return TypeConfig{}, false
	}
	return *t, true
}

// typesSnapshot returns copies of all configured types sorted by key.
func (m *TicketsModule) typesSnapshot() []TypeConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]TypeConfig, 0, len(m.cfg.Types))
	for _, t := range m.cfg.Types {
		if t != nil {
			out = append(out, *t)
		}
	}
	for i := 1; i < len(out); i++ { // tiny n; insertion sort keeps deps minimal
		for j := i; j > 0 && out[j].Key < out[j-1].Key; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// panelsSnapshot returns copies of all registered panels for one guild scope
// (panels are global config today; guild filtering happens by stored channel).
func (m *TicketsModule) panelsSnapshot() []PanelConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]PanelConfig, 0, len(m.cfg.Panels))
	for _, p := range m.cfg.Panels {
		out = append(out, p)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Name < out[j-1].Name; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// setGroupsYAML — v1 dashboard setter shim: MERGES the legacy YAML list into
// v2 types. Existing v2-only fields (helper/access roles, welcome message,
// button emoji) are preserved for keys that already exist; only the legacy
// GroupConfig fields are overwritten. Blank payloads are rejected so a stray
// empty save can never wipe the type list.
func (m *TicketsModule) setGroupsYAML(guildID, yamlText string) error {
	groups, err := parseGroupsYAML(yamlText)
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		return fmt.Errorf("empty groups list — use `tickets type remove <key>` to delete individual types")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range groups {
		gc := g
		existing, ok := m.cfg.Types[gc.Key]
		var t *TypeConfig
		if ok && existing != nil {
			t = existing // merge: keep v2-only fields
		} else {
			t = &TypeConfig{Key: gc.Key}
		}
		t.Label = gc.Label
		t.Enabled = gc.Enabled
		t.Category = gc.ParentChannel
		t.PingRoles = gc.PingRoles
		t.EmbedBody = gc.EmbedTemplate
		t.Color = gc.Color
		t.AllowClaim = gc.AllowClaim
		t.AllowClose = gc.AllowClose
		if t.ButtonLabel == "" || t.ButtonLabel == t.Key {
			t.ButtonLabel = gc.Label
		}
		m.cfg.Types[gc.Key] = t
	}
	return m.cfg.save(m.ctx.DataDir)
}
