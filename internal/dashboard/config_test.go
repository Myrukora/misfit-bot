package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
)

func TestExecModeSetValidation(t *testing.T) {
	c := defaultConfig()

	valid := []string{"prefix", "slash"}
	for _, v := range valid {
		if err := c.Set("exec_mode", v); err != nil {
			t.Errorf("Set(exec_mode, %q) unexpected error: %v", v, err)
		}
		if c.ExecMode != v {
			t.Errorf("Set(exec_mode, %q): got %q, want %q", v, c.ExecMode, v)
		}
	}

	for _, v := range []string{"", "Prefix", "slash ", "hybrid", "PREFIX"} {
		if err := c.Set("exec_mode", v); err == nil {
			t.Errorf("Set(exec_mode, %q) expected error, got nil", v)
		}
	}
}

func TestDefaultExecMode(t *testing.T) {
	c := defaultConfig()
	if c.ExecMode != "prefix" {
		t.Errorf("defaultConfig().ExecMode = %q, want %q", c.ExecMode, "prefix")
	}
}

func TestLoadConfigNormalizesInvalidExecMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(cfgPath(dir), []byte("exec_mode: hybrid\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if c.ExecMode != "prefix" {
		t.Errorf("loadConfig normalized ExecMode = %q, want %q", c.ExecMode, "prefix")
	}

	if err := os.WriteFile(cfgPath(dir), []byte("exec_mode: slash\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err = loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if c.ExecMode != "slash" {
		t.Errorf("loadConfig kept ExecMode = %q, want %q", c.ExecMode, "slash")
	}
}

func TestOptionSchemaTypes(t *testing.T) {
	cases := []struct {
		name       string
		opt        discord.ApplicationCommandOption
		wantType   string
		wantReq    bool
		wantChoice []string
	}{
		{"string choices", discord.ApplicationCommandOptionString{
			Name: "action", Description: "d", Required: true,
			Choices: []discord.ApplicationCommandOptionChoiceString{{Name: "a", Value: "a"}, {Name: "b", Value: "b"}},
		}, "string", true, []string{"a", "b"}},
		{"plain string", discord.ApplicationCommandOptionString{Name: "text", Description: "d", Required: false}, "string", false, nil},
		{"int", discord.ApplicationCommandOptionInt{Name: "count", Description: "d", Required: true}, "int", true, nil},
		{"bool", discord.ApplicationCommandOptionBool{Name: "flag", Description: "d", Required: false}, "bool", false, nil},
		{"channel", discord.ApplicationCommandOptionChannel{Name: "ch", Description: "d", Required: true}, "channel", true, nil},
		{"role", discord.ApplicationCommandOptionRole{Name: "r", Description: "d", Required: false}, "role", false, nil},
		{"user", discord.ApplicationCommandOptionUser{Name: "u", Description: "d", Required: true}, "user", true, nil},
		{"subcommand", discord.ApplicationCommandOptionSubCommand{
			Name: "ban", Description: "d",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionUser{Name: "user", Description: "d"},
				discord.ApplicationCommandOptionString{Name: "reason", Description: "d"},
			},
		}, "subcommand", true, []string{"ban"}},
	}
	for _, c := range cases {
		got := optionSchema(c.opt)
		if got.Name == "" {
			t.Errorf("%s: empty name", c.name)
		}
		if got.Type != c.wantType {
			t.Errorf("%s: type = %q, want %q", c.name, got.Type, c.wantType)
		}
		if got.Required != c.wantReq {
			t.Errorf("%s: required = %v, want %v", c.name, got.Required, c.wantReq)
		}
		if len(got.Choices) != len(c.wantChoice) {
			t.Errorf("%s: choices = %v, want %v", c.name, got.Choices, c.wantChoice)
		} else {
			for i := range c.wantChoice {
				if got.Choices[i] != c.wantChoice[i] {
					t.Errorf("%s: choice[%d] = %q, want %q", c.name, i, got.Choices[i], c.wantChoice[i])
				}
			}
		}
		if c.name == "subcommand" {
			if len(got.Sub) != 1 || got.Sub[0].Name != "ban" {
				t.Fatalf("subcommand: Sub = %+v, want [{ban ...}]", got.Sub)
			}
			if len(got.Sub[0].Args) != 2 || got.Sub[0].Args[0].Type != "user" || got.Sub[0].Args[1].Type != "string" {
				t.Errorf("subcommand: nested args = %+v, want [user, string]", got.Sub[0].Args)
			}
		}
	}
}

// TestSiblingSubcommandsMerge verifies that ban+kick-style sibling subcommand
// options produce ONE selector (choices [ban kick]) with per-subcommand nested
// args, so the dispatcher's ctx.Args[0] branch always receives the chosen name.
func TestSiblingSubcommandsMerge(t *testing.T) {
	opts := []discord.ApplicationCommandOption{
		discord.ApplicationCommandOptionSubCommand{
			Name: "ban", Description: "Ban a member",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionUser{Name: "user", Description: "d"},
			},
		},
		discord.ApplicationCommandOptionSubCommand{
			Name: "kick", Description: "Kick a member",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionUser{Name: "user", Description: "d"},
				discord.ApplicationCommandOptionString{Name: "reason", Description: "d"},
			},
		},
		discord.ApplicationCommandOptionString{Name: "quiet", Description: "d", Required: false},
	}
	schema := optionSchemas(opts)
	if len(schema) != 2 {
		t.Fatalf("len = %d, want 2 (one merged subcommand selector + quiet)", len(schema))
	}
	sub := schema[0]
	if sub.Type != "subcommand" || len(sub.Choices) != 2 || sub.Choices[0] != "ban" || sub.Choices[1] != "kick" {
		t.Fatalf("selector = %+v, want choices [ban kick]", sub)
	}
	if len(sub.Sub) != 2 || sub.Sub[0].Name != "ban" || sub.Sub[1].Name != "kick" {
		t.Fatalf("Sub = %+v, want [ban kick] nested groups", sub.Sub)
	}
	if len(sub.Sub[0].Args) != 1 || len(sub.Sub[1].Args) != 2 {
		t.Errorf("nested arg counts = %d/%d, want 1/2", len(sub.Sub[0].Args), len(sub.Sub[1].Args))
	}
}

// TestSubcommandWebArgs simulates the browser form for a nested-subcommand
// command: the dispatched args must start with the subcommand NAME, followed
// by the selected nested arguments (mirroring the slash dispatcher).
func TestSubcommandWebArgs(t *testing.T) {
	opts := []discord.ApplicationCommandOption{
		discord.ApplicationCommandOptionSubCommand{
			Name: "ban", Description: "Ban a member",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionUser{Name: "user", Description: "d"},
				discord.ApplicationCommandOptionString{Name: "reason", Description: "d"},
			},
		},
	}
	schema := optionSchemas(opts)
	if len(schema) != 1 || schema[0].Type != "subcommand" || schema[0].Choices[0] != "ban" {
		t.Fatalf("schema = %+v, want subcommand selector with choice 'ban'", schema)
	}
	// Browser collection: subcommand select value first, then the active
	// group's non-empty inputs (app.js behavior).
	selected := map[string]string{"subcommand": "ban", "user": "123", "reason": ""}
	var args []string
	for _, item := range schema {
		if v, ok := selected[item.Name]; ok && v != "" {
			args = append(args, v)
		}
		for _, sub := range item.Sub {
			if sub.Name != selected[item.Name] {
				continue
			}
			for _, nested := range sub.Args {
				if v := selected[nested.Name]; v != "" {
					args = append(args, v)
				}
			}
		}
	}
	want := []string{"ban", "123"}
	if len(args) != len(want) || args[0] != want[0] || args[1] != want[1] {
		t.Fatalf("args = %v, want %v (subcommand name first, then nested)", args, want)
	}
}

func TestOptionSchemasSkipsEmpty(t *testing.T) {
	if optionSchemas(nil) != nil {
		t.Error("optionSchemas(nil) should be nil")
	}
	opts := []discord.ApplicationCommandOption{
		discord.ApplicationCommandOptionString{Name: "a", Description: "d"},
		discord.ApplicationCommandOptionSubCommandGroup{Name: "grp", Description: "d"},
	}
	got := optionSchemas(opts)
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("got %+v, want just the 'a' option (subcommand group skipped)", got)
	}
}

func TestParseGuildLabels(t *testing.T) {
	in := []string{
		"Misfit's Tavern (123456789012345678)",
		"bare-id",
		" Weird (name) (999) ",
		"(not a label)",
	}
	got := parseGuildLabels(in)
	want := []string{"123456789012345678", "bare-id", "999", "(not a label)"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestAllowedGuildsLabelRoundTrip covers the web save path for labels whose
// guild NAMES contain commas: the newline-separated wire format must keep each
// label intact and extract exactly the trailing ID.
func TestAllowedGuildsLabelRoundTrip(t *testing.T) {
	wire := "Dev, Ops (123456789012345678)\nMisfit's Tavern (876543210987654321)"
	var labels []string
	for _, part := range strings.Split(wire, "\n") {
		if part = strings.TrimSpace(part); part != "" {
			labels = append(labels, part)
		}
	}
	ids := parseGuildLabels(labels)
	want := []string{"123456789012345678", "876543210987654321"}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Fatalf("ids = %v, want %v (comma inside guild name must not split the label)", ids, want)
	}
	// The generic Set path then splits the comma-joined IDs — IDs never
	// contain commas, so the stored allowlist round-trips exactly.
	joined := strings.Join(ids, ",")
	if got := strings.FieldsFunc(joined, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '	' }); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Set-split = %v, want %v", got, want)
	}
}

// TestMigrateLegacyConfig pins the one-time migration from the pre-restructure
// module_configs/ layout into the module's own folder: it copies only when the
// new file is missing, never overwrites, and preserves 0600.
func TestMigrateLegacyConfig(t *testing.T) {
	botDir := t.TempDir()
	legacy := filepath.Join(botDir, "module_configs", "dashboard")
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	legacyFile := filepath.Join(legacy, "config.yml")
	if err := os.WriteFile(legacyFile, []byte("session_secret: legacy-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// 1. Fresh data dir + legacy present → migrates, 0600 preserved.
	dataDir := filepath.Join(t.TempDir(), "modules", "Go", "dashboard")
	migrated, err := migrateLegacyConfig(dataDir, botDir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !migrated {
		t.Fatal("want migration to happen")
	}
	data, err := os.ReadFile(cfgPath(dataDir))
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	if string(data) != "session_secret: legacy-secret\n" {
		t.Fatalf("migrated content = %q", data)
	}
	if fi, _ := os.Stat(cfgPath(dataDir)); fi.Mode().Perm() != 0600 {
		t.Fatalf("migrated file mode = %v, want 0600", fi.Mode().Perm())
	}

	// 2. New file already exists → no migration, no overwrite.
	existing := filepath.Join(t.TempDir(), "modules", "Go", "dashboard")
	os.MkdirAll(existing, 0755)
	os.WriteFile(cfgPath(existing), []byte("listen: 127.0.0.1:9090\n"), 0600)
	migrated, err = migrateLegacyConfig(existing, botDir)
	if err != nil || migrated {
		t.Fatalf("want no migration (new file exists), got migrated=%v err=%v", migrated, err)
	}
	data, _ = os.ReadFile(cfgPath(existing))
	if string(data) != "listen: 127.0.0.1:9090\n" {
		t.Fatalf("existing config was overwritten: %q", data)
	}

	// 3. No legacy file anywhere → no migration, no error.
	clean := t.TempDir()
	migrated, err = migrateLegacyConfig(filepath.Join(clean, "data"), clean)
	if err != nil || migrated {
		t.Fatalf("want no migration (no legacy), got migrated=%v err=%v", migrated, err)
	}
}
