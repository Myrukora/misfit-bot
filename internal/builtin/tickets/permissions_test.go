package tickets

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

const gateGuild = "111222333444555666"

// gateBot answers the owner/elevated questions the button gates ask.
type gateBot struct {
	commands.Interface
	owner string
}

func (b gateBot) IsOwner(id string) bool { return id == b.owner }
func (b gateBot) IsElevated(string) bool { return false }

// buttonEvent builds a component interaction carrying the clicking user and
// their guild permissions, matching what Discord sends.
func buttonEvent(t *testing.T, customID, guildID, userID string, perms discord.Permissions) *events.ComponentInteractionCreate {
	t.Helper()
	raw := fmt.Sprintf(`{"type":3,"data":{"custom_id":%q,"component_type":2},"guild_id":%q,"user":{"id":%q},"member":{"permissions":%q}}`,
		customID, guildID, userID, fmt.Sprint(int64(perms)))
	var ci discord.ComponentInteraction
	if err := json.Unmarshal([]byte(raw), &ci); err != nil {
		t.Fatalf("unmarshal component interaction: %v", err)
	}
	return &events.ComponentInteractionCreate{ComponentInteraction: ci}
}

func gateModule(t *testing.T, typ *TypeConfig, roles map[snowflake.ID][]snowflake.ID) *TicketsModule {
	t.Helper()
	return &TicketsModule{
		ctx:    &modules.Context{Logger: testLogger{}, Rest: &mockRest{memberRoles: roles}, Bot: gateBot{owner: "1"}},
		store:  mustOpenStore(t),
		module: &ModuleConfig{Version: configVersion},
		guilds: map[string]*Config{gateGuild: {Version: configVersion, Types: map[string]*TypeConfig{"staff": typ}}},
		loaded: true,
	}
}

func gateTicket() *modules.Ticket {
	return &modules.Ticket{ID: "staff-0001", GuildID: gateGuild, Type: "staff", OpenerID: "111", Status: "open"}
}

func gateResponse(t *testing.T, m *TicketsModule, e *events.ComponentInteractionCreate, ticketID string) string {
	t.Helper()
	h := &wireHarness{}
	e.Respond = h.respond
	m.onCloseButton(e, ticketID)
	if h.callbackCount() != 1 {
		t.Fatalf("callbacks = %d, want 1", h.callbackCount())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	mc, ok := h.callbacks[0].data.(discord.MessageCreate)
	if !ok || len(mc.Embeds) == 0 {
		t.Fatalf("callback carries no message embed: %+v", h.callbacks[0].data)
	}
	return mc.Embeds[0].Description
}

// TestCloseGate pins who may close: helpers always; the opener only while the
// type allows closing. Unrelated members never.
func TestCloseGate(t *testing.T) {
	m := gateModule(t, &TypeConfig{Key: "staff", HelperRoles: []string{"7777"}, AllowClose: boolPtr(true)},
		map[snowflake.ID][]snowflake.ID{333: {7777}})
	tk := gateTicket()
	cases := []struct {
		name  string
		user  string
		perms discord.Permissions
		want  bool
	}{
		{"opener while allowed", "111", 0, true},
		{"helper role", "333", 0, true},
		{"stranger", "222", 0, false},
		{"manage messages", "444", discord.PermissionManageMessages, true},
	}
	for _, tc := range cases {
		e := buttonEvent(t, "tickets:close:"+tk.ID, gateGuild, tc.user, tc.perms)
		if got := m.canCloseTicket(e, tk); got != tc.want {
			t.Errorf("%s: canClose = %v, want %v", tc.name, got, tc.want)
		}
	}

	m.mu.Lock()
	m.guilds[gateGuild].Types["staff"].AllowClose = boolPtr(false)
	m.mu.Unlock()
	if m.canCloseTicket(buttonEvent(t, "tickets:close:"+tk.ID, gateGuild, "111", 0), tk) {
		t.Error("opener can still close with allow_close off")
	}
	if !m.canCloseTicket(buttonEvent(t, "tickets:close:"+tk.ID, gateGuild, "333", 0), tk) {
		t.Error("helper role must still be able to close with allow_close off")
	}
}

// TestRenameTicket pins the in-channel rename: the topic resolution finds the
// ticket, the opener is refused, a helper's name reaches Discord sanitized and
// is persisted, and a missing name yields the usage reply.
func TestRenameTicket(t *testing.T) {
	roles := map[snowflake.ID][]snowflake.ID{333: {7777}}
	m := gateModule(t, &TypeConfig{Key: "staff", HelperRoles: []string{"7777"}, AllowClose: boolPtr(true)}, roles)
	r := m.ctx.Rest.(*mockRest)
	r.channelTopic = "staff-0001"
	tk := gateTicket()
	tk.ChannelID = "555"
	tk.ChannelName = "old-name"
	if err := m.store.save(tk); err != nil {
		t.Fatalf("save: %v", err)
	}

	var last []discord.Embed
	respond := func(embeds ...discord.Embed) error { last = embeds; return nil }
	ctx := &commands.Context{
		Bot: gateBot{owner: "1"}, GuildID: gateGuild, ChannelID: "555",
		Author: discord.User{ID: 111}, Args: []string{"nope"}, Respond: respond,
	}

	// The opener is not a helper: refused, no update call.
	if err := m.renameTicketCommand(ctx); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if len(last) == 0 || !strings.Contains(last[0].Description, "Only helpers") {
		t.Fatalf("response = %+v, want the helper-only error", last)
	}
	if calls := r.callsFiltered("/channels/555"); len(calls) != 1 || calls[0].method != "GET" {
		t.Fatalf("calls after refusal = %+v, want only the topic lookup", calls)
	}

	// A helper's rename is slugified, sent to Discord and persisted.
	ctx.Author = discord.User{ID: 333}
	ctx.Args = []string{"New", "Name"}
	last = nil
	if err := m.renameTicketCommand(ctx); err != nil {
		t.Fatalf("rename: %v", err)
	}
	patched := ""
	for _, c := range r.callsFiltered("/channels/555") {
		if c.method == "PATCH" {
			patched = c.body
		}
	}
	if patched != "new-name" {
		t.Fatalf("channel update body = %q, want new-name", patched)
	}
	got, _ := m.store.load(gateGuild, "staff-0001")
	if got == nil || got.ChannelName != "new-name" {
		t.Fatalf("stored ticket = %+v, want ChannelName new-name", got)
	}
	if len(last) == 0 || !strings.Contains(last[0].Description, "new-name") {
		t.Fatalf("response = %+v, want the renamed confirmation", last)
	}

	// Missing name: usage reply, no further update.
	ctx.Args = nil
	last = nil
	if err := m.renameTicketCommand(ctx); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if len(last) == 0 || (!strings.Contains(last[0].Title, "Usage") && !strings.Contains(last[0].Description, "Usage")) {
		t.Fatalf("response = %+v, want the usage reply", last)
	}
}

// TestTicketCommandSurface pins the user-facing slash actions: close, add,
// remove and rename, with a user option on add/remove and a string option on
// rename.
func TestTicketCommandSurface(t *testing.T) {
	m := &TicketsModule{}
	names := []string{"close", "add", "remove", "rename"}
	slash := map[string]commands.SlashCommand{}
	for _, c := range m.SlashCommands() {
		slash[c.Name] = c
	}
	for _, name := range names {
		c, ok := slash[name]
		if !ok || c.Execute == nil {
			t.Errorf("slash command %q missing", name)
			continue
		}
		switch name {
		case "add", "remove":
			if len(c.Options) != 1 {
				t.Errorf("/%s options = %d, want 1 (member)", name, len(c.Options))
				continue
			}
			if _, ok := c.Options[0].(discord.ApplicationCommandOptionUser); !ok {
				t.Errorf("/%s option type = %T, want user", name, c.Options[0])
			}
		case "rename":
			if len(c.Options) != 1 {
				t.Errorf("/rename options = %d, want 1 (name)", len(c.Options))
				continue
			}
			if _, ok := c.Options[0].(discord.ApplicationCommandOptionString); !ok {
				t.Errorf("/rename option type = %T, want string", c.Options[0])
			}
		default:
			if len(c.Options) != 0 {
				t.Errorf("/%s options = %d, want none", name, len(c.Options))
			}
		}
	}
}

// TestCloseButtonRejectsStranger pins the handler wiring: a member with no
// standing gets an ephemeral error and the ticket stays open.
func TestCloseButtonRejectsStranger(t *testing.T) {
	m := gateModule(t, &TypeConfig{Key: "staff", AllowClose: boolPtr(true)}, nil)
	tk := gateTicket()
	if err := m.store.save(tk); err != nil {
		t.Fatalf("save: %v", err)
	}
	desc := gateResponse(t, m, buttonEvent(t, "tickets:close:"+tk.ID, gateGuild, "222", 0), tk.ID)
	if !strings.Contains(desc, "Only helpers or the ticket opener") {
		t.Fatalf("response = %q, want the close-permission error", desc)
	}
	if got, _ := m.store.load(gateGuild, tk.ID); got == nil || got.Status != "open" {
		t.Fatalf("ticket = %+v, want still open", got)
	}
}
