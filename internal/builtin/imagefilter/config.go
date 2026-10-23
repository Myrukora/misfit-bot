package imagefilter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	corecfg "github.com/misfit/bot/config"
)

// Punishment actions (ported from the Python module's semantics).
const (
	PunishNone = "none"
	PunishMute = "mute"
	PunishKick = "kick"
	PunishBan  = "ban"
)

// ClipVariants lists the vision-tower variants the export script can produce
// (scripts/export_clip_onnx.py). The .onnx file must exist under modelsDir
// for a variant to be usable; DefaultClipVariant is what new installs use.
var ClipVariants = []string{"b32", "b16", "l14", "l14-336"}

const DefaultClipVariant = "b32"

// GuildConfig is the per-server configuration. Everything here is edited from
// the dashboard only — the module registers no Discord commands.
type GuildConfig struct {
	Enabled      bool    `json:"enabled"`
	Threshold    float64 `json:"threshold"`
	Punishment   string  `json:"punishment"`
	MuteDuration int     `json:"mute_duration"` // seconds, punishment == "mute"
	LogChannel   string  `json:"log_channel"`   // "" = no log embeds
	DeleteOnNone bool    `json:"delete_on_none"`
}

// defaultGuildConfig mirrors the Python module's DEFAULT_CONFIG.
func defaultGuildConfig() GuildConfig {
	return GuildConfig{
		Enabled:      false,
		Threshold:    0.95,
		Punishment:   PunishNone,
		MuteDuration: 600,
		LogChannel:   "",
		DeleteOnNone: false,
	}
}

// Validate normalises and checks user-supplied values; it returns a descriptive
// error for anything the dashboard shouldn't have let through.
func (c *GuildConfig) Validate() error {
	if c.Threshold <= 0 || c.Threshold > 1 {
		return fmt.Errorf("threshold must be in (0, 1], got %v", c.Threshold)
	}
	switch c.Punishment {
	case PunishNone, PunishMute, PunishKick, PunishBan:
	default:
		return fmt.Errorf("punishment must be one of none|mute|kick|ban, got %q", c.Punishment)
	}
	// A mute needs a usable duration; there is no reason to require 10s
	// specifically (the timeout API accepts any positive duration).
	if c.Punishment == PunishMute && c.MuteDuration <= 0 {
		return fmt.Errorf("mute_duration must be a positive number of seconds, got %d", c.MuteDuration)
	}
	if c.MuteDuration < 0 {
		return fmt.Errorf("mute_duration must be >= 0, got %d", c.MuteDuration)
	}
	return nil
}

// fileConfig is the on-disk shape of <dataDir>/config.json.
type fileConfig struct {
	ClipVariant string                 `json:"clip_variant"`
	Guilds      map[string]GuildConfig `json:"guilds"`
}

// config is the in-memory store. Two backing modes:
//   - core != nil: persisted in config.yml's `imagefilter:` section (the
//     production path; the module dir only holds images/models).
//   - path != "": the legacy module-local config.json (kept for tests and as
//     the migration source).
type config struct {
	mu   sync.RWMutex
	path string
	core *corecfg.Config
	file fileConfig
}

func configPath(dataDir string) string {
	return filepath.Join(dataDir, "config.json")
}

// loadConfig reads config.json, filling defaults for anything missing
// (first run, new keys after upgrades, corrupt file). Never fails hard on a
// corrupt file: it starts fresh so one bad write can't brick the module.
func loadConfig(dataDir string) (*config, error) {
	c := &config{
		path: configPath(dataDir),
		file: fileConfig{ClipVariant: DefaultClipVariant, Guilds: map[string]GuildConfig{}},
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, c.saveLocked()
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &c.file); err != nil {
		// Corrupt config: start fresh (a backup of the bad file is kept).
		_ = os.Rename(c.path, c.path+".bad")
		return c, c.saveLocked()
	}
	if c.file.Guilds == nil {
		c.file.Guilds = map[string]GuildConfig{}
	}
	if !validVariant(c.file.ClipVariant) {
		c.file.ClipVariant = DefaultClipVariant
	}
	return c, c.saveLocked()
}

func validVariant(v string) bool {
	for _, want := range ClipVariants {
		if v == want {
			return true
		}
	}
	return false
}

// saveLocked persists atomically. Callers must hold mu or be in a path that
// has exclusive access (load). In core mode the section goes to config.yml;
// in legacy mode to the module-local config.json.
func (c *config) saveLocked() error {
	if c.core != nil {
		sec := corecfg.ImageFilterConfig{Variant: c.file.ClipVariant}
		if len(c.file.Guilds) > 0 {
			sec.Guilds = make(map[string]corecfg.ImageFilterGuild, len(c.file.Guilds))
			for id, g := range c.file.Guilds {
				sec.Guilds[id] = corecfg.ImageFilterGuild{
					Enabled: g.Enabled, Threshold: g.Threshold,
					Punishment: g.Punishment, MuteDuration: g.MuteDuration,
					LogChannel: g.LogChannel, DeleteOnNone: g.DeleteOnNone,
				}
			}
		}
		return c.core.SetImageFilter(sec)
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(&c.file, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// loadCoreConfig builds the config store backed by the core config.yml. A
// module-local config.json from an older install is imported once and renamed
// so the module directory no longer carries settings.
func loadCoreConfig(core *corecfg.Config, dataDir string) (*config, error) {
	sec := core.GetImageFilter()
	c := &config{
		core: core,
		file: fileConfig{ClipVariant: sec.Variant, Guilds: map[string]GuildConfig{}},
	}
	needSave := false
	if !validVariant(c.file.ClipVariant) {
		// A hand-edited/unknown variant is normalized AND persisted, so the
		// file cannot disagree with the running model on every restart.
		c.file.ClipVariant = DefaultClipVariant
		needSave = true
	}
	for id, g := range sec.Guilds {
		c.file.Guilds[id] = GuildConfig{
			Enabled: g.Enabled, Threshold: g.Threshold,
			Punishment: g.Punishment, MuteDuration: g.MuteDuration,
			LogChannel: g.LogChannel, DeleteOnNone: g.DeleteOnNone,
		}
	}

	// One-time migration: an install that used the legacy JSON keeps its
	// settings. Only when the core section carries no guilds yet.
	if len(sec.Guilds) == 0 {
		if legacy, ok := readLegacyConfig(dataDir); ok {
			c.file = legacy
			needSave = true
			_ = os.Rename(configPath(dataDir), configPath(dataDir)+".migrated")
		}
	}
	if needSave {
		if err := c.saveLocked(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// readLegacyConfig reads <dataDir>/config.json without creating or rewriting
// anything (migration source only).
func readLegacyConfig(dataDir string) (fileConfig, bool) {
	data, err := os.ReadFile(configPath(dataDir))
	if err != nil {
		return fileConfig{}, false
	}
	var file fileConfig
	if err := json.Unmarshal(data, &file); err != nil {
		return fileConfig{}, false
	}
	if file.Guilds == nil {
		file.Guilds = map[string]GuildConfig{}
	}
	if !validVariant(file.ClipVariant) {
		file.ClipVariant = DefaultClipVariant
	}
	return file, true
}

// ClipVariant returns the bot-wide CLIP variant (owner-level setting).
func (c *config) ClipVariant() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.file.ClipVariant
}

// SetClipVariant switches the bot-wide model variant. Changing it invalidates
// every cached embedding (different model = different vector space), which the
// caller (manager) handles via the returned flag.
func (c *config) SetClipVariant(v string) (invalidated bool, err error) {
	if !validVariant(v) {
		return false, fmt.Errorf("unknown CLIP variant %q (available: %s)", v, strings.Join(ClipVariants, ", "))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file.ClipVariant == v {
		return false, nil
	}
	prev := c.file.ClipVariant
	c.file.ClipVariant = v
	if err := c.saveLocked(); err != nil {
		c.file.ClipVariant = prev
		return false, err
	}
	return true, nil
}

// GuildSettings returns the effective per-guild config with defaults merged
// (a copy — callers never hold the lock).
func (c *config) GuildSettings(guildID string) GuildConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := defaultGuildConfig()
	if g, ok := c.file.Guilds[guildID]; ok {
		if g.Threshold > 0 && g.Threshold <= 1 {
			out.Threshold = g.Threshold
		}
		if validPunishment(g.Punishment) {
			out.Punishment = g.Punishment
		}
		// 0 means "unset" (merge the default); any positive stored value wins.
		if g.MuteDuration > 0 {
			out.MuteDuration = g.MuteDuration
		}
		out.LogChannel = g.LogChannel
		out.DeleteOnNone = g.DeleteOnNone
		out.Enabled = g.Enabled
	}
	return out
}

func validPunishment(p string) bool {
	switch p {
	case PunishNone, PunishMute, PunishKick, PunishBan:
		return true
	}
	return false
}

// EnabledGuilds lists guild IDs with enabled=true (drives the model refcount).
func (c *config) EnabledGuilds() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []string
	for gid, g := range c.file.Guilds {
		if g.Enabled {
			out = append(out, gid)
		}
	}
	return out
}

// SetGuildConfig validates and stores the full per-guild config. If the
// persist fails, the in-memory entry is restored: a config the disk doesn't
// have must not silently drive detection.
func (c *config) SetGuildConfig(guildID string, gc GuildConfig) error {
	if err := gc.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	prev, had := c.file.Guilds[guildID]
	c.file.Guilds[guildID] = gc
	if err := c.saveLocked(); err != nil {
		if had {
			c.file.Guilds[guildID] = prev
		} else {
			delete(c.file.Guilds, guildID)
		}
		return err
	}
	return nil
}

// SetGuildEnabled flips just the enable flag; returns whether the effective
// enabled-set changed (used for the warm/cold refcount).
func (c *config) SetGuildEnabled(guildID string, enabled bool) (changed bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev, had := c.file.Guilds[guildID]
	if prev.Enabled == enabled {
		return false, nil
	}
	next := prev
	next.Enabled = enabled
	c.file.Guilds[guildID] = next
	if err := c.saveLocked(); err != nil {
		if had {
			c.file.Guilds[guildID] = prev
		} else {
			delete(c.file.Guilds, guildID)
		}
		return false, err
	}
	return true, nil
}
