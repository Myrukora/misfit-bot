package commands

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/disgoorg/disgo/discord"
)

// ticketsCmd builds a SlashCommand whose Options mirror the real /tickets
// tree: three flat subcommands (setup, reload, logchannel) plus three groups
// (type, panel, access) with overlapping leaf names (add/remove/list/set).
func ticketsCmd() SlashCommand {
	strOpt := func(name string) discord.ApplicationCommandOption {
		return discord.ApplicationCommandOptionString{Name: name}
	}
	chOpt := func(name string) discord.ApplicationCommandOption {
		return discord.ApplicationCommandOptionChannel{Name: name}
	}
	sub := func(name string, opts ...discord.ApplicationCommandOption) discord.ApplicationCommandOption {
		return discord.ApplicationCommandOptionSubCommand{
			Name:    name,
			Options: opts,
		}
	}
	group := func(name string, subs ...discord.ApplicationCommandOptionSubCommand) discord.ApplicationCommandOption {
		return discord.ApplicationCommandOptionSubCommandGroup{
			Name:    name,
			Options: subs,
		}
	}
	return SlashCommand{
		Name: "tickets",
		Options: []discord.ApplicationCommandOption{
			sub("setup"),
			sub("reload"),
			sub("logchannel", chOpt("channel")),
			group("type",
				discord.ApplicationCommandOptionSubCommand{Name: "add", Options: []discord.ApplicationCommandOption{strOpt("key")}},
				discord.ApplicationCommandOptionSubCommand{Name: "set", Options: []discord.ApplicationCommandOption{strOpt("key"), strOpt("field"), strOpt("value")}},
				discord.ApplicationCommandOptionSubCommand{Name: "enable", Options: []discord.ApplicationCommandOption{strOpt("key")}},
				discord.ApplicationCommandOptionSubCommand{Name: "disable", Options: []discord.ApplicationCommandOption{strOpt("key")}},
				discord.ApplicationCommandOptionSubCommand{Name: "remove", Options: []discord.ApplicationCommandOption{strOpt("key")}},
				discord.ApplicationCommandOptionSubCommand{Name: "show", Options: []discord.ApplicationCommandOption{strOpt("key")}},
				discord.ApplicationCommandOptionSubCommand{Name: "list"},
			),
			group("panel",
				discord.ApplicationCommandOptionSubCommand{Name: "create", Options: []discord.ApplicationCommandOption{strOpt("name"), chOpt("channel"), strOpt("type")}},
				discord.ApplicationCommandOptionSubCommand{Name: "set", Options: []discord.ApplicationCommandOption{strOpt("name"), strOpt("field"), strOpt("value")}},
				discord.ApplicationCommandOptionSubCommand{Name: "move", Options: []discord.ApplicationCommandOption{strOpt("name")}},
				discord.ApplicationCommandOptionSubCommand{Name: "resend"},
				discord.ApplicationCommandOptionSubCommand{Name: "suspend", Options: []discord.ApplicationCommandOption{strOpt("name")}},
				discord.ApplicationCommandOptionSubCommand{Name: "resume", Options: []discord.ApplicationCommandOption{strOpt("name")}},
				discord.ApplicationCommandOptionSubCommand{Name: "remove", Options: []discord.ApplicationCommandOption{strOpt("name")}},
				discord.ApplicationCommandOptionSubCommand{Name: "list"},
			),
			group("access",
				discord.ApplicationCommandOptionSubCommand{Name: "add", Options: []discord.ApplicationCommandOption{strOpt("name")}},
				discord.ApplicationCommandOptionSubCommand{Name: "remove", Options: []discord.ApplicationCommandOption{strOpt("name")}},
				discord.ApplicationCommandOptionSubCommand{Name: "list"},
			),
		},
	}
}

// decodeData JSON-decodes a SlashCommandInteractionData fixture. The
// unexported fields (id, name, guildID) survive because json.Unmarshal fills
// the exported fields and the UnmarshalJSON method sets the rest.
func decodeData(t *testing.T, raw string) discord.SlashCommandInteractionData {
	t.Helper()
	var d discord.SlashCommandInteractionData
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	return d
}

func TestSlashArgs(t *testing.T) {
	cmd := ticketsCmd()
	cases := []struct {
		name string
		json string
		want []string
	}{
		{
			name: "setup",
			json: `{"name":"tickets","options":[{"name":"setup","type":1}]}`,
			want: []string{"setup"},
		},
		{
			name: "logchannel with channel",
			json: `{"name":"tickets","options":[{"name":"logchannel","type":1,"options":[{"name":"channel","type":7,"value":"123456789"}]}]}`,
			want: []string{"logchannel", "123456789"},
		},
		{
			name: "type add",
			json: `{"name":"tickets","options":[{"name":"type","type":2,"options":[{"name":"add","type":1,"options":[{"name":"key","type":3,"value":"contact"}]}]}]}`,
			want: []string{"type", "add", "contact"},
		},
		{
			name: "type set",
			json: `{"name":"tickets","options":[{"name":"type","type":2,"options":[{"name":"set","type":1,"options":[{"name":"key","type":3,"value":"k"},{"name":"field","type":3,"value":"label"},{"name":"value","type":3,"value":"Staff"}]}]}]}`,
			want: []string{"type", "set", "k", "label", "Staff"},
		},
		{
			name: "type list (proves panel/access don't leak)",
			json: `{"name":"tickets","options":[{"name":"type","type":2,"options":[{"name":"list","type":1}]}]}`,
			want: []string{"type", "list"},
		},
		{
			name: "panel list",
			json: `{"name":"tickets","options":[{"name":"panel","type":2,"options":[{"name":"list","type":1}]}]}`,
			want: []string{"panel", "list"},
		},
		{
			name: "access list",
			json: `{"name":"tickets","options":[{"name":"access","type":2,"options":[{"name":"list","type":1}]}]}`,
			want: []string{"access", "list"},
		},
		{
			name: "panel create with channel",
			json: `{"name":"tickets","options":[{"name":"panel","type":2,"options":[{"name":"create","type":1,"options":[{"name":"name","type":3,"value":"p"},{"name":"channel","type":7,"value":"999"},{"name":"type","type":3,"value":"t"}]}]}]}`,
			want: []string{"panel", "create", "p", "999", "t"},
		},
		{
			name: "panel create channel omitted (optional shifts nothing)",
			json: `{"name":"tickets","options":[{"name":"panel","type":2,"options":[{"name":"create","type":1,"options":[{"name":"name","type":3,"value":"p"},{"name":"type","type":3,"value":"t"}]}]}]}`,
			want: []string{"panel", "create", "p", "t"},
		},
		{
			name: "panel move",
			json: `{"name":"tickets","options":[{"name":"panel","type":2,"options":[{"name":"move","type":1,"options":[{"name":"name","type":3,"value":"p"}]}]}]}`,
			want: []string{"panel", "move", "p"},
		},
		{
			// Reverse declaration order in the JSON options: the result must
			// follow the DECLARED option order (name, channel, type), not the
			// JSON/map order (type, channel, name).
			name: "panel create reverse JSON order",
			json: `{"name":"tickets","options":[{"name":"panel","type":2,"options":[{"name":"create","type":1,"options":[{"name":"type","type":3,"value":"t"},{"name":"channel","type":7,"value":"999"},{"name":"name","type":3,"value":"p"}]}]}]}`,
			want: []string{"panel", "create", "p", "999", "t"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := decodeData(t, tc.json)
			got := SlashArgs(cmd, data)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SlashArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSlashArgsTopLevelLeaf verifies a top-level command with no subcommand
// and a channel option produces the channel ID with no panic.
func TestSlashArgsTopLevelLeaf(t *testing.T) {
	cmd := SlashCommand{
		Name: "test",
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionChannel{Name: "channel"},
		},
	}
	data := decodeData(t, `{"name":"test","options":[{"name":"channel","type":7,"value":"42"}]}`)
	got := SlashArgs(cmd, data)
	want := []string{"42"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SlashArgs = %v, want %v", got, want)
	}
}
