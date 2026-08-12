package main

import (
	"testing"

	"github.com/custombot/bot/modules"
)

func TestFreeArgsNeeded(t *testing.T) {
	cases := []struct {
		usage      string
		hasOptions bool
		want       bool
	}{
		{"ping", false, false},   // zero-arg command: no box
		{"uptime", false, false}, // zero-arg command: no box
		{"backup [create|verify|restore|list] [filename]", false, true}, // untyped args
		{"eval <command>", false, true},                                 // untyped args
		{"help [command]", false, true},                                 // optional arg
		{"ping", true, false},                                           // option schema present: forms win
		{"eval <command>", true, false},                                 // option schema present: forms win
	}
	for _, c := range cases {
		if got := freeArgsNeeded(c.usage, c.hasOptions); got != c.want {
			t.Errorf("freeArgsNeeded(%q, %v) = %v, want %v", c.usage, c.hasOptions, got, c.want)
		}
	}
}

// mockWebConfig is a minimal WebConfigurable for buildModuleView tests: one
// global field and one guild-scoped field.
type mockWebConfig struct{}

func (mockWebConfig) WebConfigSchema() []modules.ConfigField {
	return []modules.ConfigField{
		{Key: "global_key", Label: "Global", Type: modules.FieldTypeText, Scope: "global"},
		{Key: "guild_key", Label: "Guild", Type: modules.FieldTypeText, Scope: "guild", GuildScoped: true},
	}
}
func (mockWebConfig) WebGetConfig(guildID string) (map[string]string, error) {
	return map[string]string{"global_key": "g", "guild_key": "x"}, nil
}
func (mockWebConfig) WebSetConfig(guildID, key, value string) error { return nil }

// TestBuildModuleViewGlobalOwner pins that global fields render for
// owner/elevated WITHOUT a guild selected (the settings page must keep the
// dashboard self-config visible even when the guild selector is on a server).
func TestBuildModuleViewGlobalOwner(t *testing.T) {
	m := &DashboardModule{}
	mv := m.buildModuleView(mockWebConfig{}, "mock", nil, lvlOwner, "")
	if len(mv.Fields) != 1 {
		t.Fatalf("owner + no guild: fields = %d, want 1 (global only)", len(mv.Fields))
	}
	if mv.Fields[0].Key != "global_key" {
		t.Fatalf("field = %q, want global_key", mv.Fields[0].Key)
	}
	if mv.Fields[0].GuildID != "" {
		t.Fatalf("global field GuildID = %q, want empty", mv.Fields[0].GuildID)
	}
}

// TestBuildModuleViewStaffNoGlobal pins that global fields never leak to
// staff/regular viewers (mirrors moduleConfigRead's owner/elevated gate).
func TestBuildModuleViewStaffNoGlobal(t *testing.T) {
	m := &DashboardModule{}
	mv := m.buildModuleView(mockWebConfig{}, "mock", nil, lvlStaff, "")
	if len(mv.Fields) != 0 {
		t.Fatalf("staff + no guild: fields = %d, want 0 (global is owner/elevated only)", len(mv.Fields))
	}
}

// TestBuildModuleViewMergesGuildScoped pins that a guild-scoped field renders
// with the selected guild as its per-field context when a server is chosen.
func TestBuildModuleViewMergesGuildScoped(t *testing.T) {
	m := &DashboardModule{}
	mv := m.buildModuleView(mockWebConfig{}, "mock", nil, lvlOwner, "123456789")
	got := map[string]fieldRender{}
	for _, f := range mv.Fields {
		got[f.Key] = f
	}
	if _, ok := got["global_key"]; !ok {
		t.Error("global field missing from merged view (must stay visible with a guild selected)")
	}
	gf, ok := got["guild_key"]
	if !ok {
		t.Fatal("guild-scoped field missing from merged view")
	}
	if gf.GuildID != "123456789" {
		t.Fatalf("guild-scoped field GuildID = %q, want the selected guild", gf.GuildID)
	}
}
