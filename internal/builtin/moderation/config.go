package moderation

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/disgoorg/snowflake/v2"
	"gopkg.in/yaml.v3"

	"github.com/misfit/bot/modules"
)

const (
	maxRules            = 50
	maxWarnThreshold    = 1000
	maxTimeoutMinutes   = 28 * 24 * 60 // Discord's timeout ceiling
	maxWarnExpiryDays   = 3650
	maxBanDeleteSeconds = 7 * 24 * 60 * 60
)

// configStore persists one YAML file per guild under <dataDir>/guilds/.
type configStore struct {
	mu    sync.RWMutex
	dir   string
	cache map[string]modules.ModerationConfig
}

func openConfigStore(dir string) (*configStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create moderation config dir: %w", err)
	}
	return &configStore{dir: dir, cache: map[string]modules.ModerationConfig{}}, nil
}

// defaultConfig is returned for a guild with no saved file. Warnings are
// recorded by default, but no action is taken until the guild enables the
// module and configures channels/rules.
func defaultConfig() modules.ModerationConfig {
	return modules.ModerationConfig{WarnNotify: true}
}

func (s *configStore) configPath(guildID string) (string, error) {
	if !validGuildID(guildID) {
		return "", fmt.Errorf("invalid guild id")
	}
	return filepath.Join(s.dir, guildID+".yaml"), nil
}

func (s *configStore) Get(guildID string) (modules.ModerationConfig, error) {
	path, err := s.configPath(guildID)
	if err != nil {
		return modules.ModerationConfig{}, err
	}
	s.mu.RLock()
	if cfg, ok := s.cache[guildID]; ok {
		s.mu.RUnlock()
		return cfg, nil
	}
	s.mu.RUnlock()

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return defaultConfig(), nil
	}
	if err != nil {
		return modules.ModerationConfig{}, fmt.Errorf("read moderation config: %w", err)
	}
	var cfg modules.ModerationConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return modules.ModerationConfig{}, fmt.Errorf("corrupt moderation config for guild %s: %w", guildID, err)
	}
	norm, err := normalizeConfig(cfg)
	if err != nil {
		return modules.ModerationConfig{}, fmt.Errorf("invalid moderation config for guild %s: %w", guildID, err)
	}
	s.mu.Lock()
	s.cache[guildID] = norm
	s.mu.Unlock()
	return norm, nil
}

func (s *configStore) Save(guildID string, cfg modules.ModerationConfig) error {
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
		return fmt.Errorf("encode moderation config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write moderation config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save moderation config: %w", err)
	}
	s.mu.Lock()
	s.cache[guildID] = norm
	s.mu.Unlock()
	return nil
}

func normalizeConfig(cfg modules.ModerationConfig) (modules.ModerationConfig, error) {
	var err error
	if cfg.AuditChannel, err = snowflakeIDString(cfg.AuditChannel); err != nil {
		return cfg, fmt.Errorf("audit channel: %w", err)
	}
	if cfg.WarnChannel, err = snowflakeIDString(cfg.WarnChannel); err != nil {
		return cfg, fmt.Errorf("warn channel: %w", err)
	}
	if len(cfg.Rules) > maxRules {
		return cfg, fmt.Errorf("at most %d warn rules are allowed", maxRules)
	}
	sort.Slice(cfg.Rules, func(i, j int) bool { return cfg.Rules[i].Threshold < cfg.Rules[j].Threshold })
	seen := map[int]bool{}
	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		r.Action = strings.ToLower(strings.TrimSpace(r.Action))
		if r.Action != modules.ModerationActionTimeout {
			r.DurationMinutes = 0
		}
		if seen[r.Threshold] {
			return cfg, fmt.Errorf("duplicate warn rule threshold %d", r.Threshold)
		}
		seen[r.Threshold] = true
	}
	return cfg, nil
}

func validateConfig(cfg modules.ModerationConfig) error {
	if cfg.WarnExpiryDays < 0 || cfg.WarnExpiryDays > maxWarnExpiryDays {
		return fmt.Errorf("warn expiry must be between 0 and %d days", maxWarnExpiryDays)
	}
	if cfg.BanDeleteSeconds < 0 || cfg.BanDeleteSeconds > maxBanDeleteSeconds {
		return fmt.Errorf("ban message deletion must be between 0 and %d seconds", maxBanDeleteSeconds)
	}
	for i, r := range cfg.Rules {
		if r.Threshold < 1 || r.Threshold > maxWarnThreshold {
			return fmt.Errorf("rule %d: threshold must be between 1 and %d", i+1, maxWarnThreshold)
		}
		switch r.Action {
		case modules.ModerationActionTimeout:
			if r.DurationMinutes < 1 || r.DurationMinutes > maxTimeoutMinutes {
				return fmt.Errorf("rule %d: timeout duration must be between 1 and %d minutes", i+1, maxTimeoutMinutes)
			}
		case modules.ModerationActionKick, modules.ModerationActionBan:
			if r.DurationMinutes != 0 {
				return fmt.Errorf("rule %d: %s does not take a duration", i+1, r.Action)
			}
		default:
			return fmt.Errorf("rule %d: action must be timeout, kick or ban", i+1)
		}
	}
	return nil
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
