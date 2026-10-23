package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Config struct {
	FilePath    string              `yaml:"-"`
	Bot         BotConfig           `yaml:"bot"`
	Intents     IntentsConfig       `yaml:"intents"`
	Modules     ModulesConfig       `yaml:"modules"`
	Logging     LoggingConfig       `yaml:"logging"`
	OAuth       OAuthConfig         `yaml:"oauth"`
	Dashboard   DashboardCoreConfig `yaml:"dashboard"`
	Updater     UpdaterConfig       `yaml:"updater"`
	MCP         MCPConfig           `yaml:"mcp"`
	ImageFilter ImageFilterConfig   `yaml:"imagefilter"`
}

// IntentsConfig selects the privileged gateway intents the bot asks Discord
// for. Both are optional: disabling one here — or Discord rejecting it (close
// 4014) — makes the bot reconnect without it and run in the degraded/gated
// mode described in the README. Presence is never requested: the bot only
// sets its own status.
type IntentsConfig struct {
	// MessageContent gates guild message text, attachments and embeds, so
	// prefix commands and the image filter depend on it.
	MessageContent bool `yaml:"message_content"`
	// GuildMembers gates the member cache, member events and permission
	// lookups. Without it the dashboard is gated to the owner/elevated users.
	GuildMembers bool `yaml:"guild_members"`
}

// ImageFilterConfig is the image filter's configuration, kept in the core
// config.yml instead of a module-local file. Guilds maps guild IDs to their
// settings; Variant is the bot-wide CLIP vision-tower variant.
type ImageFilterConfig struct {
	Variant string                      `yaml:"variant"`
	Guilds  map[string]ImageFilterGuild `yaml:"guilds"`
}

// ImageFilterGuild is one guild's image-filter settings.
type ImageFilterGuild struct {
	Enabled      bool    `yaml:"enabled"`
	Threshold    float64 `yaml:"threshold"`
	Punishment   string  `yaml:"punishment"`
	MuteDuration int     `yaml:"mute_duration"`
	LogChannel   string  `yaml:"log_channel"`
	DeleteOnNone bool    `yaml:"delete_on_none"`
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
	// ClientSecret is a SECRET and deliberately not serialized: it lives in
	// <dir>/.env as DISCORD_OAUTH_CLIENT_SECRET (0600) — see secrets.go.
	ClientSecret string `yaml:"-"`
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
	// Token is a SECRET and deliberately not serialized: it lives in
	// <dir>/.env as DISCORD_BOT_TOKEN (0600) — see secrets.go. Load fills it
	// from that file, Set writes it there, and Save never emits it.
	Token       string   `yaml:"-"`
	Prefix      string   `yaml:"prefix"`
	OwnerID     string   `yaml:"owner_id"`
	ElevatedIDs []string `yaml:"elevated_ids"`
	ToS         string   `yaml:"tos_url"`
	Privacy     string   `yaml:"privacy_url"`
	Name        string   `yaml:"name"`
	// Status is the Discord presence status (online/idle/dnd/invisible). It is
	// applied on every (re)start via SetPresence so the bot comes up with the
	// owner's chosen status instead of always online.
	Status string `yaml:"status"`
	// BotAllowlist is a list of bot user IDs whose prefix commands are
	// processed despite the standard "ignore all bots" rule. Intended for
	// QA/testing observer bots (single explicit IDs — never a blanket
	// bots-in-general exemption, so bot↔bot echo loops stay impossible).
	// Empty = default behavior (all bots ignored).
	BotAllowlist []string `yaml:"bot_allowlist,omitempty"`
}

type ModulesConfig struct {
	AutoLoad bool     `yaml:"auto_load"`
	Path     string   `yaml:"path"`
	Disabled []string `yaml:"disabled"`
	// EnabledModules gates the compiled-in feature modules (cleanup, tickets).
	// Missing key = enabled (zero-config default preserves current behavior).
	EnabledModules map[string]bool `yaml:"enabled_modules,omitempty"`
}

type LoggingConfig struct {
	Enabled  bool   `yaml:"enabled"`
	FilePath string `yaml:"file_path"`
	Level    string `yaml:"level"`
}

// UpdaterConfig controls the bot's self-update integration with its own
// GitHub repository: polling for new commits/PRs (posted as embeds to the
// per-server notify channels) and automatically pulling, rebuilding and
// re-launching itself when new commits land on the tracked branch.
//
// The token lives here in core config.yml (which is gitignored) so it never
// reaches the repository. The repo must be private (the bot's own code).
type UpdaterConfig struct {
	Enabled bool   `yaml:"enabled"` // master switch; false = updater does nothing
	Repo    string `yaml:"repo"`    // owner/name of the bot's GitHub repository; empty = feature off
	Branch  string `yaml:"branch"`  // branch to track; default "main"
	// Token is a SECRET and deliberately not serialized: it lives in
	// <dir>/.env as UPDATER_GITHUB_TOKEN (0600). Empty = anonymous /
	// credential-helper fallback.
	Token         string `yaml:"-"`
	CheckInterval int    `yaml:"check_interval"` // seconds between polls; default 300, minimum 30
	AutoPull      bool   `yaml:"auto_pull"`      // automatically pull + rebuild + restart on new commits
	// NotifyChannels is the per-server map of update-notification channels:
	// guild ID → channel ID. Each configured server gets its own PR/commit
	// embeds posted to its own channel, so multiple servers can watch the repo
	// in their own dedicated channels. Empty = no notifications.
	NotifyChannels map[string]string `yaml:"notify_channels,omitempty"`
}

// NotifyTargets returns the distinct channel IDs that receive update
// notifications (one per configured server), sorted for deterministic
// delivery order.
//
// notifyMu guards NotifyChannels against concurrent access: the updater's poll
// loop reads them (NotifyTargets) while owner actions — dashboard and MCP —
// write them through Set. Package-level rather than a Config field because
// Config is value-copied (Load, &Config{} literals in tests), which would make
// an embedded mutex a copylocks hazard; the live bot config is a single
// long-lived *Config.
var notifyMu sync.RWMutex

func (u *UpdaterConfig) NotifyTargets() []string {
	notifyMu.RLock()
	seen := make(map[string]bool, len(u.NotifyChannels))
	var out []string
	for _, ch := range u.NotifyChannels {
		if ch == "" || seen[ch] {
			continue
		}
		seen[ch] = true
		out = append(out, ch)
	}
	sort.Strings(out)
	notifyMu.RUnlock()
	return out
}

// SetNotifyChannel sets (or clears, when value is empty) one server's
// update-notification channel. It is the locked write counterpart of
// NotifyTargets: Config.Set's per-guild notify key routes through here so the
// map is never mutated without the guard.
func (u *UpdaterConfig) SetNotifyChannel(guildID, value string) {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	if u.NotifyChannels == nil {
		u.NotifyChannels = map[string]string{}
	}
	if value == "" {
		delete(u.NotifyChannels, guildID)
		return
	}
	u.NotifyChannels[guildID] = value
}

// NotifyChannelFor returns the update-notification channel configured for one
// server ("" = this server gets no update notifications).
func (u *UpdaterConfig) NotifyChannelFor(guildID string) string {
	notifyMu.RLock()
	defer notifyMu.RUnlock()
	return u.NotifyChannels[guildID]
}

// MCPConfig controls the built-in MCP server mounted at /mcp on the
// dashboard listener. The token authenticates bearer-token clients;
// empty = generated on first start and saved back to config.yml.
type MCPConfig struct {
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token"`
}

var DefaultConfig = &Config{
	Bot: BotConfig{
		Prefix:  "[p]",
		Name:    "Bot",
		ToS:     "",
		Privacy: "",
	},
	// Privileged intents are requested by default (the historical behavior);
	// the gateway automatically drops them on Discord's 4014 and runs gated
	// mode, and setting either to false never requests it at all.
	Intents: IntentsConfig{
		MessageContent: true,
		GuildMembers:   true,
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
	// MCP: enabled by default — a conscious choice. Exposure is bounded by the
	// auto-generated bearer token (generated on first start when empty) and
	// the dashboard listener binding 127.0.0.1:8080 by default (LAN-only
	// unless the owner rebinds). Load() starts from DefaultConfig then
	// overlays YAML, so a missing `mcp:` section on an existing install comes
	// up enabled (same precedent as `updater:`).
	MCP: MCPConfig{
		Enabled: true,
	},
	ImageFilter: ImageFilterConfig{
		Variant: "b32",
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

	// Secrets (see secrets.go) come from <dir>/.env, never from config.yml:
	// the corresponding struct fields are yaml:"-".
	cfg.Bot.Token = ReadEnvValue(dir, EnvBotToken)
	cfg.OAuth.ClientSecret = ReadEnvValue(dir, EnvOAuthClientSecret)
	cfg.Updater.Token = ReadEnvValue(dir, EnvUpdaterToken)

	return &cfg, nil
}

func Save(cfg *Config, dir string) error {
	if cfg.FilePath == "" {
		cfg.FilePath = filepath.Join(dir, "config.yml")
	}

	// Marshal under the notify-channel read guard: the marshal walks the
	// live NotifyChannels map, which the updater poll and Set mutate
	// concurrently. The lock is released before the (slow) file write.
	notifyMu.RLock()
	data, err := yaml.Marshal(cfg)
	notifyMu.RUnlock()
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

// imageFilterMu serializes image-filter section reads/writes; the core Config
// itself carries no mutex, so the builtin guards its own section.
var imageFilterMu sync.Mutex

// GetImageFilter returns a copy of the image-filter section.
func (c *Config) GetImageFilter() ImageFilterConfig {
	imageFilterMu.Lock()
	defer imageFilterMu.Unlock()
	sec := c.ImageFilter
	if sec.Guilds != nil {
		guilds := make(map[string]ImageFilterGuild, len(sec.Guilds))
		for id, g := range sec.Guilds {
			guilds[id] = g
		}
		sec.Guilds = guilds
	}
	return sec
}

// SetImageFilter replaces the image-filter section and saves config.yml. If
// the save fails, the previous section is restored: a config the disk doesn't
// have must never drive detection (the image-filter setters rely on this).
func (c *Config) SetImageFilter(sec ImageFilterConfig) error {
	imageFilterMu.Lock()
	defer imageFilterMu.Unlock()
	prev := c.ImageFilter
	c.ImageFilter = sec
	if err := Save(c, filepath.Dir(c.FilePath)); err != nil {
		c.ImageFilter = prev
		return err
	}
	return nil
}

func (c *Config) Set(key, value string) error {
	// Per-server update notify channel: "updater_notify_channel:<guild_id>"
	// sets one server's channel (an empty value clears it). Handled before the
	// switch because the key carries a guild ID suffix.
	if guildID, ok := strings.CutPrefix(key, "updater_notify_channel:"); ok {
		guildID = strings.TrimSpace(guildID)
		if guildID == "" {
			return fmt.Errorf("invalid updater_notify_channel: missing guild id")
		}
		if _, err := strconv.ParseUint(guildID, 10, 64); err != nil || guildID == "0" {
			return fmt.Errorf("invalid notify channel guild id: %q", guildID)
		}
		v, err := normalizeChannelID(value)
		if err != nil {
			return fmt.Errorf("invalid updater_notify_channel: %v", err)
		}
		c.Updater.SetNotifyChannel(guildID, v)
		return Save(c, filepath.Dir(c.FilePath))
	}
	switch key {
	case "prefix":
		if value == "" {
			return fmt.Errorf("prefix cannot be empty")
		}
		c.Bot.Prefix = value
	case "token":
		// A secret: write it to <dir>/.env (0600), never config.yml.
		v := strings.TrimSpace(value)
		if c.FilePath != "" {
			if err := WriteEnvToken(filepath.Dir(c.FilePath), v); err != nil {
				return fmt.Errorf("writing the bot token to .env: %w", err)
			}
		}
		c.Bot.Token = v
	case "owner_id":
		c.Bot.OwnerID = value
	case "tos_url":
		c.Bot.ToS = value
	case "privacy_url":
		c.Bot.Privacy = value
	case "name":
		c.Bot.Name = value
	case "status":
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "online", "idle", "dnd", "invisible":
			c.Bot.Status = strings.ToLower(strings.TrimSpace(value))
		default:
			return fmt.Errorf("invalid status: %q (must be online/idle/dnd/invisible)", value)
		}
	case "intents_message_content":
		v, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("invalid intents_message_content: %v", err)
		}
		c.Intents.MessageContent = v
	case "intents_guild_members":
		v, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("invalid intents_guild_members: %v", err)
		}
		c.Intents.GuildMembers = v
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
	case "enabled_modules":
		// value is a comma/space-separated list of `name` or `name=bool`
		// entries, e.g. "tickets=false" (disable tickets) or
		// "tickets=true" / "tickets" (enable). The [p]modules enable|disable
		// command uses this key. A bare name means enable.
		fields := strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\n'
		})
		if c.Modules.EnabledModules == nil {
			c.Modules.EnabledModules = map[string]bool{}
		}
		for _, f := range fields {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			name, want := f, true
			if i := strings.IndexByte(f, '='); i >= 0 {
				name, want = strings.TrimSpace(f[:i]), strings.TrimSpace(f[i+1:]) == "true"
			}
			if name == "" {
				continue
			}
			if want {
				// Enable = remove from the disabled map (missing key = enabled).
				delete(c.Modules.EnabledModules, name)
			} else {
				c.Modules.EnabledModules[name] = false
			}
		}
	case "dashboard_listen":
		c.Dashboard.Listen = NormalizeListen(value)
	case "dashboard_public_url":
		v := strings.TrimRight(strings.TrimSpace(value), "/")
		if v != "" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			return fmt.Errorf("dashboard_public_url must start with http:// or https://")
		}
		c.Dashboard.PublicURL = v
	case "oauth_client_secret":
		// A secret: write it to <dir>/.env (0600), never config.yml.
		v := strings.TrimSpace(value)
		if c.FilePath != "" {
			if err := WriteEnvValue(filepath.Dir(c.FilePath), EnvOAuthClientSecret, v); err != nil {
				return fmt.Errorf("writing the OAuth client secret to .env: %w", err)
			}
		}
		c.OAuth.ClientSecret = v
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
		// A secret: write it to <dir>/.env (0600), never config.yml.
		v := strings.TrimSpace(value)
		if c.FilePath != "" {
			if err := WriteEnvValue(filepath.Dir(c.FilePath), EnvUpdaterToken, v); err != nil {
				return fmt.Errorf("writing the updater token to .env: %w", err)
			}
		}
		c.Updater.Token = v
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
	case "mcp_enabled":
		v, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("invalid mcp_enabled: %v", err)
		}
		c.MCP.Enabled = v
	case "mcp_token":
		c.MCP.Token = strings.TrimSpace(value)
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
