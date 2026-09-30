package tickets

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/misfit/bot/modules"
)

// testLogger is a no-op modules.Logger for tests. When warns is non-nil it
// records every Warn message (mutex-guarded: the close tail may run in a
// goroutine); the zero value stays a pure no-op.
type testLogger struct {
	warns *warnCapture
}

// warnCapture records formatted Warn messages for assertions.
type warnCapture struct {
	mu   sync.Mutex
	msgs []string
}

func (c *warnCapture) record(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, fmt.Sprintf(format, args...))
}

// messages returns a copy of the recorded warnings.
func (c *warnCapture) messages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

func (testLogger) Debug(string, ...any) {}
func (testLogger) Info(string, ...any)  {}
func (l testLogger) Warn(format string, args ...any) {
	if l.warns != nil {
		l.warns.record(format, args...)
	}
}
func (testLogger) Error(string, ...any) {}

// TestModuleConfigRetentionRoundTrip pins the bot-wide retention semantics: an
// explicit 0 (keep forever) survives save→reload, an omitted field falls back
// to the 30-day default, and a null value also falls back to the default.
func TestModuleConfigRetentionRoundTrip(t *testing.T) {
	dir := t.TempDir()

	mod, err := loadModuleConfig(dir)
	if err != nil {
		t.Fatalf("loadModuleConfig fresh: %v", err)
	}
	if mod.RetentionDays() != defaultRetentionDays {
		t.Fatalf("fresh: want default %d, got %d", defaultRetentionDays, mod.RetentionDays())
	}

	mod.Retention = retentionDays{value: 0, set: true}
	if err := mod.save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := loadModuleConfig(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.RetentionDays() != 0 {
		t.Fatalf("explicit zero lost: got %d, want 0", reloaded.RetentionDays())
	}

	if err := os.WriteFile(moduleConfigPath(dir), []byte("version: 3\nstorage_retention_days: null\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nullMod, err := loadModuleConfig(dir)
	if err != nil {
		t.Fatalf("load null: %v", err)
	}
	if nullMod.RetentionDays() != defaultRetentionDays {
		t.Fatalf("null: want default %d, got %d", defaultRetentionDays, nullMod.RetentionDays())
	}
}

// TestGuildConfigRoundTrip covers a per-guild file: types + panels (with
// questions) + log channel round-trip.
func TestGuildConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	guildID := "111222333444555666"
	cfg := loadGuildConfig(dir, guildID, nil)
	cfg.LogChannel = "999888777666555444"
	cfg.Types["staff"] = &TypeConfig{
		Key: "staff", Label: "Staff", Enabled: true,
		Category: "111222333444555666",
	}
	cfg.Panels["staff_panel"] = PanelConfig{
		Name: "staff_panel", ChannelID: "121212121212121212",
		TypeKey: "staff", Title: "Need help?",
		ModalTitle: "Tell us what's up",
		Questions: []QuestionConfig{
			{Label: "What do you need?", Style: "short", Required: true},
			{Label: "Details", Style: "paragraph"},
		},
	}
	if err := cfg.save(dir, guildID); err != nil {
		t.Fatal(err)
	}
	got := loadGuildConfig(dir, guildID, nil)
	if got.LogChannel != "999888777666555444" {
		t.Fatalf("log channel lost: %q", got.LogChannel)
	}
	tc := got.Types["staff"]
	if tc == nil || !tc.Enabled || tc.Category != "111222333444555666" {
		t.Fatalf("type round-trip broken: %+v", tc)
	}
	p := got.Panels["staff_panel"]
	if p.ModalTitle != "Tell us what's up" || len(p.Questions) != 2 {
		t.Fatalf("panel questions lost: %+v", p)
	}
	if p.Questions[0].Label != "What do you need?" || !p.Questions[0].Required || p.Questions[1].Style != "paragraph" {
		t.Fatalf("question fields lost: %+v", p.Questions)
	}
}

// TestMigrateToV3 pins the legacy→per-guild migration: two guild ticket dirs +
// two panels (one per guild) become two guild files with the right panel each;
// the legacy file is renamed; the module config keeps retention + dashboard
// close; a second run is a no-op.
func TestMigrateToV3(t *testing.T) {
	const guildA = "111222333444555666"
	const guildB = "222333444555666777"
	dir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dir, "tickets", guildA), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tickets", guildB), 0755); err != nil {
		t.Fatal(err)
	}

	legacy := `
version: 2
storage_retention_days: 14
allow_dashboard_close: true
types:
  staff:
    key: staff
    label: Staff
    enabled: true
    category: "111222333444555666"
panels:
  panel_a:
    name: panel_a
    channel_id: "100100100100100100"
    type: staff
  panel_b:
    name: panel_b
    channel_id: "200200200200200200"
    type: staff
log_channel: "300300300300300300"
`
	if err := os.WriteFile(moduleConfigPath(dir), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}

	m := &TicketsModule{
		ctx: &modules.Context{Logger: testLogger{}},
		channelGuild: func(channelID string) (string, bool) {
			switch channelID {
			case "100100100100100100", "300300300300300300":
				return guildA, true
			case "200200200200200200":
				return guildB, true
			}
			return "", false
		},
	}
	if err := m.migrateToV3(dir); err != nil {
		t.Fatalf("migrateToV3: %v", err)
	}

	if _, err := os.Stat(moduleConfigPath(dir) + ".v2-migrated"); err != nil {
		t.Fatalf("legacy not renamed: %v", err)
	}

	// A fresh bot-wide module config is written to config.yml after the rename.
	mod, err := loadModuleConfig(dir)
	if err != nil {
		t.Fatalf("loadModuleConfig: %v", err)
	}
	if mod.RetentionDays() != 14 {
		t.Fatalf("retention lost: got %d, want 14", mod.RetentionDays())
	}
	if !mod.AllowDashClose {
		t.Fatal("allow_dashboard_close lost")
	}

	ga := loadGuildConfig(dir, guildA, nil)
	if _, ok := ga.Panels["panel_a"]; !ok {
		t.Fatalf("guild A missing panel_a: %+v", ga.Panels)
	}
	if _, ok := ga.Panels["panel_b"]; ok {
		t.Fatal("guild A should not have panel_b")
	}
	if ga.LogChannel != "300300300300300300" {
		t.Fatalf("guild A log channel lost: %q", ga.LogChannel)
	}
	gb := loadGuildConfig(dir, guildB, nil)
	if _, ok := gb.Panels["panel_b"]; !ok {
		t.Fatalf("guild B missing panel_b: %+v", gb.Panels)
	}
	if _, ok := gb.Panels["panel_a"]; ok {
		t.Fatal("guild B should not have panel_a")
	}
	if gb.LogChannel != "" {
		t.Fatalf("guild B should have no log channel: %q", gb.LogChannel)
	}
	if _, ok := gb.Types["staff"]; !ok {
		t.Fatal("guild B missing staff type")
	}

	if err := m.migrateToV3(dir); err != nil {
		t.Fatalf("second migrateToV3: %v", err)
	}
	if got := loadGuildConfig(dir, guildA, nil); len(got.Panels) != 1 {
		t.Fatalf("second run changed guild A panels: %+v", got.Panels)
	}
}

// TestMigrateV1GroupsToPerGuild pins the v1 groups_yaml → per-guild types
// migration: each group becomes a type in every target guild.
func TestMigrateV1GroupsToPerGuild(t *testing.T) {
	const guildA = "111222333444555666"
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tickets", guildA), 0755); err != nil {
		t.Fatal(err)
	}
	legacy := `
groups_yaml: |
  - key: staff
    label: Staff
    enabled: true
    parent_channel: "111222333444555666"
    ping_roles: ["987654321098765432"]
  - key: apps
    label: Applications
    enabled: false
    parent_channel: "111222333444555666"
`
	if err := os.WriteFile(moduleConfigPath(dir), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	m := &TicketsModule{ctx: &modules.Context{Logger: testLogger{}}}
	if err := m.migrateToV3(dir); err != nil {
		t.Fatalf("migrateToV3: %v", err)
	}
	ga := loadGuildConfig(dir, guildA, nil)
	staff, ok := ga.Types["staff"]
	if !ok || !staff.Enabled || staff.Category != "111222333444555666" || len(staff.PingRoles) != 1 {
		t.Fatalf("staff type not migrated: %+v", staff)
	}
	if _, ok := ga.Types["apps"]; !ok {
		t.Fatal("apps type not migrated")
	}
	if staff.Label != "Staff" {
		t.Fatalf("label not carried: %q", staff.Label)
	}
}
