package main

import (
	"os"
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
			Name: "sub", Description: "d",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{Name: "x", Description: "d"},
			},
		}, "string", true, []string{"x"}},
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
