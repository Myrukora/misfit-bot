package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/disgoorg/disgo/discord"
)

// CommandOverrides persists per-command enable/disable and restriction rules in
// a single JSON file next to config.yml (command_overrides.json, 0600). It is
// the single source of truth for what the dashboard's Configuration/Commands
// tabs edit: the core dispatcher enforces it centrally, so plugin modules
// (which can't import the core commands package) need no changes.
//
// Two scopes are supported:
//   - Global: bot-owner-only. A globally disabled command is off everywhere
//     (Carl semantics: "disabled really are disabled, even for mods").
//   - Per-guild "local": guild managers (staff tier) can narrow a command for
//     their guild, but only ever restrict — a local toggle can't re-enable
//     something global disabled, and allowed-channels/roles can only shrink.
//
// The owner-only `RequiredPerm` override replaces the base command's
// RequiredPerm globally (a powerful knob the UI warns about).
type CommandOverrides struct {
	mu     sync.RWMutex
	path   string
	data   overridesData
	loaded bool
}

type overridesData struct {
	Version int                               `json:"version"`
	Global  map[string]GlobalCmdCfg           `json:"global"`
	Guilds  map[string]map[string]GuildCmdCfg `json:"guilds"`
}

// GlobalCmdCfg is a bot-owner-only override for a single command name. It
// carries the full field set so the dashboard can render and persist both
// scopes uniformly through All(); the dispatcher only enforces the fields each
// scope is responsible for (guild values may additionally carry AllowedRoles).
type GlobalCmdCfg struct {
	// Disabled turns the command off everywhere. nil = not overridden.
	Disabled *bool `json:"disabled,omitempty"`
	// ModOnly restricts the command to manage-messages users everywhere.
	// nil = not overridden.
	ModOnly *bool `json:"mod_only,omitempty"`
	// AllowedChannels narrows the command to these channel IDs. Empty = all.
	AllowedChannels []string `json:"allowed_channels,omitempty"`
	// AllowedRoles narrows the command to members holding any of these role IDs.
	// Empty = all. Only meaningful at the guild scope.
	AllowedRoles []string `json:"allowed_roles,omitempty"`
	// RequiredPerm replaces the base command's RequiredPerm globally. nil =
	// the base command's permission is used.
	RequiredPerm *int64 `json:"required_perm,omitempty"`
}

// GuildCmdCfg is a per-guild (staff-narrowable) override for a single command.
// It is identical in shape to GlobalCmdCfg so the dashboard can render and
// persist both scopes uniformly; guild values only ever narrow the effective
// behavior (never widen it).
type GuildCmdCfg = GlobalCmdCfg

// LoadCommandOverrides reads the overrides file from path. A missing file is
// not an error — it means "everything allowed" (the default). A corrupt file is
// an error so the owner can fix it rather than silently locking out.
func LoadCommandOverrides(path string) (*CommandOverrides, error) {
	o := &CommandOverrides{path: path}
	if err := o.load(); err != nil {
		return nil, err
	}
	return o, nil
}

// Path returns the backing file path.
func (o *CommandOverrides) Path() string { return o.path }

func (o *CommandOverrides) load() error {
	if o.loaded {
		return nil
	}
	data, err := os.ReadFile(o.path)
	if err != nil {
		if os.IsNotExist(err) {
			o.data = overridesData{Version: 1, Global: map[string]GlobalCmdCfg{}, Guilds: map[string]map[string]GuildCmdCfg{}}
			o.loaded = true
			return nil
		}
		return err
	}
	d := overridesData{}
	if len(data) == 0 {
		o.data = d
		o.loaded = true
		return nil
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	if d.Global == nil {
		d.Global = map[string]GlobalCmdCfg{}
	}
	if d.Guilds == nil {
		d.Guilds = map[string]map[string]GuildCmdCfg{}
	}
	o.data = d
	o.loaded = true
	return nil
}

// Save persists the current in-memory state atomically (temp file + rename),
// 0600.
func (o *CommandOverrides) Save() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	data, err := json.MarshalIndent(o.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.path), 0755); err != nil {
		return err
	}
	tmp := o.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, o.path)
}

// Allowed reports whether the named command may run in the given context. It is
// the single enforcement point both dispatchers call after CanUse passes.
//
// Rules (global wins, then guild narrows):
//   - A globally disabled command is refused everywhere.
//   - A globally mod-only command refuses non-mods everywhere.
//   - A globally allowlisted channel refuses channels outside the list.
//   - A globally overridden RequiredPerm is enforced against memberPerms.
//   - A locally disabled command is refused in that guild.
//   - A locally mod-only command refuses non-mods in that guild.
//   - A locally allowlisted channel narrows the effective channel set.
//   - A locally allowlisted role refuses members holding none of them.
func (o *CommandOverrides) Allowed(cmd, guildID, channelID string, memberPerms discord.Permissions, memberRoles []string, isMod bool) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()

	g, hasGlobal := o.data.Global[cmd]
	// Global rules: disabled / mod-only / channel / perm override.
	if hasGlobal {
		if g.Disabled != nil && *g.Disabled {
			return false
		}
		if g.ModOnly != nil && *g.ModOnly && !isMod {
			return false
		}
		if len(g.AllowedChannels) > 0 && !containsString(g.AllowedChannels, channelID) {
			return false
		}
		if g.RequiredPerm != nil && !memberPerms.Has(discord.Permissions(*g.RequiredPerm)) {
			return false
		}
	}

	if guildID == "" {
		return true
	}
	gc, hasGuild := o.data.Guilds[guildID][cmd]
	if !hasGuild {
		return true
	}
	if gc.Disabled != nil && *gc.Disabled {
		return false
	}
	if gc.ModOnly != nil && *gc.ModOnly && !isMod {
		return false
	}
	// Channel narrowing: the effective set is (global list ∩ guild list), where
	// an empty list at a scope means "no restriction there". Local can only
	// narrow — it can never widen beyond the global list.
	if len(gc.AllowedChannels) > 0 {
		effective := intersectStrings(g.AllowedChannels, gc.AllowedChannels)
		if !containsString(effective, channelID) {
			return false
		}
	} else if len(g.AllowedChannels) > 0 && !containsString(g.AllowedChannels, channelID) {
		return false
	}
	// Role allowlist: empty = everyone; a member passes if ANY of their roles is
	// in the list.
	if len(gc.AllowedRoles) > 0 && !anyRoleIn(gc.AllowedRoles, memberRoles) {
		return false
	}
	return true
}

// GlobalDisabled reports whether a command is disabled globally (owner scope).
func (o *CommandOverrides) GlobalDisabled(name string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	g, ok := o.data.Global[name]
	return ok && g.Disabled != nil && *g.Disabled
}

// GuildDisabled reports whether a command is disabled by a per-guild override
// (staff scope), independent of any global disable.
func (o *CommandOverrides) GuildDisabled(guildID, name string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if guildID == "" {
		return false
	}
	gc, ok := o.data.Guilds[guildID][name]
	return ok && gc.Disabled != nil && *gc.Disabled
}

// HasGuildOverride reports whether a command has any per-guild override entry.
func (o *CommandOverrides) HasGuildOverride(guildID, name string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if guildID == "" {
		return false
	}
	_, ok := o.data.Guilds[guildID][name]
	return ok
}

// IsDisabled reports whether the command is disabled globally or in the given
// guild. Used to filter disabled commands out of the [p]help listing. A nil or
// unreadable store returns false (everything shown).
func (o *CommandOverrides) IsDisabled(cmd, guildID string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if g, ok := o.data.Global[cmd]; ok && g.Disabled != nil && *g.Disabled {
		return true
	}
	if guildID == "" {
		return false
	}
	if gc, ok := o.data.Guilds[guildID][cmd]; ok && gc.Disabled != nil && *gc.Disabled {
		return true
	}
	return false
}

// EffectiveRequiredPerm returns the effective RequiredPerm for a command: the
// global override when present, otherwise the base command's own permission.
// The dispatcher passes this to CanUse so owner/elevated still bypass via the
// normal path while non-owners are checked against the override value.
func (o *CommandOverrides) EffectiveRequiredPerm(cmd string, base discord.Permissions) discord.Permissions {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if g, ok := o.data.Global[cmd]; ok && g.RequiredPerm != nil {
		return discord.Permissions(*g.RequiredPerm)
	}
	return base
}

// SetGlobal sets (or clears when cfg is the zero value) a bot-owner-only
// override. It returns an error only if the backing file can't be loaded.
func (o *CommandOverrides) SetGlobal(name string, cfg GlobalCmdCfg) error {
	if err := o.load(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if isZeroGlobal(cfg) {
		delete(o.data.Global, name)
	} else {
		o.data.Global[name] = cfg
	}
	return nil
}

// SetGuild sets (or clears when cfg is the zero value) a per-guild override.
// It returns an error only if the backing file can't be loaded.
func (o *CommandOverrides) SetGuild(guildID, name string, cfg GuildCmdCfg) error {
	if err := o.load(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.data.Guilds[guildID] == nil {
		o.data.Guilds[guildID] = map[string]GuildCmdCfg{}
	}
	if isZeroGuild(cfg) {
		delete(o.data.Guilds[guildID], name)
	} else {
		o.data.Guilds[guildID][name] = cfg
	}
	return nil
}

// All returns a flattened view of every override (global + guild) keyed by
// command name. When a command has both a global and a guild override the guild
// values win (they are more specific), so the returned entry is the effective
// config. Each entry is a pointer so a command with no override can be
// represented as nil.
func (o *CommandOverrides) All() map[string]*GlobalCmdCfg {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if err := o.load(); err != nil {
		return nil
	}
	out := make(map[string]*GlobalCmdCfg)
	for name, g := range o.data.Global {
		clone := g
		out[name] = &clone
	}
	for _, cmds := range o.data.Guilds {
		for name, gc := range cmds {
			// Guild values overlay global ones; nil guild field falls back to
			// the global value so the dashboard sees the effective config.
			var merged GlobalCmdCfg
			if g, ok := out[name]; ok && g != nil {
				merged = *g
			}
			if gc.Disabled != nil {
				merged.Disabled = gc.Disabled
			}
			if gc.ModOnly != nil {
				merged.ModOnly = gc.ModOnly
			}
			if len(gc.AllowedChannels) > 0 {
				merged.AllowedChannels = gc.AllowedChannels
			}
			if len(gc.AllowedRoles) > 0 {
				merged.AllowedRoles = gc.AllowedRoles
			}
			out[name] = &merged
		}
	}
	return out
}

func isZeroGlobal(cfg GlobalCmdCfg) bool {
	return cfg.Disabled == nil && cfg.ModOnly == nil &&
		cfg.RequiredPerm == nil && len(cfg.AllowedChannels) == 0
}

func isZeroGuild(cfg GlobalCmdCfg) bool {
	return cfg.Disabled == nil && cfg.ModOnly == nil &&
		len(cfg.AllowedChannels) == 0 && len(cfg.AllowedRoles) == 0
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func anyRoleIn(allowed, memberRoles []string) bool {
	for _, r := range allowed {
		for _, m := range memberRoles {
			if r == m {
				return true
			}
		}
	}
	return false
}

// intersectStrings returns the elements of a that also appear in b.
func intersectStrings(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, x := range b {
		set[x] = true
	}
	out := make([]string, 0)
	for _, x := range a {
		if set[x] {
			out = append(out, x)
		}
	}
	return out
}
