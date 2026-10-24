package reactionroles

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/disgoorg/snowflake/v2"
	"gopkg.in/yaml.v3"

	"github.com/misfit/bot/modules"
)

const (
	maxPanels      = 50
	maxMappings    = 20
	maxPanelName   = 100
	maxTitleLen    = 256
	maxDescLen     = 4096
	maxFooterLen   = 2048
	maxEmbedTotal  = 6000
	maxPostedEmoji = 100
)

type configStore struct {
	mu    sync.RWMutex
	dir   string
	cache map[string]modules.ReactionRolesConfig
}

func openConfigStore(dir string) (*configStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create reaction roles config dir: %w", err)
	}
	return &configStore{dir: dir, cache: map[string]modules.ReactionRolesConfig{}}, nil
}

func (s *configStore) configPath(guildID string) (string, error) {
	if !validGuildID(guildID) {
		return "", fmt.Errorf("invalid guild id")
	}
	return filepath.Join(s.dir, guildID+".yml"), nil
}

func (s *configStore) Get(guildID string) (modules.ReactionRolesConfig, error) {
	path, err := s.configPath(guildID)
	if err != nil {
		return modules.ReactionRolesConfig{}, err
	}
	s.mu.RLock()
	if cfg, ok := s.cache[guildID]; ok {
		s.mu.RUnlock()
		return cloneConfig(cfg), nil
	}
	s.mu.RUnlock()

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return modules.ReactionRolesConfig{NextID: 1}, nil
	}
	if err != nil {
		return modules.ReactionRolesConfig{}, fmt.Errorf("read reaction roles config: %w", err)
	}
	var cfg modules.ReactionRolesConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return modules.ReactionRolesConfig{}, fmt.Errorf("corrupt reaction roles config for guild %s: %w", guildID, err)
	}
	norm, err := normalizeConfig(cfg)
	if err != nil {
		return modules.ReactionRolesConfig{}, fmt.Errorf("invalid reaction roles config for guild %s: %w", guildID, err)
	}
	s.mu.Lock()
	s.cache[guildID] = norm
	s.mu.Unlock()
	return cloneConfig(norm), nil
}

func (s *configStore) Save(guildID string, cfg modules.ReactionRolesConfig) error {
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
		return fmt.Errorf("encode reaction roles config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write reaction roles config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save reaction roles config: %w", err)
	}
	s.mu.Lock()
	s.cache[guildID] = norm
	s.mu.Unlock()
	return nil
}

func cloneConfig(cfg modules.ReactionRolesConfig) modules.ReactionRolesConfig {
	out := cfg
	out.Panels = make([]modules.ReactionPanel, len(cfg.Panels))
	for i, p := range cfg.Panels {
		out.Panels[i] = p
		out.Panels[i].Mappings = append([]modules.ReactionRoleMapping(nil), p.Mappings...)
		out.Panels[i].PostedEmojis = append([]string(nil), p.PostedEmojis...)
	}
	return out
}

func normalizeConfig(cfg modules.ReactionRolesConfig) (modules.ReactionRolesConfig, error) {
	if cfg.NextID < 1 {
		cfg.NextID = 1
	}
	if len(cfg.Panels) > maxPanels {
		return cfg, fmt.Errorf("at most %d panels are allowed", maxPanels)
	}
	seenIDs := map[int]bool{}
	maxID := 0
	for i := range cfg.Panels {
		p, err := normalizePanel(cfg.Panels[i])
		if err != nil {
			return cfg, fmt.Errorf("panel %d: %w", i+1, err)
		}
		if p.ID > maxID {
			maxID = p.ID
		}
		if seenIDs[p.ID] {
			return cfg, fmt.Errorf("duplicate panel id %d", p.ID)
		}
		seenIDs[p.ID] = true
		cfg.Panels[i] = p
	}
	if cfg.NextID <= maxID {
		cfg.NextID = maxID + 1
	}
	sort.Slice(cfg.Panels, func(i, j int) bool { return cfg.Panels[i].ID < cfg.Panels[j].ID })
	return cfg, nil
}

func normalizePanel(p modules.ReactionPanel) (modules.ReactionPanel, error) {
	if p.ID <= 0 {
		return p, fmt.Errorf("invalid id")
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		p.Name = fmt.Sprintf("Panel %d", p.ID)
	}
	if charLen(p.Name) > maxPanelName {
		return p, fmt.Errorf("name is too long (max %d)", maxPanelName)
	}

	var err error
	if p.ChannelID, err = snowflakeIDString(p.ChannelID); err != nil {
		return p, fmt.Errorf("channel: %w", err)
	}
	if p.MessageID, err = snowflakeIDString(p.MessageID); err != nil {
		return p, fmt.Errorf("message: %w", err)
	}
	if p.PostedChannelID, err = snowflakeIDString(p.PostedChannelID); err != nil {
		return p, fmt.Errorf("posted channel: %w", err)
	}
	if p.Embed, err = normalizeEmbed(p.Embed); err != nil {
		return p, err
	}
	if len(p.Mappings) > maxMappings {
		return p, fmt.Errorf("at most %d mappings are allowed", maxMappings)
	}
	seenEmoji := map[string]bool{}
	for i := range p.Mappings {
		m := &p.Mappings[i]
		emoji, err := normalizeEmoji(m.Emoji)
		if err != nil {
			return p, fmt.Errorf("mapping %d: %w", i+1, err)
		}
		roleID, err := snowflakeIDString(m.RoleID)
		if err != nil || roleID == "" {
			return p, fmt.Errorf("mapping %d: choose a role", i+1)
		}
		key := emojiUniqueKey(emoji)
		if seenEmoji[key] {
			return p, fmt.Errorf("mapping %d: duplicate emoji", i+1)
		}
		seenEmoji[key] = true
		m.Emoji = emoji
		m.RoleID = roleID
	}
	posted := make([]string, 0, len(p.PostedEmojis))
	seenPosted := map[string]bool{}
	for _, raw := range p.PostedEmojis {
		emoji, err := normalizeEmoji(raw)
		if err != nil {
			continue // internal state: drop corrupt entries rather than break the panel
		}
		key := emojiUniqueKey(emoji)
		if seenPosted[key] {
			continue
		}
		seenPosted[key] = true
		posted = append(posted, emoji)
		if len(posted) >= maxPostedEmoji {
			break
		}
	}
	p.PostedEmojis = posted
	return p, nil
}

func normalizeEmbed(e modules.ReactionRoleEmbed) (modules.ReactionRoleEmbed, error) {
	e.Title = strings.TrimSpace(e.Title)
	e.Description = strings.TrimSpace(e.Description)
	e.Footer = strings.TrimSpace(e.Footer)
	e.ImageURL = strings.TrimSpace(e.ImageURL)
	e.ThumbnailURL = strings.TrimSpace(e.ThumbnailURL)
	color, err := normalizeColor(e.Color)
	if err != nil {
		return e, err
	}
	e.Color = color
	return e, nil
}

func validateConfig(cfg modules.ReactionRolesConfig) error {
	for _, p := range cfg.Panels {
		if err := validatePanel(p); err != nil {
			return fmt.Errorf("panel %q: %w", p.Name, err)
		}
	}
	return nil
}

func validatePanel(p modules.ReactionPanel) error {
	if charLen(p.Embed.Title) > maxTitleLen {
		return fmt.Errorf("title is too long (max %d)", maxTitleLen)
	}
	if charLen(p.Embed.Description) > maxDescLen {
		return fmt.Errorf("description is too long (max %d)", maxDescLen)
	}
	if charLen(p.Embed.Footer) > maxFooterLen {
		return fmt.Errorf("footer is too long (max %d)", maxFooterLen)
	}
	total := charLen(p.Embed.Title) + charLen(p.Embed.Description) + charLen(p.Embed.Footer)
	if total > maxEmbedTotal {
		return fmt.Errorf("embed is too long (%d/%d characters)", total, maxEmbedTotal)
	}
	for label, u := range map[string]string{
		"image url":     p.Embed.ImageURL,
		"thumbnail url": p.Embed.ThumbnailURL,
	} {
		if !validEmbedURL(u) {
			return fmt.Errorf("%s must be an http(s) URL", label)
		}
	}
	if len(p.Mappings) > maxMappings {
		return fmt.Errorf("at most %d mappings are allowed", maxMappings)
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

func charLen(s string) int {
	return len(utf16.Encode([]rune(s)))
}
