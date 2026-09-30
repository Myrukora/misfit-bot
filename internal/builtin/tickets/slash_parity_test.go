package tickets

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

// leafPlaceholder returns the string value a leaf option gets in the parity
// fixture, by declared type.
func leafPlaceholder(opt discord.ApplicationCommandOption) (string, bool) {
	switch opt.(type) {
	case discord.ApplicationCommandOptionString:
		return "v", true
	case discord.ApplicationCommandOptionChannel:
		return "111222333444555666", true
	case discord.ApplicationCommandOptionInt:
		return "42", true
	case discord.ApplicationCommandOptionBool:
		return "true", true
	case discord.ApplicationCommandOptionFloat:
		return "1.5", true
	default:
		return "", false
	}
}

// leafRequired reports whether a leaf option is marked Required.
func leafRequired(opt discord.ApplicationCommandOption) bool {
	switch o := opt.(type) {
	case discord.ApplicationCommandOptionString:
		return o.Required
	case discord.ApplicationCommandOptionChannel:
		return o.Required
	case discord.ApplicationCommandOptionInt:
		return o.Required
	case discord.ApplicationCommandOptionBool:
		return o.Required
	case discord.ApplicationCommandOptionFloat:
		return o.Required
	default:
		return false
	}
}

// leafJSON encodes one leaf option as a JSON fragment for the fixture.
func leafJSON(opt discord.ApplicationCommandOption) (string, bool) {
	name := opt.OptionName()
	switch opt.(type) {
	case discord.ApplicationCommandOptionString:
		return fmt.Sprintf(`{"name":%q,"type":3,"value":"v"}`, name), true
	case discord.ApplicationCommandOptionChannel:
		return fmt.Sprintf(`{"name":%q,"type":7,"value":"111222333444555666"}`, name), true
	case discord.ApplicationCommandOptionInt:
		return fmt.Sprintf(`{"name":%q,"type":4,"value":42}`, name), true
	case discord.ApplicationCommandOptionBool:
		return fmt.Sprintf(`{"name":%q,"type":5,"value":true}`, name), true
	case discord.ApplicationCommandOptionFloat:
		return fmt.Sprintf(`{"name":%q,"type":10,"value":1.5}`, name), true
	default:
		return "", false
	}
}

// buildFixture builds a JSON fixture for a SlashCommandInteractionData from a
// subcommand's declared options. When includeOptional is false, only Required
// options are filled; when true, all options are filled.
func buildFixture(subName, groupName string, opts []discord.ApplicationCommandOption, includeOptional bool) string {
	var leafJSONs []string
	var values []string
	for _, opt := range opts {
		ph, isLeaf := leafPlaceholder(opt)
		if !isLeaf {
			continue
		}
		if !leafRequired(opt) && !includeOptional {
			continue
		}
		lj, _ := leafJSON(opt)
		leafJSONs = append(leafJSONs, lj)
		values = append(values, ph)
	}
	subJSON := fmt.Sprintf(`{"name":%q,"type":1`, subName)
	if len(leafJSONs) > 0 {
		subJSON += `,"options":[` + strings.Join(leafJSONs, ",") + `]`
	}
	subJSON += `}`
	if groupName != "" {
		return fmt.Sprintf(`{"name":"tickets","options":[{"name":%q,"type":2,"options":[%s]}]}`, groupName, subJSON)
	}
	return fmt.Sprintf(`{"name":"tickets","options":[%s]}`, subJSON)
}

// expectedVector builds the arg vector the prefix tree must receive for a
// subcommand: [sub, …values] for flat, [group, sub, …values] for grouped.
func expectedVector(groupName, subName string, opts []discord.ApplicationCommandOption, includeOptional bool) []string {
	var vec []string
	if groupName != "" {
		vec = append(vec, groupName)
	}
	vec = append(vec, subName)
	for _, opt := range opts {
		ph, isLeaf := leafPlaceholder(opt)
		if !isLeaf {
			continue
		}
		if !leafRequired(opt) && !includeOptional {
			continue
		}
		vec = append(vec, ph)
	}
	return vec
}

// TestSlashParity walks the REAL /tickets tree (m.slashCommands()[0]) and
// asserts that commands.SlashArgs produces the exact vector the prefix handler
// expects, for every flat subcommand and every subcommand in every group.
// Both required-only and required+optional-present variants are checked.
func TestSlashParity(t *testing.T) {
	m := &TicketsModule{guilds: map[string]*Config{}}
	cmd := m.slashCommands()[0]

	type caseDef struct {
		groupName string
		subName   string
		opts      []discord.ApplicationCommandOption
	}
	var cases []caseDef

	for _, opt := range cmd.Options {
		switch o := opt.(type) {
		case discord.ApplicationCommandOptionSubCommand:
			cases = append(cases, caseDef{subName: o.Name, opts: o.Options})
		case discord.ApplicationCommandOptionSubCommandGroup:
			for _, sub := range o.Options {
				cases = append(cases, caseDef{groupName: o.Name, subName: sub.Name, opts: sub.Options})
			}
		}
	}

	if len(cases) == 0 {
		t.Fatal("no subcommands found in the /tickets tree")
	}

	for _, tc := range cases {
		for _, includeOptional := range []bool{false, true} {
			label := tc.subName
			if tc.groupName != "" {
				label = tc.groupName + " " + tc.subName
			}
			if includeOptional {
				label += " (with optional)"
			}
			t.Run(label, func(t *testing.T) {
				fixture := buildFixture(tc.subName, tc.groupName, tc.opts, includeOptional)
				var data discord.SlashCommandInteractionData
				if err := json.Unmarshal([]byte(fixture), &data); err != nil {
					t.Fatalf("json.Unmarshal: %v\nfixture: %s", err, fixture)
				}
				got := commands.SlashArgs(cmd, data)
				want := expectedVector(tc.groupName, tc.subName, tc.opts, includeOptional)
				if len(got) != len(want) {
					t.Fatalf("SlashArgs = %v (len %d), want %v (len %d)", got, len(got), want, len(want))
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("SlashArgs[%d] = %q, want %q\nfull: %v vs %v", i, got[i], want[i], got, want)
					}
				}
			})
		}
	}
}

// TestSlashParityDispatch proves that runTicketsCommand lands in the intended
// branch (not the ⚠️ Usage fallback) for every subcommand in the real tree.
// The module is loaded over a temp DataDir; Respond/ReplyText capture the
// embed title+description. The placeholder values ("v" for keys/names,
// "111222333444555666" for channels) match no existing config entry, so every
// branch produces a deterministic response — the title is pinned for each.
func TestSlashParityDispatch(t *testing.T) {
	cmd := (&TicketsModule{guilds: map[string]*Config{}}).slashCommands()[0]

	// expectedTitle maps each subcommand label to the embed title the branch
	// produces in this harness (empty config, placeholder values).
	expectedTitle := map[string]string{
		"setup":         "🎫 Tickets setup",
		"reload":        "✅ Reloaded",
		"logchannel":    "Log channel", // channel is optional; required-only variant omits it → info embed
		"type add":      "✅ Type created",
		"type set":      "❌ Error",
		"type enable":   "❌ Error",
		"type disable":  "❌ Error",
		"type remove":   "❌ Error",
		"type show":     "❌ Error",
		"type list":     "Types",
		"panel create":  "❌ Error", // unknown type "v" — postOrUpdatePanel fails before REST
		"panel set":     "❌ Error", // unknown panel "v"
		"panel move":    "❌ Error", // unknown panel "v"
		"panel resend":  "❌ Error", // unknown panel "v"
		"panel suspend": "❌ Error", // unknown panel "v"
		"panel resume":  "❌ Error", // unknown panel "v"
		"panel remove":  "❌ Error", // unknown panel "v"
		"panel list":    "Panels",
		"access add":    "❌ Error", // unknown type "v"
		"access remove": "❌ Error", // unknown type "v"
		"access list":   "Access",
	}

	type caseDef struct {
		groupName string
		subName   string
		opts      []discord.ApplicationCommandOption
	}
	var cases []caseDef
	for _, opt := range cmd.Options {
		switch o := opt.(type) {
		case discord.ApplicationCommandOptionSubCommand:
			cases = append(cases, caseDef{subName: o.Name, opts: o.Options})
		case discord.ApplicationCommandOptionSubCommandGroup:
			for _, sub := range o.Options {
				cases = append(cases, caseDef{groupName: o.Name, subName: sub.Name, opts: sub.Options})
			}
		}
	}

	for _, tc := range cases {
		label := tc.subName
		if tc.groupName != "" {
			label = tc.groupName + " " + tc.subName
		}
		t.Run(label, func(t *testing.T) {
			// Fresh module per subtest — no state leakage between branches.
			dataDir := t.TempDir()
			st, err := openStore(dataDir)
			if err != nil {
				t.Fatalf("openStore: %v", err)
			}
			m := &TicketsModule{
				ctx:    &modules.Context{DataDir: dataDir, Logger: testLogger{}},
				store:  st,
				module: &ModuleConfig{Version: configVersion},
				guilds: map[string]*Config{
					"111": {Version: configVersion, Types: map[string]*TypeConfig{}, Panels: map[string]PanelConfig{}},
				},
				loaded: true,
			}

			// Build the parity args (required-only variant).
			fixture := buildFixture(tc.subName, tc.groupName, tc.opts, false)
			var data discord.SlashCommandInteractionData
			if err := json.Unmarshal([]byte(fixture), &data); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			args := commands.SlashArgs(cmd, data)

			// Capture the response.
			var capturedTitle, capturedDesc string
			ctx := &commands.Context{
				Bot:       nil,
				ChannelID: "111",
				GuildID:   "111",
				Args:      args,
				IsSlash:   true,
				Respond: func(embeds ...discord.Embed) error {
					if len(embeds) > 0 {
						capturedTitle = embeds[0].Title
						capturedDesc = embeds[0].Description
					}
					return nil
				},
				ReplyText: func(text string) error {
					capturedDesc = text
					return nil
				},
			}

			if err := m.runTicketsCommand(ctx); err != nil {
				t.Logf("runTicketsCommand returned error (acceptable): %v", err)
			}

			// The branch must have responded — a silent no-op is a bug.
			if capturedTitle == "" && capturedDesc == "" {
				t.Fatalf("branch %q produced no response (args=%v)", label, args)
			}
			// Must not be the usage fallback.
			if capturedTitle == "⚠️ Usage" {
				t.Fatalf("dispatch hit the usage fallback for %q (args=%v)", label, args)
			}
			// Pin the expected title.
			want := expectedTitle[label]
			if want == "" {
				t.Fatalf("no expected title registered for %q — add it to expectedTitle", label)
			}
			if capturedTitle != want {
				t.Fatalf("branch %q: title = %q, want %q (desc=%q)", label, capturedTitle, want, capturedDesc)
			}
		})
	}
}

// TestSlashParityNoUsageFallback verifies that the usage fallback is only
// reached when Args is empty or unrecognized.
func TestSlashParityNoUsageFallback(t *testing.T) {
	m := &TicketsModule{guilds: map[string]*Config{}}

	// Empty args → usage info (not the warning fallback).
	var capturedTitle string
	ctx := &commands.Context{
		Args: []string{},
		Respond: func(embeds ...discord.Embed) error {
			if len(embeds) > 0 {
				capturedTitle = embeds[0].Title
			}
			return nil
		},
	}
	if err := m.runTicketsCommand(ctx); err != nil {
		t.Fatalf("runTicketsCommand: %v", err)
	}
	if capturedTitle != "🎫 Tickets" {
		t.Fatalf("empty args: title = %q, want 🎫 Tickets", capturedTitle)
	}

	// Unrecognized subcommand → usage warning fallback.
	capturedTitle = ""
	ctx = &commands.Context{
		Args: []string{"bogus"},
		Respond: func(embeds ...discord.Embed) error {
			if len(embeds) > 0 {
				capturedTitle = embeds[0].Title
			}
			return nil
		},
	}
	if err := m.runTicketsCommand(ctx); err != nil {
		t.Fatalf("runTicketsCommand: %v", err)
	}
	if capturedTitle != "⚠️ Usage" {
		t.Fatalf("bogus subcommand: title = %q, want ⚠️ Usage", capturedTitle)
	}
}
