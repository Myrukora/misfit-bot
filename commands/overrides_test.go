package commands

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestOverrides loads an empty store backed by a temp file.
func newTestOverrides(t *testing.T) *CommandOverrides {
	t.Helper()
	path := filepath.Join(t.TempDir(), "command_overrides.json")
	ov, err := LoadCommandOverrides(path)
	if err != nil {
		t.Fatalf("LoadCommandOverrides: %v", err)
	}
	return ov
}

func strSlice(s ...string) []string { return s }

// TestOverridesBlankFileDefault pins the no-config default: with no file on
// disk every command is allowed everywhere for everyone.
func TestOverridesBlankFileDefault(t *testing.T) {
	ov := newTestOverrides(t)

	if !ov.Allowed("ping", "", "", nil, false) {
		t.Fatal("blank store must allow everything")
	}
	if !ov.Allowed("cleanup", "guild1", "chan1", nil, false) {
		t.Fatal("blank store must allow guild-scoped commands too")
	}
}

// TestOverridesLoadSaveRoundTrip pins persistence: every field the dashboard
// can set must survive a Save → reload cycle.
func TestOverridesLoadSaveRoundTrip(t *testing.T) {
	ov := newTestOverrides(t)

	if err := ov.SetGuild("g1", "cleanup", CmdCfg{
		Disabled:        new(false),
		ModOnly:         new(true),
		AllowedChannels: strSlice("c1", "c2"),
		AllowedRoles:    strSlice("r1"),
	}); err != nil {
		t.Fatalf("SetGuild: %v", err)
	}
	if err := ov.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify the file is actually on disk with restrictive perms.
	info, err := os.Stat(ov.Path())
	if err != nil {
		t.Fatalf("stat overrides file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("overrides file mode = %o, want 0600", mode)
	}

	// Reload from the same path and check every field.
	ov2, err := LoadCommandOverrides(ov.Path())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	c := ov2.EffectiveFor("g1", "cleanup")
	if c == nil {
		t.Fatal("guild cleanup cfg lost in round trip")
	}
	if len(c.AllowedChannels) != 2 || c.AllowedChannels[0] != "c1" || c.AllowedChannels[1] != "c2" {
		t.Fatalf("allowed_channels lost in round trip: %v", c.AllowedChannels)
	}
	if len(c.AllowedRoles) != 1 || c.AllowedRoles[0] != "r1" {
		t.Fatalf("allowed_roles lost in round trip: %v", c.AllowedRoles)
	}
	if c.ModOnly == nil || !*c.ModOnly {
		t.Fatal("mod_only lost in round trip")
	}
	if c.Disabled == nil || *c.Disabled {
		t.Fatal("disabled=false lost in round trip")
	}
}

// TestLegacyGlobalSectionIgnored pins the cutover: a legacy file with a
// "global" section loads fine, the section is ignored (overrides are
// per-guild now), and the guild entries survive.
func TestLegacyGlobalSectionIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "command_overrides.json")
	legacy := `{
  "version": 1,
  "global": { "backup": { "disabled": true } },
  "guilds": { "g1": { "cleanup": { "disabled": true } } }
}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	ov, err := LoadCommandOverrides(path)
	if err != nil {
		t.Fatalf("load legacy file: %v", err)
	}
	if ov.Allowed("backup", "g1", "c1", nil, false) != true {
		t.Fatal("legacy global disable must have no effect — overrides are per-guild")
	}
	if !ov.IsDisabled("cleanup", "g1") {
		t.Fatal("legacy guild disable must survive the cutover")
	}
}

// TestAllowedGuildDisable pins the per-guild toggle: it disables a command
// for one guild only, leaving every other guild untouched.
func TestAllowedGuildDisable(t *testing.T) {
	ov := newTestOverrides(t)
	if err := ov.SetGuild("g1", "cleanup", CmdCfg{Disabled: new(true)}); err != nil {
		t.Fatal(err)
	}

	if ov.Allowed("cleanup", "g1", "c1", nil, false) {
		t.Fatal("guild-disabled command must be denied in that guild")
	}
	if !ov.Allowed("cleanup", "g2", "c1", nil, false) {
		t.Fatal("guild disable must not leak to other guilds")
	}
	if !ov.Allowed("cleanup", "", "", nil, false) {
		t.Fatal("guild disable must not affect commands outside any guild")
	}
}

// TestAllowedAllowedChannels pins the per-guild channel allowlist: listed
// channels pass, everything else is denied, and other guilds are unaffected.
func TestAllowedAllowedChannels(t *testing.T) {
	ov := newTestOverrides(t)
	if err := ov.SetGuild("g1", "cleanup", CmdCfg{AllowedChannels: strSlice("c1", "c2")}); err != nil {
		t.Fatal(err)
	}

	if !ov.Allowed("cleanup", "g1", "c1", nil, false) {
		t.Fatal("channel in the allowlist must be allowed")
	}
	if ov.Allowed("cleanup", "g1", "c9", nil, false) {
		t.Fatal("channel outside the allowlist must be denied")
	}
	// A guild without an allowlist is unaffected.
	if !ov.Allowed("cleanup", "g2", "c9", nil, false) {
		t.Fatal("a guild without an allowlist must not be restricted by another guild's list")
	}
}

// TestAllowedAllowedRoles pins the role allowlist: empty = everyone, and a
// member passes if ANY of their roles is in the list.
func TestAllowedAllowedRoles(t *testing.T) {
	ov := newTestOverrides(t)
	if err := ov.SetGuild("g1", "cleanup", CmdCfg{AllowedRoles: strSlice("r1", "r2")}); err != nil {
		t.Fatal(err)
	}

	if !ov.Allowed("cleanup", "g1", "c1", strSlice("r2"), false) {
		t.Fatal("member with an allowed role must pass")
	}
	if ov.Allowed("cleanup", "g1", "c1", strSlice("r9"), false) {
		t.Fatal("member with only disallowed roles must be denied")
	}
	if ov.Allowed("cleanup", "g1", "c1", nil, false) {
		t.Fatal("member with no roles must be denied when a role list is set")
	}
	// A different guild with no role restriction is unaffected.
	if !ov.Allowed("cleanup", "g2", "c1", nil, false) {
		t.Fatal("role restriction must not leak to other guilds")
	}
}

// TestAllowedModOnly pins the per-guild mod-only toggle: non-mods denied,
// mods pass, other guilds unaffected.
func TestAllowedModOnly(t *testing.T) {
	ov := newTestOverrides(t)
	if err := ov.SetGuild("g1", "cleanup", CmdCfg{ModOnly: new(true)}); err != nil {
		t.Fatal(err)
	}

	if ov.Allowed("cleanup", "g1", "c1", nil, false) {
		t.Fatal("non-mod must be denied by mod_only")
	}
	if !ov.Allowed("cleanup", "g1", "c1", nil, true) {
		t.Fatal("mod must pass mod_only")
	}
	if !ov.Allowed("cleanup", "g2", "c1", nil, false) {
		t.Fatal("mod_only must not leak to other guilds")
	}
}

// TestAllowedNoConfigMeansAllowed pins the per-guild default: a guild with no
// entry for a command imposes no restriction there.
func TestAllowedNoConfigMeansAllowed(t *testing.T) {
	ov := newTestOverrides(t)
	// Set an unrelated command so the store is non-empty.
	if err := ov.SetGuild("g1", "other", CmdCfg{Disabled: new(true)}); err != nil {
		t.Fatal(err)
	}

	if !ov.Allowed("cleanup", "g1", "c1", nil, false) {
		t.Fatal("command with no config in the guild must be allowed")
	}
}

// TestSetGuildClear pins the zero-value clear: saving an empty config removes
// the entry, and the command is allowed again.
func TestSetGuildClear(t *testing.T) {
	ov := newTestOverrides(t)
	if err := ov.SetGuild("g1", "cleanup", CmdCfg{Disabled: new(true)}); err != nil {
		t.Fatal(err)
	}
	if !ov.IsDisabled("cleanup", "g1") {
		t.Fatal("cleanup must be disabled before the clear")
	}
	if err := ov.SetGuild("g1", "cleanup", CmdCfg{}); err != nil {
		t.Fatal(err)
	}
	if ov.IsDisabled("cleanup", "g1") {
		t.Fatal("zero-value save must clear the override")
	}
	if ov.EffectiveFor("g1", "cleanup") != nil {
		t.Fatal("cleared override must report no entry")
	}
}