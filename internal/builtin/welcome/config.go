package welcome

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/disgoorg/snowflake/v2"
	"gopkg.in/yaml.v3"

	"github.com/misfit/bot/modules"
)

// Discord limits (UTF-16 code units, matching how Discord counts characters).
const (
	maxContentLen     = 2000
	maxTitleLen       = 256
	maxDescriptionLen = 4096
	maxFields         = 25
	maxFieldNameLen   = 256
	maxFieldValueLen  = 1024
	maxFooterLen      = 2048
	maxAuthorNameLen  = 256
	maxEmbedTotalLen  = 6000
	maxAutoDeleteSecs = 24 * 60 * 60
)

// store persists one YAML file per guild under <dataDir>/guilds/<guildID>.yaml
// and keeps decoded configs in memory. All mutations flush atomically
// (tmp + rename); reads return copies so callers never share store state.
type store struct {
	mu    sync.RWMutex
	dir   string
	cache map[string]modules.WelcomeConfig
}

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create welcome data dir: %w", err)
	}
	return &store{dir: dir, cache: map[string]modules.WelcomeConfig{}}, nil
}

// configPath validates the guild ID before building a path from it: a value
// like ".." or "abc" must never escape the data directory.
func (s *store) configPath(guildID string) (string, error) {
	if !validGuildID(guildID) {
		return "", fmt.Errorf("invalid guild id")
	}
	return filepath.Join(s.dir, guildID+".yaml"), nil
}

// Get returns the guild's config, loading it from disk on first use. A missing
// file is the zero config (nothing enabled); a corrupt file is an error so the
// caller can surface it instead of silently overwriting user data.
func (s *store) Get(guildID string) (modules.WelcomeConfig, error) {
	path, err := s.configPath(guildID)
	if err != nil {
		return modules.WelcomeConfig{}, err
	}
	s.mu.RLock()
	if cfg, ok := s.cache[guildID]; ok {
		s.mu.RUnlock()
		return cfg, nil
	}
	s.mu.RUnlock()

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return modules.WelcomeConfig{}, nil
	}
	if err != nil {
		return modules.WelcomeConfig{}, fmt.Errorf("read welcome config: %w", err)
	}
	var cfg modules.WelcomeConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return modules.WelcomeConfig{}, fmt.Errorf("corrupt welcome config for guild %s: %w", guildID, err)
	}
	norm, err := normalizeConfig(cfg)
	if err != nil {
		return modules.WelcomeConfig{}, fmt.Errorf("invalid welcome config for guild %s: %w", guildID, err)
	}

	s.mu.Lock()
	s.cache[guildID] = norm
	s.mu.Unlock()
	return norm, nil
}

// Save normalizes, validates and persists the config, then updates the cache.
func (s *store) Save(guildID string, cfg modules.WelcomeConfig) error {
	path, err := s.configPath(guildID)
	if err != nil {
		return err
	}
	norm, err := normalizeConfig(cfg)
	if err != nil {
		return err
	}
	if err := validateConfig(norm); err != nil {
		return err
	}
	raw, err := yaml.Marshal(norm)
	if err != nil {
		return fmt.Errorf("encode welcome config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write welcome config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save welcome config: %w", err)
	}
	s.mu.Lock()
	s.cache[guildID] = norm
	s.mu.Unlock()
	return nil
}

// normalizeConfig trims/normalizes values that have exactly one valid spelling
// (ids, URLs, colors, timestamps) without rejecting them. Validation follows.
func normalizeConfig(cfg modules.WelcomeConfig) (modules.WelcomeConfig, error) {
	var err error
	if cfg.Join.ChannelID, err = snowflakeIDString(cfg.Join.ChannelID); err != nil {
		return cfg, fmt.Errorf("join channel: %w", err)
	}
	if cfg.Leave.ChannelID, err = snowflakeIDString(cfg.Leave.ChannelID); err != nil {
		return cfg, fmt.Errorf("leave channel: %w", err)
	}
	if cfg.Join.Embed, err = normalizeEmbed(cfg.Join.Embed); err != nil {
		return cfg, fmt.Errorf("join embed: %w", err)
	}
	if cfg.Leave.Embed, err = normalizeEmbed(cfg.Leave.Embed); err != nil {
		return cfg, fmt.Errorf("leave embed: %w", err)
	}
	return cfg, nil
}

func normalizeEmbed(e modules.WelcomeEmbed) (modules.WelcomeEmbed, error) {
	e.Title = strings.TrimSpace(e.Title)
	e.Description = strings.TrimSpace(e.Description)
	e.ImageURL = strings.TrimSpace(e.ImageURL)
	e.ThumbnailURL = strings.TrimSpace(e.ThumbnailURL)
	e.Footer = strings.TrimSpace(e.Footer)
	e.FooterIcon = strings.TrimSpace(e.FooterIcon)
	e.TitleURL = strings.TrimSpace(e.TitleURL)
	e.Author.Name = strings.TrimSpace(e.Author.Name)
	e.Author.URL = strings.TrimSpace(e.Author.URL)
	e.Author.IconURL = strings.TrimSpace(e.Author.IconURL)

	color, err := normalizeColor(e.Color)
	if err != nil {
		return e, err
	}
	e.Color = color

	if e.Timestamp != "" {
		t, err := time.Parse(time.RFC3339, e.Timestamp)
		if err != nil {
			return e, fmt.Errorf("timestamp must be RFC3339")
		}
		e.Timestamp = t.UTC().Format(time.RFC3339)
	}
	for i := range e.Fields {
		e.Fields[i].Name = strings.TrimSpace(e.Fields[i].Name)
		e.Fields[i].Value = strings.TrimSpace(e.Fields[i].Value)
	}
	return e, nil
}

func normalizeColor(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	s = strings.TrimPrefix(strings.ToLower(s), "#")
	switch len(s) {
	case 3:
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	case 6:
	default:
		return "", fmt.Errorf("color must be a hex value like #5865f2")
	}
	if _, err := strconv.ParseUint(s, 16, 32); err != nil {
		return "", fmt.Errorf("color must be a hex value like #5865f2")
	}
	return "#" + s, nil
}

// parseColor converts a normalized "#rrggbb" to the integer disgo expects.
func parseColor(s string) int {
	s = strings.TrimPrefix(s, "#")
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0
	}
	return int(v)
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

func validateConfig(cfg modules.WelcomeConfig) error {
	if err := validateEvent("join", cfg.Join); err != nil {
		return err
	}
	return validateEvent("leave", cfg.Leave)
}

func validateEvent(name string, ev modules.WelcomeEvent) error {
	if ev.AutoDelete < 0 || ev.AutoDelete > maxAutoDeleteSecs {
		return fmt.Errorf("%s: auto-delete must be between 0 and %d seconds", name, maxAutoDeleteSecs)
	}
	if charLen(ev.Message) > maxContentLen {
		return fmt.Errorf("%s: message is %d characters (Discord allows %d)", name, charLen(ev.Message), maxContentLen)
	}
	if err := validateEmbed(ev.Embed); err != nil {
		return fmt.Errorf("%s embed: %w", name, err)
	}
	if ev.Enabled {
		if ev.ChannelID == "" {
			return fmt.Errorf("%s: choose a channel before enabling", name)
		}
		if strings.TrimSpace(ev.Message) == "" && !(ev.Embed.Enabled && embedHasContent(ev.Embed)) {
			return fmt.Errorf("%s: write a message or enable an embed before enabling", name)
		}
	}
	return nil
}

func validateEmbed(e modules.WelcomeEmbed) error {
	if _, err := normalizeColor(e.Color); err != nil {
		return err
	}
	if charLen(e.Title) > maxTitleLen {
		return fmt.Errorf("title is too long (max %d)", maxTitleLen)
	}
	if charLen(e.Description) > maxDescriptionLen {
		return fmt.Errorf("description is too long (max %d)", maxDescriptionLen)
	}
	if charLen(e.Footer) > maxFooterLen {
		return fmt.Errorf("footer is too long (max %d)", maxFooterLen)
	}
	if charLen(e.Author.Name) > maxAuthorNameLen {
		return fmt.Errorf("author name is too long (max %d)", maxAuthorNameLen)
	}
	if len(e.Fields) > maxFields {
		return fmt.Errorf("too many fields (max %d)", maxFields)
	}
	total := charLen(e.Title) + charLen(e.Description) + charLen(e.Footer) + charLen(e.Author.Name)
	for i, f := range e.Fields {
		if charLen(f.Name) > maxFieldNameLen {
			return fmt.Errorf("field %d name is too long (max %d)", i+1, maxFieldNameLen)
		}
		if charLen(f.Value) > maxFieldValueLen {
			return fmt.Errorf("field %d value is too long (max %d)", i+1, maxFieldValueLen)
		}
		if e.Enabled && strings.TrimSpace(f.Name) == "" {
			return fmt.Errorf("field %d needs a name", i+1)
		}
		if e.Enabled && strings.TrimSpace(f.Value) == "" {
			return fmt.Errorf("field %d needs a value", i+1)
		}
		total += charLen(f.Name) + charLen(f.Value)
	}
	if total > maxEmbedTotalLen {
		return fmt.Errorf("embed is too long (%d characters, max %d)", total, maxEmbedTotalLen)
	}
	for label, u := range map[string]string{
		"author url":      e.Author.URL,
		"author icon url": e.Author.IconURL,
		"title url":       e.TitleURL,
		"image url":       e.ImageURL,
		"thumbnail url":   e.ThumbnailURL,
		"footer icon url": e.FooterIcon,
	} {
		if !validEmbedURL(u) {
			return fmt.Errorf("%s must be an http(s) URL", label)
		}
	}
	return nil
}

func validEmbedURL(s string) bool {
	if s == "" {
		return true
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// embedHasContent reports whether the embed would render anything visible.
func embedHasContent(e modules.WelcomeEmbed) bool {
	if strings.TrimSpace(e.Title) != "" ||
		strings.TrimSpace(e.Description) != "" ||
		strings.TrimSpace(e.Author.Name) != "" ||
		strings.TrimSpace(e.ImageURL) != "" ||
		strings.TrimSpace(e.ThumbnailURL) != "" ||
		strings.TrimSpace(e.Footer) != "" {
		return true
	}
	for _, f := range e.Fields {
		if strings.TrimSpace(f.Name) != "" || strings.TrimSpace(f.Value) != "" {
			return true
		}
	}
	return false
}

// charLen counts UTF-16 code units, the unit Discord uses for its limits.
func charLen(s string) int {
	return len(utf16.Encode([]rune(s)))
}
