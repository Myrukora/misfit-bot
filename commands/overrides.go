package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// CommandOverrides persists per-command enable/disable and restriction rules
// in a single JSON file next to config.yml (command_overrides.json, 0600). It
// is the single source of truth for what the dashboard's Commands tab edits:
// the core dispatcher enforces it centrally, so plugin modules (which can't
// import the core commands package) need no changes.
//
// Overrides are per-guild: each server's command configuration is independent,
// so a command disabled (or restricted) in one guild stays available in every
// other guild. There is no bot-wide scope — "disable everywhere" means
// disabling in each guild.
//
// A per-guild entry can disable the command, restrict it to manage-messages
// users, and allowlist channels/roles.

type CommandOverrides struct {
	mu     sync.RWMutex
	path   string
	data   overridesData
	loaded bool
}

type overridesData struct {
	Version int                            `json:"version"`
	Guilds  map[string]map[string]CmdCfg `json:"guilds"`
}

// CmdCfg is a per-guild override for a single command name.
type CmdCfg struct {
	// Disabled turns the command off in this guild. nil = not overridden.
	Disabled *bool `json:"disabled,omitempty"`
	// ModOnly restricts the command to manage-messages users in this guild.
	// nil = not overridden.
	ModOnly *bool `json:"mod_only,omitempty"`
	// AllowedChannels narrows the command to these channel IDs. Empty = all.
	AllowedChannels []string `json:"allowed_channels,omitempty"`
	// AllowedRoles narrows the command to members holding any of these role
	// IDs. Empty = all.
	AllowedRoles []string `json:"allowed_roles,omitempty"`
}

// LoadCommandOverrides reads the overrides file from path. A missing file is
// not an error — it means "everything allowed" (the default). A corrupt file
// is an error so the owner can fix it rather than silently locking out.
// Legacy files with a "global" section load fine: the section is ignored,
// since overrides are per-guild now.
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
			o.data = overridesData{Version: 1, Guilds: map[string]map[string]CmdCfg{}}
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
	if d.Guilds == nil {
		d.Guilds = map[string]map[string]CmdCfg{}
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

// Allowed reports whether the named command may run in the given context. It
// is the single enforcement point both dispatchers call after CanUse passes.
//
// Rules (per-guild):
//   - A disabled command is refused in that guild.
//   - A mod-only command refuses non-mods in that guild.
//   - An allowlisted channel refuses channels outside the list.
//   - An allowlisted role refuses members holding none of them.
func (o *CommandOverrides) Allowed(cmd, guildID, channelID string, memberRoles []string, isMod bool) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if guildID == "" {
		return true
	}
	c, ok := o.data.Guilds[guildID][cmd]
	if !ok {
		return true
	}
	if c.Disabled != nil && *c.Disabled {
		return false
	}
	if c.ModOnly != nil && *c.ModOnly && !isMod {
		return false
	}
	if len(c.AllowedChannels) > 0 && !containsString(c.AllowedChannels, channelID) {
		return false
	}
	if len(c.AllowedRoles) > 0 && !anyRoleIn(c.AllowedRoles, memberRoles) {
		return false
	}
	return true
}

// IsDisabled reports whether the command is disabled in the given guild. Used
// to filter disabled commands out of the [p]help listing. A nil or unreadable
// store returns false (everything shown).
func (o *CommandOverrides) IsDisabled(cmd, guildID string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if guildID == "" {
		return false
	}
	c, ok := o.data.Guilds[guildID][cmd]
	return ok && c.Disabled != nil && *c.Disabled
}

// SetGuild sets (or clears when cfg is the zero value) a per-guild override.
// It returns an error only if the backing file can't be loaded.
func (o *CommandOverrides) SetGuild(guildID, name string, cfg CmdCfg) error {
	if err := o.load(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.data.Guilds[guildID] == nil {
		o.data.Guilds[guildID] = map[string]CmdCfg{}
	}
	if isZeroCfg(cfg) {
		delete(o.data.Guilds[guildID], name)
	} else {
		o.data.Guilds[guildID][name] = cfg
	}
	return nil
}

// EffectiveFor returns the per-guild override for one command in one guild —
// the per-guild view the dashboard modal needs. A nil pointer means "no
// override at all".
func (o *CommandOverrides) EffectiveFor(guildID, name string) *CmdCfg {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if err := o.load(); err != nil {
		return nil
	}
	c, ok := o.data.Guilds[guildID][name]
	if !ok {
		return nil
	}
	out := c
	return &out
}

// isZeroCfg reports whether cfg carries no restriction at all (no override).
func isZeroCfg(cfg CmdCfg) bool {
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