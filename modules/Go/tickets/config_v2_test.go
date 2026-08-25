package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(configPath(dir), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestConfigV2MigrationFromGroups pins the v1→v2 config migration: legacy
// groups YAML becomes one type per group; panels/log_channel survive.
func TestConfigV2MigrationFromGroups(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, `groups_yaml: |
  - key: staff
    label: Staff
    enabled: true
    parent_channel: "111222333444555666"
    ping_roles: ["987654321098765432"]
  - key: apps
    label: Applications
    enabled: false
    parent_channel: "111222333444555666"
log_channel: "555444333222111000"
`)
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Version != 2 {
		t.Fatalf("version = %d, want 2 after migration", cfg.Version)
	}
	if len(cfg.Types) != 2 {
		t.Fatalf("types = %d, want 2", len(cfg.Types))
	}
	staff, ok := cfg.Types["staff"]
	if !ok || !staff.Enabled || staff.Category != "111222333444555666" || len(staff.PingRoles) != 1 {
		t.Fatalf("staff type not migrated: %+v", staff)
	}
	if staff.Label != "Staff" {
		t.Fatalf("label not carried: %q", staff.Label)
	}
	if cfg.LogChannel != "555444333222111000" {
		t.Fatalf("log_channel lost: %q", cfg.LogChannel)
	}
}

// TestConfigV2FreshRoundTrip covers a native v2 file: types + panels round-trip
// with button label/emoji and panel registry intact.
func TestConfigV2FreshRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Version = 2
	cfg.LogChannel = "999888777666555444"
	cfg.Types["contact"] = &TypeConfig{
		Key: "contact", Label: "Contact Staff", Enabled: true,
		Category:  "111222333444555666",
		PingRoles: []string{"111"}, HelperRoles: []string{"222"},
		WelcomeMsg:  "Welcome {user.mention}!",
		EmbedBody:   "{user} opened a ticket.",
		ButtonLabel: "Open Ticket",
		ButtonEmoji: "🎫",
		Color:       0x5865F2,
	}
	cfg.Panels["contact_staff"] = PanelConfig{
		Name: "contact_staff", ChannelID: "121212121212121212",
		MessageID: "343434343434343434", TypeKey: "contact",
		Title: "Need help?", Description: "Click below.",
	}
	if err := cfg.save(dir); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	tc := got.Types["contact"]
	if tc == nil || tc.ButtonEmoji != "🎫" || tc.ButtonLabel != "Open Ticket" || !tc.AllowClaimOn() {
		t.Fatalf("type round-trip broken: %+v", tc)
	}
	p := got.Panels["contact_staff"]
	if p.Name == "" || p.MessageID != "343434343434343434" || p.TypeKey != "contact" || p.Suspended {
		t.Fatalf("panel round-trip broken: %+v", p)
	}
}

func TestPanelNameValidation(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"contact_staff", true},
		{"a", true},
		{"Contact-Staff-2", true},
		{"", false},
		{"has space", false},
		{"../evil", false},
		{"dot.dot", false},
	}
	for _, c := range cases {
		if got := validPanelName(c.name); got != c.want {
			t.Errorf("validPanelName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTypeValidation(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := loadConfig(dir)
	// enabled without category must be rejected at parse time
	raw := `
version: 2
types:
  bad:
    key: bad
    label: Bad
    enabled: true
`
	writeCfg(t, dir, strings.TrimSpace(raw))
	if _, err := loadConfig(dir); err == nil {
		t.Fatal("enabled-without-category must fail load")
	}
	_ = cfg
}

var _ = filepath.Join // keep import if tests shrink
