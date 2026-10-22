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
	FilePath  string              `yaml:"-"`
	Bot       BotConfig           `yaml:"bot"`
	Modules   ModulesConfig       `yaml:"modules"`
	Logging   LoggingConfig       `yaml:"logging"`
	OAuth     OAuthConfig         `yaml:"oauth"`
	Dashboard DashboardCoreConfig `yaml:"dashboard"`
	Updater   UpdaterConfig       `yaml:"updater"`
	MCP       MCPConfig           `yaml:"mcp"`
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
	Enabled       bool   `yaml:"enabled"`        // master switch; false = updater does nothing
	Repo          string `yaml:"repo"`           // owner/name of the bot's GitHub repository; empty = feature off
	Branch        string `yaml:"branch"`         // branch to track; default "main"
	Token         string `yaml:"token"`          // GitHub PAT (or gh token); empty = anonymous/credential-helper fallback
	CheckInterval int    `yaml:"check_interval"` // seconds between polls; default 300, minimum 30
	AutoPull      bool   `yaml:"auto_pull"`      // automatically pull + rebuild + restart on new commits
	// NotifyChannels is the per-server map of update-notification channels:
	// guild ID → channel ID. Each configured server gets its own PR/commit
	// embeds posted to its own channel, so multiple servers can watch the repo
	// in their own dedicated channels. Empty = no notifications.
	NotifyChannels map[string]string `yaml:"notify_channels,omitempty"`
	// NotifyChannel is the legacy single global channel. It is kept so existing
	// installs keep notifying until the per-server map is populated (the bot
	// migrates it into NotifyChannels on startup when it can resolve the
	// channel's guild). New configuration uses NotifyChannels only.
	NotifyChannel string `yaml:"notify_channel,omitempty"`
}

// NotifyTargets returns the distinct channel IDs that receive update
// notifications: every per-server channel in NotifyChannels plus the legacy
// global NotifyChannel (for backward compatibility with pre-migration
// installs). Sorted for deterministic delivery order.
//
// notifyMu guards NotifyChannels + NotifyChannel against concurrent access:
// the updater's poll loop reads them (NotifyTargets) while owner actions —
// dashboard, MCP and the one-time startup migration — write them through
// Set. Package-level rather than a Config field because Config is
// value-copied (Load, &Config{} literals in tests), which would make an
// embedded mutex a copylocks hazard; the live bot config is a single
// long-lived *Config.
var notifyMu sync.RWMutex

func (u *UpdaterConfig) NotifyTargets() []string {
	notifyMu.RLock()
	seen := make(map[string]bool, len(u.NotifyChannels)+1)
	var out []string
	for _, ch := range u.NotifyChannels {
		if ch == "" || seen[ch] {
			continue
		}
		seen[ch] = true
		out = append(out, ch)
	}
	if u.NotifyChannel != "" && !seen[u.NotifyChannel] {
		out = append(out, u.NotifyChannel)
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

// SetLegacyNotifyChannel updates the legacy global channel under the same
// guard, keeping the scalar write race-free next to the map.
func (u *UpdaterConfig) SetLegacyNotifyChannel(value string) {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	u.NotifyChannel = value
}

// LegacyNotifyChannel returns the legacy global channel ("" = unset).
func (u *UpdaterConfig) LegacyNotifyChannel() string {
	notifyMu.RLock()
	defer notifyMu.RUnlock()
	return u.NotifyChannel
}

// NotifyChannelFor returns the update-notification channel configured for one
// server ("" = this server gets no update notifications).
func (u *UpdaterConfig) NotifyChannelFor(guildID string) string {
	notifyMu.RLock()
	defer notifyMu.RUnlock()
	return u.NotifyChannels[guildID]
}

// MigrateLegacyNotifyChannel folds the pre-redesign single global notify
// channel into the per-server map once: resolveGuild reports which guild owns
// the channel (the caller supplies the Discord lookup — config has no gateway
// access), then the value moves under that guild and the legacy field is
// cleared. It returns migrated=true only when the config actually changed.
//
// Best effort by design: when the guild cannot be resolved the legacy value is
// left untouched, where NotifyTargets still uses it as a fallback, and the
// caller retries on the next startup. A nil resolveGuild never migrates.
func MigrateLegacyNotifyChannel(c *Config, dir string, resolveGuild func(channelID string) (string, bool)) (bool, error) {
	if c == nil {
		return false, nil
	}
	legacy := c.Updater.LegacyNotifyChannel()
	if legacy == "" || resolveGuild == nil {
		return false, nil
	}
	guildID, ok := resolveGuild(legacy)
	if !ok || guildID == "" {
		return false, nil
	}
	c.Updater.SetNotifyChannel(guildID, legacy)
	c.Updater.SetLegacyNotifyChannel("")
	if err := Save(c, dir); err != nil {
		return false, fmt.Errorf("saving migrated notify channel: %w", err)
	}
	return true, nil
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

	// The bot token is a secret (see secrets.go): it comes from <dir>/.env,
	// never from config.yml (BotConfig.Token is yaml:"-").
	if tok := ReadEnvToken(dir); tok != "" {
		cfg.Bot.Token = tok
	}

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

func (c *Config) Set(key, value string) error {
	// Per-server update notify channel: "updater_notify_channel:<guild_id>"
	// sets one server's channel (an empty value clears it). Handled before the
	// switch because the key carries a guild ID suffix; the bare key below
	// still writes the legacy global channel.
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
		// Legacy global channel: kept as a NotifyTargets fallback and as the
		// [p]update set notify_channel escape hatch. Per-server channels use
		// the composite key handled above.
		v, err := normalizeChannelID(value)
		if err != nil {
			return fmt.Errorf("invalid updater_notify_channel: %v", err)
		}
		c.Updater.SetLegacyNotifyChannel(v)
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
