package tickets

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/misfit/bot/modules"
)

const (
	configVersion        = 3
	defaultRetentionDays = 30
	defaultTicketColor   = 0x5865F2

	maxModalQuestions      = 5
	maxQuestionLabel       = 45
	maxQuestionPlaceholder = 100
	maxQuestionValue       = 4000
)

// TypeConfig is one ticket type (v2 replacement of GroupConfig).
type TypeConfig struct {
	Key         string     `yaml:"key"`
	Label       string     `yaml:"label"`
	Enabled     bool       `yaml:"enabled"`
	Category    string     `yaml:"category"`
	PingRoles   []string   `yaml:"ping_roles"`
	HelperRoles []string   `yaml:"helper_roles"`
	AccessRoles []string   `yaml:"access_roles"`
	WelcomeMsg  string     `yaml:"welcome_msg"`
	EmbedBody   string     `yaml:"embed_body"`
	ButtonLabel string     `yaml:"button_label"`
	ButtonEmoji string     `yaml:"button_emoji"`
	Color       colorValue `yaml:"color"`
	AllowClaim  *bool      `yaml:"allow_claim"`
	AllowClose  *bool      `yaml:"allow_close"`
	Seq         int        `yaml:"seq"`
}

func (t *TypeConfig) AllowClaimOn() bool { return t.AllowClaim == nil || *t.AllowClaim }
func (t *TypeConfig) AllowCloseOn() bool { return t.AllowClose == nil || *t.AllowClose }

// QuestionConfig is one open-time modal field for a panel.
type QuestionConfig struct {
	Label       string `yaml:"label"`
	Placeholder string `yaml:"placeholder,omitempty"`
	Style       string `yaml:"style,omitempty"` // "" | "short" | "paragraph"
	Required    bool   `yaml:"required"`
	Value       string `yaml:"value,omitempty"`
}

// PanelConfig is one posted panel: a channel + message carrying the open button.
type PanelConfig struct {
	Name        string           `yaml:"name"`
	ChannelID   string           `yaml:"channel_id"`
	MessageID   string           `yaml:"message_id"`
	TypeKey     string           `yaml:"type"`
	Title       string           `yaml:"title"`
	Description string           `yaml:"description"`
	Suspended   bool             `yaml:"suspended"`
	ModalTitle  string           `yaml:"modal_title,omitempty"`
	Questions   []QuestionConfig `yaml:"questions,omitempty"`
}

// ModuleConfig is the bot-wide (non-guild) ticket config: retention, dashboard
// close, and the open-time modal master switch.
type ModuleConfig struct {
	Version        int           `yaml:"version"`
	Retention      retentionDays `yaml:"storage_retention_days"`
	AllowDashClose bool          `yaml:"allow_dashboard_close"`
	ModalsEnabled  *bool         `yaml:"modals_enabled"`
}

// ModalsOn reports whether open-time question modals are enabled (nil = on).
func (c *ModuleConfig) ModalsOn() bool { return c.ModalsEnabled == nil || *c.ModalsEnabled }

// RetentionDays returns the configured retention, defaulting to 30 days.
func (c *ModuleConfig) RetentionDays() int {
	if c.Retention.set {
		return c.Retention.value
	}
	return defaultRetentionDays
}

// Config is one guild's ticket config: types, panels, and the log channel.
type Config struct {
	Version    int                    `yaml:"version"`
	LogChannel string                 `yaml:"log_channel"`
	Types      map[string]*TypeConfig `yaml:"types"`
	Panels     map[string]PanelConfig `yaml:"panels"`
	parsed     bool
}

// retentionDays distinguishes an explicit 0 (keep forever) from an unset
// default.
type retentionDays struct {
	value int
	set   bool
}

// UnmarshalYAML implements yaml.Unmarshaler. A QUOTED numeric scalar
// ("30") is accepted — yaml.v3 refuses to decode a !!str into an int, so a
// hand-edited quoted value would otherwise fail the whole module load.
func (r *retentionDays) UnmarshalYAML(value *yaml.Node) error {
	if value.Tag == "!!null" {
		return nil // unset → default
	}
	if n, err := strconv.Atoi(strings.TrimSpace(value.Value)); err == nil {
		r.value = n
		r.set = true
		return nil
	}
	// Fall back to yaml's own decoding so unquoted scalars keep working
	// (e.g. 0x1e) and junk gets yaml's descriptive error.
	var v int
	if err := value.Decode(&v); err != nil {
		return fmt.Errorf("storage_retention_days must be an integer: %w", err)
	}
	r.value = v
	r.set = true
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (r retentionDays) MarshalYAML() (interface{}, error) {
	if !r.set {
		return nil, nil
	}
	return r.value, nil
}

// colorValue is an int color that accepts 0xRRGGBB, #RRGGBB, or decimal.
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

// ── paths ──────────────────────────────────────────────────────────────────

func moduleConfigPath(dataDir string) string { return filepath.Join(dataDir, "config.yml") }
func guildsRoot(dataDir string) string       { return filepath.Join(dataDir, "guilds") }
func guildConfigPath(dataDir, guildID string) string {
	return filepath.Join(guildsRoot(dataDir), guildID+".yml")
}

// ── load / save ────────────────────────────────────────────────────────────

// loadModuleConfig reads the bot-wide config; a missing file yields defaults
// (no write — the file is created on first save).
func loadModuleConfig(dataDir string) (*ModuleConfig, error) {
	cfg := &ModuleConfig{Version: configVersion}
	raw, err := os.ReadFile(moduleConfigPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse module config: %w", err)
	}
	cfg.Version = configVersion
	return cfg, nil
}

// loadGuildConfig reads one guild's config; a missing file yields empty maps.
// Per-panel question-validation failures clear that panel's questions (and
// modal title) and WARN, keeping the panel itself.
func loadGuildConfig(dataDir, guildID string, log modules.Logger) *Config {
	empty := func() *Config {
		return &Config{Version: configVersion, Types: map[string]*TypeConfig{}, Panels: map[string]PanelConfig{}}
	}
	raw, err := os.ReadFile(guildConfigPath(dataDir, guildID))
	if err != nil {
		if os.IsNotExist(err) {
			return empty()
		}
		if log != nil {
			log.Warn("Tickets: could not read guild config %s: %v", guildID, err)
		}
		return empty()
	}
	cfg := empty()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		if log != nil {
			log.Warn("Tickets: parse guild config %s failed: %v", guildID, err)
		}
		return empty()
	}
	for name, p := range cfg.Panels {
		if err := validateQuestions(p.Questions); err != nil {
			if log != nil {
				log.Warn("Tickets: panel %s questions invalid (%v); cleared", name, err)
			}
			p.Questions = nil
			p.ModalTitle = ""
			cfg.Panels[name] = p
		}
	}
	cfg.parsed = true
	return cfg
}

// save writes the bot-wide config atomically (tmp + rename, 0600).
func (c *ModuleConfig) save(dataDir string) error {
	raw, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return atomicWrite(moduleConfigPath(dataDir), raw)
}

// save writes one guild's config atomically (tmp + rename, 0600).
func (c *Config) save(dataDir, guildID string) error {
	if err := os.MkdirAll(guildsRoot(dataDir), 0755); err != nil {
		return err
	}
	raw, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return atomicWrite(guildConfigPath(dataDir, guildID), raw)
}

func atomicWrite(path string, raw []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ── validation ─────────────────────────────────────────────────────────────

// validateQuestions enforces the modal field limits: at most 5 questions,
// each with a non-empty label (<=45 runes), placeholder (<=100), value
// (<=4000), and a valid style.
func validateQuestions(qs []QuestionConfig) error {
	if len(qs) > maxModalQuestions {
		return fmt.Errorf("at most %d questions per panel (got %d)", maxModalQuestions, len(qs))
	}
	for i, q := range qs {
		if q.Label == "" {
			return fmt.Errorf("question %d: label is required", i+1)
		}
		if len([]rune(q.Label)) > maxQuestionLabel {
			return fmt.Errorf("question %d: label exceeds %d chars", i+1, maxQuestionLabel)
		}
		if len([]rune(q.Placeholder)) > maxQuestionPlaceholder {
			return fmt.Errorf("question %d: placeholder exceeds %d chars", i+1, maxQuestionPlaceholder)
		}
		if len([]rune(q.Value)) > maxQuestionValue {
			return fmt.Errorf("question %d: value exceeds %d chars", i+1, maxQuestionValue)
		}
		if q.Style != "" && q.Style != "short" && q.Style != "paragraph" {
			return fmt.Errorf("question %d: style must be \"\", \"short\" or \"paragraph\"", i+1)
		}
	}
	return nil
}

// ── legacy migration ───────────────────────────────────────────────────────

// legacyConfig is the v1/v2 single-file config shape (all guilds in one file).
type legacyConfig struct {
	Version        int                    `yaml:"version"`
	GroupsYAML     string                 `yaml:"groups_yaml"`
	Types          map[string]*TypeConfig `yaml:"types"`
	Panels         map[string]PanelConfig `yaml:"panels"`
	LogChannel     string                 `yaml:"log_channel"`
	Retention      retentionDays          `yaml:"storage_retention_days"`
	AllowDashClose bool                   `yaml:"allow_dashboard_close"`
}

// isV1Config reports whether a raw config file is the v1 groups_yaml shape.
func isV1Config(raw []byte) bool {
	var probe struct {
		Version    int    `yaml:"version"`
		GroupsYAML string `yaml:"groups_yaml"`
	}
	if err := yaml.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.Version < 2 && probe.GroupsYAML != ""
}

// migrateV1 converts a v1 groups_yaml config into per-type entries.
func migrateV1(cfg *legacyConfig) error {
	groups, err := parseGroupsYAML(cfg.GroupsYAML)
	if err != nil {
		return fmt.Errorf("parse legacy groups: %w", err)
	}
	cfg.Types = map[string]*TypeConfig{}
	for _, g := range groups {
		cfg.Types[g.Key] = &TypeConfig{
			Key:        g.Key,
			Label:      g.Label,
			Enabled:    g.Enabled,
			Category:   g.ParentChannel,
			PingRoles:  g.PingRoles,
			EmbedBody:  g.EmbedTemplate,
			Color:      g.Color,
			AllowClaim: g.AllowClaim,
			AllowClose: g.AllowClose,
		}
	}
	return nil
}

// validPanelName accepts letters, digits, '-' and '_' (<=64 chars) — the
// charset shared by panel names, type keys, and custom_id payloads.
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
