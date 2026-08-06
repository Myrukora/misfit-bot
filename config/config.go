package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	FilePath  string              `yaml:"-"`
	Bot       BotConfig           `yaml:"bot"`
	Modules   ModulesConfig       `yaml:"modules"`
	Logging   LoggingConfig       `yaml:"logging"`
	OAuth     OAuthConfig         `yaml:"oauth"`
	Dashboard DashboardCoreConfig `yaml:"dashboard"`
	Updater   UpdaterConfig       `yaml:"updater"`
}

// OAuthConfig holds the Discord application's OAuth2 client secret. Unlike
// the bot token (which authorizes the bot account), the OAuth client secret
// authorizes user-facing OAuth flows (e.g. dashboard login). It lives in the
// core config.yml so any module that performs Discord OAuth can read it from
// one place instead of each keeping its own copy.
//
// The client_id half is NOT stored here: it equals the bot's application ID
// (ctx.Bot.GetClient().(*bot.Client).ApplicationID), so there's nothing for
// the owner to fill in.
type OAuthConfig struct {
	ClientSecret string `yaml:"client_secret"` // from Dev Portal → OAuth2 → General; empty = not configured
}

// DashboardCoreConfig holds the non-secret, infrastructure-level dashboard
// settings that the owner may want to pin from the main config.yml (e.g. the
// listen port — useful when the default 127.0.0.1:8080 is already taken and
// the dashboard can't start to be reconfigured via the web). The OAuth client
// secret lives in the separate [OAuthConfig] section (also in core config).
// Only the dashboard's per-installation session_secret and the allowed_guilds
// allowlist stay in the module's own 0600 config file. When a core field here
// is set, it takes priority over the dashboard module's own value.
type DashboardCoreConfig struct {
	Listen    string `yaml:"listen"`     // e.g. "127.0.0.1:9090"; empty = module default 127.0.0.1:8080
	PublicURL string `yaml:"public_url"` // e.g. "https://dashboard.example.com"
}

// NormalizeListen coerces a user-supplied listen address into a bare
// host:port (or ":port") suitable for net.Listen. It forgives inputs that
// look like URLs ("http://127.0.0.1:9090/", "https://host:8080/callback") by
// stripping any scheme and path. Inputs like "127.0.0.1:9090", ":8080", or
// "0.0.0.0:8080" are returned unchanged. IPv6 forms ("[::1]:8080") survive.
func NormalizeListen(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "://"); i >= 0 { // strip scheme http://, https://, tcp://, …
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 { // strip any trailing path/query
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

type BotConfig struct {
	Token       string   `yaml:"token"`
	Prefix      string   `yaml:"prefix"`
	OwnerID     string   `yaml:"owner_id"`
	ElevatedIDs []string `yaml:"elevated_ids"`
	ToS         string   `yaml:"tos_url"`
	Privacy     string   `yaml:"privacy_url"`
	Name        string   `yaml:"name"`
}

type ModulesConfig struct {
	AutoLoad bool     `yaml:"auto_load"`
	Path     string   `yaml:"path"`
	Disabled []string `yaml:"disabled"`
}

type LoggingConfig struct {
	Enabled  bool   `yaml:"enabled"`
	FilePath string `yaml:"file_path"`
	Level    string `yaml:"level"`
}

// UpdaterConfig controls the bot's self-update integration with its own
// GitHub repository: polling for new commits/PRs (posted as embeds to
// notify_channel) and automatically pulling, rebuilding and re-launching
// itself when new commits land on the tracked branch.
//
// The token lives here in core config.yml (which is gitignored) so it never
// reaches the repository. The repo must be private (the bot's own code).
type UpdaterConfig struct {
	Enabled       bool   `yaml:"enabled"`        // master switch; false = updater does nothing
	Repo          string `yaml:"repo"`           // owner/name of the bot's GitHub repository; empty = feature off
	Branch        string `yaml:"branch"`         // branch to track; default "main"
	Token         string `yaml:"token"`          // GitHub PAT (or gh token); empty = anonymous/credential-helper fallback
	CheckInterval int    `yaml:"check_interval"` // seconds between polls; default 300, minimum 30
	AutoPull      bool   `yaml:"auto_pull"`      // automatically pull + rebuild + restart on new commits
	NotifyChannel string `yaml:"notify_channel"` // Discord channel ID for PR/commit embeds; empty = notifications skipped
}

var DefaultConfig = &Config{
	Bot: BotConfig{
		Prefix:  "[p]",
		Name:    "Bot",
		ToS:     "",
		Privacy: "",
	},
	Modules: ModulesConfig{
		AutoLoad: true,
		Path:     "modules",
		Disabled: []string{},
	},
	Logging: LoggingConfig{
		Enabled:  true,
		FilePath: "logs/bot.log",
		Level:    "info",
	},
	Updater: UpdaterConfig{
		Enabled:       true,
		Branch:        "main",
		CheckInterval: 300,
		AutoPull:      true,
	},
}

func Load(dir string) (*Config, error) {
	path := filepath.Join(dir, "config.yml")

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("no config.yml found at %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config.yml: %w", err)
	}

	// Start from defaults so sections missing from config.yml (e.g. a fresh
	// `updater:` block on an existing install) get sane values; the YAML is
	// then overlaid on top.
	cfg := *DefaultConfig
	cfg.FilePath = path
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config.yml: %w", err)
	}

	return &cfg, nil
}

func Save(cfg *Config, dir string) error {
	if cfg.FilePath == "" {
		cfg.FilePath = filepath.Join(dir, "config.yml")
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(cfg.FilePath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	return os.WriteFile(cfg.FilePath, data, 0644)
}

func Exists(dir string) bool {
	path := filepath.Join(dir, "config.yml")
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

func (c *Config) Set(key, value string) error {
	switch key {
	case "prefix":
		if value == "" {
			return fmt.Errorf("prefix cannot be empty")
		}
		c.Bot.Prefix = value
	case "token":
		c.Bot.Token = value
	case "owner_id":
		c.Bot.OwnerID = value
	case "tos_url":
		c.Bot.ToS = value
	case "privacy_url":
		c.Bot.Privacy = value
	case "name":
		c.Bot.Name = value
	case "log_level":
		switch value {
		case "debug", "info", "warn", "error":
			c.Logging.Level = value
		default:
			return fmt.Errorf("invalid log_level: %q (must be debug/info/warn/error)", value)
		}
		// Note: logger level is fixed at startup. Changes require a restart.
	case "log_enabled":
		v, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("invalid log_enabled: %v", err)
		}
		c.Logging.Enabled = v
	case "log_file_path":
		v := strings.TrimSpace(value)
		if v == "" {
			return fmt.Errorf("log file path cannot be empty")
		}
		c.Logging.FilePath = v
	case "modules_auto_load":
		v, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("invalid modules_auto_load: %v", err)
		}
		c.Modules.AutoLoad = v
	case "dashboard_listen":
		c.Dashboard.Listen = NormalizeListen(value)
	case "dashboard_public_url":
		v := strings.TrimRight(strings.TrimSpace(value), "/")
		if v != "" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			return fmt.Errorf("dashboard_public_url must start with http:// or https://")
		}
		c.Dashboard.PublicURL = v
	case "oauth_client_secret":
		c.OAuth.ClientSecret = strings.TrimSpace(value)
	case "updater_enabled":
		v, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("invalid updater_enabled: %v", err)
		}
		c.Updater.Enabled = v
	case "updater_repo":
		v := strings.TrimSpace(value)
		if v == "" {
			return fmt.Errorf("updater repo cannot be empty")
		}
		if !strings.Contains(v, "/") || strings.HasPrefix(v, "/") || strings.HasSuffix(v, "/") {
			return fmt.Errorf("updater repo must be in owner/name form, e.g. Myrukora/misfit-bot")
		}
		c.Updater.Repo = v
	case "updater_branch":
		v := strings.TrimSpace(value)
		if v == "" {
			return fmt.Errorf("updater branch cannot be empty")
		}
		c.Updater.Branch = v
	case "updater_token":
		c.Updater.Token = strings.TrimSpace(value)
	case "updater_interval":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 30 {
			return fmt.Errorf("invalid updater_interval: %q (must be a number of seconds, minimum 30)", value)
		}
		c.Updater.CheckInterval = n
	case "updater_auto_pull":
		v, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("invalid updater_auto_pull: %v", err)
		}
		c.Updater.AutoPull = v
	case "updater_notify_channel":
		v, err := normalizeChannelID(value)
		if err != nil {
			return fmt.Errorf("invalid updater_notify_channel: %v", err)
		}
		c.Updater.NotifyChannel = v
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return Save(c, filepath.Dir(c.FilePath))
}

// normalizeChannelID accepts a Discord channel mention (<#1234567890>) or a
// bare numeric ID and returns the bare ID. Channel NAMES are rejected — they
// cannot be resolved to an ID without the cache, and storing them would break
// every later snowflake parse ("invalid notify channel").
func normalizeChannelID(value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", nil
	}
	if strings.HasPrefix(v, "<#") && strings.HasSuffix(v, ">") {
		v = strings.TrimSuffix(strings.TrimPrefix(v, "<#"), ">")
	}
	if _, err := strconv.ParseUint(v, 10, 64); err != nil {
		return "", fmt.Errorf("%q is not a channel mention or numeric ID (paste the #channel mention)", value)
	}
	return v, nil
}

// parseBool accepts the same value set as the log_enabled handling
// (true/false/1/0/yes/no/on/off, case-insensitive) and rejects anything else
// instead of silently coercing it.
func parseBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%q is not a boolean (use true/false/1/0/yes/no/on/off)", value)
	}
}
