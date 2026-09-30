package tickets

import (
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

// TestMCPPrefixDispatch proves every tickets subcommand dispatches
// correctly when invoked with flat prefix-style args (the vector MCP's
// run_command sends), landing in the intended branch rather than the
// "⚠️ Usage" fallback. Mirrors TestSlashParityDispatch but for the
// raw-args path — MCP sends ExecKindPrefix with positional args, exactly
// what a prefix-typing user would produce.
//
// Soundness note: the "❌ Error" cases ARE branch-pinned. The handler
// switch enters a specific case whose body returns embed.Error("❌ Error")
// (deterministic). A misspelled or unrecognized subcommand would hit the
// runTicketsCommand fallback or the sub-handler's terminal fallback, both
// of which return embed.Warning("⚠️ Usage") — caught by the panic-if-usage
// check before the title assertion.
func TestMCPPrefixDispatch(t *testing.T) {
	cmd := (&TicketsModule{guilds: map[string]*Config{}}).prefixCommands()[0]

	type tc struct {
		args  []string
		title string
	}
	cases := []tc{
		{[]string{"setup"}, "🎫 Tickets setup"},
		{[]string{"reload"}, "✅ Reloaded"},
		{[]string{"logchannel"}, "Log channel"},
		{[]string{"panel", "list"}, "Panels"},
		{[]string{"panel", "create", "mypanel", "support"}, "❌ Error"},                       // unknown type "support"
		{[]string{"panel", "create", "mypanel", "111222333444555666", "support"}, "❌ Error"}, // long form
		{[]string{"panel", "set", "nope", "title", "hello"}, "❌ Error"},                      // unknown panel
		{[]string{"panel", "move", "nope"}, "❌ Error"},                                       // unknown panel
		{[]string{"panel", "resend", "nope"}, "❌ Error"},                                     // unknown panel
		{[]string{"panel", "suspend", "nope"}, "❌ Error"},                                    // unknown panel
		{[]string{"panel", "resume", "nope"}, "❌ Error"},                                     // unknown panel
		{[]string{"panel", "remove", "nope"}, "❌ Error"},                                     // unknown panel
		{[]string{"type", "list"}, "Types"},
		{[]string{"type", "add", "bug"}, "✅ Type created"},
		{[]string{"type", "set", "bug", "label", "Bug Report"}, "❌ Error"}, // bug doesn't exist
		{[]string{"type", "enable", "bug"}, "❌ Error"},                     // bug doesn't exist
		{[]string{"type", "disable", "bug"}, "❌ Error"},                    // bug doesn't exist
		{[]string{"type", "remove", "bug"}, "❌ Error"},                     // bug doesn't exist
		{[]string{"type", "show", "bug"}, "❌ Error"},                       // bug doesn't exist
		{[]string{"access", "list"}, "Access"},
		{[]string{"access", "add", "bug", "111222333444555666"}, "❌ Error"},    // unknown type "bug"
		{[]string{"access", "remove", "bug", "111222333444555666"}, "❌ Error"}, // unknown type "bug"
	}

	for _, tc := range cases {
		name := cmd.Name
		for _, a := range tc.args {
			name += " " + a
		}
		t.Run(name, func(t *testing.T) {
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

			var capturedTitle, capturedDesc string
			ctx := &commands.Context{
				Bot:       nil,
				ChannelID: "222",
				GuildID:   "111",
				Args:      tc.args,
				IsSlash:   false,
				Web:       false,
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
			if capturedTitle == "" && capturedDesc == "" {
				t.Fatalf("branch %q produced no response (args=%v)", name, tc.args)
			}
			if capturedTitle == "⚠️ Usage" {
				t.Fatalf("dispatch hit the usage fallback for %q (args=%v)", name, tc.args)
			}
			if capturedTitle != tc.title {
				t.Fatalf("title = %q, want %q (desc=%q)", capturedTitle, tc.title, capturedDesc)
			}
		})
	}
}
