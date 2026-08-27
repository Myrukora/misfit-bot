package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/disgoorg/disgo/discord"
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

func boolPtr(b bool) *bool          { return &b }
func permPtr(p int64) *int64        { return &p }
func strSlice(s ...string) []string { return s }

// TestOverridesBlankFileDefault pins the no-config default: with no file on
// disk every command is allowed everywhere for everyone.
func TestOverridesBlankFileDefault(t *testing.T) {
	ov := newTestOverrides(t)

	if !ov.Allowed("ping", "", "", 0, nil, false) {
		t.Fatal("blank store must allow everything")
	}
	if !ov.Allowed("cleanup", "guild1", "chan1", 0, nil, false) {
		t.Fatal("blank store must allow guild-scoped commands too")
	}
	if p := ov.EffectiveRequiredPerm("ping", 0); p != 0 {
		t.Fatalf("no override: effective perm should stay 0, got %d", p)
	}
}

// TestOverridesLoadSaveRoundTrip pins persistence: every field the dashboard
// can set must survive a Save → reload cycle.
func TestOverridesLoadSaveRoundTrip(t *testing.T) {
	ov := newTestOverrides(t)

	if err := ov.SetGlobal("backup", GlobalCmdCfg{
		Disabled:     boolPtr(true),
		RequiredPerm: permPtr(int64(discord.PermissionAdministrator)),
	}); err != nil {
		t.Fatalf("SetGlobal: %v", err)
	}
	if err := ov.SetGuild("g1", "cleanup", GuildCmdCfg{
		Disabled:        boolPtr(false),
		ModOnly:         boolPtr(true),
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
	g := ov2.All()["backup"]
	if g == nil || g.Disabled == nil || !*g.Disabled {
		t.Fatal("global backup disabled flag lost in round trip")
	}
	if g.RequiredPerm == nil || *g.RequiredPerm != int64(discord.PermissionAdministrator) {
		t.Fatal("global backup required_perm override lost in round trip")
	}
	gl := ov2.All()["cleanup"]
	if gl == nil {
		t.Fatal("guild cleanup cfg lost in round trip")
	}
	if len(gl.AllowedChannels) != 2 || gl.AllowedChannels[0] != "c1" || gl.AllowedChannels[1] != "c2" {
		t.Fatalf("allowed_channels lost in round trip: %v", gl.AllowedChannels)
	}
	if len(gl.AllowedRoles) != 1 || gl.AllowedRoles[0] != "r1" {
		t.Fatalf("allowed_roles lost in round trip: %v", gl.AllowedRoles)
	}
	if gl.ModOnly == nil || !*gl.ModOnly {
		t.Fatal("mod_only lost in round trip")
	}
}

// TestAllowedGlobalDisable pins design decision #2: a global disable is a
// hard off everywhere — no guild can re-enable it locally.
func TestAllowedGlobalDisable(t *testing.T) {
	ov := newTestOverrides(t)
	if err := ov.SetGlobal("backup", GlobalCmdCfg{Disabled: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}

	if ov.Allowed("backup", "", "", 0, nil, false) {
		t.Fatal("globally disabled command must be denied")
	}
	if ov.Allowed("backup", "g1", "c1", 0, nil, false) {
		t.Fatal("globally disabled command must be denied in guilds too")
	}
	// A local "enable" attempt must NOT re-enable a globally disabled command.
	if err := ov.SetGuild("g1", "backup", GuildCmdCfg{Disabled: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	if ov.Allowed("backup", "g1", "c1", 0, nil, false) {
		t.Fatal("local enable must not override a global disable")
	}
}

// TestAllowedGuildDisable pins the local toggle: it can disable a command
// for one guild only, leaving other guilds and the global default untouched.
func TestAllowedGuildDisable(t *testing.T) {
	ov := newTestOverrides(t)
	if err := ov.SetGuild("g1", "cleanup", GuildCmdCfg{Disabled: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}

	if ov.Allowed("cleanup", "g1", "c1", 0, nil, false) {
		t.Fatal("guild-disabled command must be denied in that guild")
	}
	if !ov.Allowed("cleanup", "g2", "c1", 0, nil, false) {
		t.Fatal("guild disable must not leak to other guilds")
	}
	if !ov.Allowed("cleanup", "", "", 0, nil, false) {
		t.Fatal("guild disable must not affect the global default")
	}
}

// TestAllowedAllowedChannels pins the channel allowlist, including the
// local-narrowing rule: a guild list can only intersect with (never widen)
// the global list.
func TestAllowedAllowedChannels(t *testing.T) {
	ov := newTestOverrides(t)
	if err := ov.SetGlobal("cleanup", GlobalCmdCfg{AllowedChannels: strSlice("c1", "c2")}); err != nil {
		t.Fatal(err)
	}

	if !ov.Allowed("cleanup", "", "c1", 0, nil, false) {
		t.Fatal("channel in global allowlist must be allowed")
	}
	if ov.Allowed("cleanup", "", "c9", 0, nil, false) {
		t.Fatal("channel outside global allowlist must be denied")
	}

	// Local list narrows: only c1 survives the intersection.
	if err := ov.SetGuild("g1", "cleanup", GuildCmdCfg{AllowedChannels: strSlice("c2", "c3")}); err != nil {
		t.Fatal(err)
	}
	if !ov.Allowed("cleanup", "g1", "c2", 0, nil, false) {
		t.Fatal("channel in both global and local allowlists must be allowed")
	}
	if ov.Allowed("cleanup", "g1", "c1", 0, nil, false) {
		t.Fatal("channel only in the global list must be denied when local narrows")
	}
	if ov.Allowed("cleanup", "g1", "c3", 0, nil, false) {
		t.Fatal("channel only in the local list must be denied (cannot widen global)")
	}
}

// TestAllowedLocalChannelsNoGlobalRestriction pins the global-empty semantics:
// with NO global channel allowlist, a guild-local list is the effective set —
// the command must work in listed channels and be denied outside them.
// (Regression: intersecting against an empty global set denied the command in
// EVERY channel, contradicting "empty = no restriction".)
func TestAllowedLocalChannelsNoGlobalRestriction(t *testing.T) {
	ov := newTestOverrides(t)

	// Global: nothing set. Guild sets a local channel allowlist.
	if err := ov.SetGuild("g1", "cleanup", GuildCmdCfg{AllowedChannels: strSlice("c1")}); err != nil {
		t.Fatal(err)
	}
	if !ov.Allowed("cleanup", "g1", "c1", 0, nil, false) {
		t.Fatal("with no global list, a channel in the local allowlist must be allowed")
	}
	if ov.Allowed("cleanup", "g1", "c9", 0, nil, false) {
		t.Fatal("with no global list, a channel outside the local allowlist must be denied")
	}
	// Other guilds without a local list are unaffected.
	if !ov.Allowed("cleanup", "g2", "c9", 0, nil, false) {
		t.Fatal("a guild without a local allowlist must not be restricted by another guild's list")
	}
}

// TestAllowedAllowedRoles pins the role allowlist: empty = everyone, and a
// member passes if ANY of their roles is in the list.
func TestAllowedAllowedRoles(t *testing.T) {
	ov := newTestOverrides(t)
	if err := ov.SetGuild("g1", "cleanup", GuildCmdCfg{AllowedRoles: strSlice("r1", "r2")}); err != nil {
		t.Fatal(err)
	}

	if !ov.Allowed("cleanup", "g1", "c1", 0, strSlice("r2"), false) {
		t.Fatal("member with an allowed role must pass")
	}
	if ov.Allowed("cleanup", "g1", "c1", 0, strSlice("r9"), false) {
		t.Fatal("member with only disallowed roles must be denied")
	}
	if ov.Allowed("cleanup", "g1", "c1", 0, nil, false) {
		t.Fatal("member with no roles must be denied when a role list is set")
	}
	// A different guild with no role restriction is unaffected.
	if !ov.Allowed("cleanup", "g2", "c1", 0, nil, false) {
		t.Fatal("role restriction must not leak to other guilds")
	}
}

// TestAllowedModOnly pins the mod-only toggle: non-mods denied, mods pass.
// Either scope (global or guild) can set it; guild cannot clear global.
func TestAllowedModOnly(t *testing.T) {
	ov := newTestOverrides(t)
	if err := ov.SetGuild("g1", "cleanup", GuildCmdCfg{ModOnly: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}

	if ov.Allowed("cleanup", "g1", "c1", 0, nil, false) {
		t.Fatal("non-mod must be denied by mod_only")
	}
	if !ov.Allowed("cleanup", "g1", "c1", 0, nil, true) {
		t.Fatal("mod must pass mod_only")
	}
	if !ov.Allowed("cleanup", "g2", "c1", 0, nil, false) {
		t.Fatal("mod_only must not leak to other guilds")
	}

	// Global mod_only applies everywhere and a guild cannot clear it.
	if err := ov.SetGlobal("cleanup", GlobalCmdCfg{ModOnly: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if err := ov.SetGuild("g2", "cleanup", GuildCmdCfg{ModOnly: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	if ov.Allowed("cleanup", "g2", "c1", 0, nil, false) {
		t.Fatal("global mod_only must not be clearable by a guild")
	}
}

// TestAllowedPermOverride pins the owner-only required_perm override: it
// REPLACES the base permission — the member is checked against the override
// value, not the base.
func TestAllowedPermOverride(t *testing.T) {
	const admin = discord.PermissionAdministrator
	const manageMsgs = discord.PermissionManageMessages

	ov := newTestOverrides(t)
	if err := ov.SetGlobal("cleanup", GlobalCmdCfg{RequiredPerm: permPtr(int64(manageMsgs))}); err != nil {
		t.Fatal(err)
	}

	// Member with the override perm passes...
	if !ov.Allowed("cleanup", "", "", manageMsgs, nil, false) {
		t.Fatal("member with the override perm must pass")
	}
	// ...a member with only the (higher) base perm does NOT — the override
	// replaces the base, and manage-messages does not imply admin.
	if ov.Allowed("cleanup", "", "", admin, nil, false) {
		t.Fatal("override perm replaces base: admin without manage-messages must be denied")
	}
	// ...and a member with neither is denied.
	if ov.Allowed("cleanup", "", "", 0, nil, false) {
		t.Fatal("member without the override perm must be denied")
	}

	// EffectiveRequiredPerm exposes the override for the dispatcher; falls
	// back to the base when no override exists.
	if p := ov.EffectiveRequiredPerm("cleanup", admin); p != manageMsgs {
		t.Fatalf("EffectiveRequiredPerm must return the override, got %d", p)
	}
	if p := ov.EffectiveRequiredPerm("ping", admin); p != admin {
		t.Fatalf("EffectiveRequiredPerm must fall back to base, got %d", p)
	}
}

// TestAllowedNoConfigMeansAllowed pins the per-scope default: a scope with
// no entry for a command imposes no restriction at that level.
func TestAllowedNoConfigMeansAllowed(t *testing.T) {
	ov := newTestOverrides(t)
	// Set an unrelated command so the store is non-empty.
	if err := ov.SetGlobal("other", GlobalCmdCfg{Disabled: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}

	if !ov.Allowed("cleanup", "g1", "c1", 0, nil, false) {
		t.Fatal("command with no config at any scope must be allowed")
	}
}
