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

func gateResponse(t *testing.T, m *TicketsModule, e *events.ComponentInteractionCreate, close bool, ticketID string) string {
	t.Helper()
	h := &wireHarness{}
	e.Respond = h.respond
	if close {
		m.onCloseButton(e, ticketID)
	} else {
		m.onClaimButton(e, ticketID)
	}
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

// TestClaimGate pins who may claim: helpers (helper role, Manage Messages,
// bot owner/elevated) only. The opener and unrelated ticket members cannot.
func TestClaimGate(t *testing.T) {
	m := gateModule(t, &TypeConfig{Key: "staff", HelperRoles: []string{"7777"}, AllowClaim: boolPtr(true)},
		map[snowflake.ID][]snowflake.ID{333: {7777}})
	tk := gateTicket()
	cases := []struct {
		name  string
		user  string
		perms discord.Permissions
		want  bool
	}{
		{"opener", "111", 0, false},
		{"stranger", "222", 0, false},
		{"helper role", "333", 0, true},
		{"manage messages", "444", discord.PermissionManageMessages, true},
		{"bot owner", "1", 0, true},
	}
	for _, tc := range cases {
		e := buttonEvent(t, "tickets:claim:"+tk.ID, gateGuild, tc.user, tc.perms)
		if got := m.isHelperInteraction(e, tk); got != tc.want {
			t.Errorf("%s: helper = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestClaimButtonRejectsOpener pins the handler wiring, not just the
// predicate: pressing Claim as the opener must respond and leave the ticket
// unclaimed.
func TestClaimButtonRejectsOpener(t *testing.T) {
	m := gateModule(t, &TypeConfig{Key: "staff", HelperRoles: []string{"7777"}, AllowClaim: boolPtr(true)}, nil)
	tk := gateTicket()
	if err := m.store.save(tk); err != nil {
		t.Fatalf("save: %v", err)
	}
	desc := gateResponse(t, m, buttonEvent(t, "tickets:claim:"+tk.ID, gateGuild, "111", 0), false, tk.ID)
	if !strings.Contains(desc, "Only helpers") {
		t.Fatalf("response = %q, want the helper-only error", desc)
	}
	got, _ := m.store.load(gateGuild, tk.ID)
	if got == nil || got.ClaimerID != "" {
		t.Fatalf("ticket = %+v, want unclaimed", got)
	}
}

// TestClaimButtonHelperClaims pins the successful path and the allow_claim
// switch: a helper claims, and a type with claiming disabled refuses.
func TestClaimButtonHelperClaims(t *testing.T) {
	m := gateModule(t, &TypeConfig{Key: "staff", HelperRoles: []string{"7777"}, AllowClaim: boolPtr(true)},
		map[snowflake.ID][]snowflake.ID{333: {7777}})
	tk := gateTicket()
	if err := m.store.save(tk); err != nil {
		t.Fatalf("save: %v", err)
	}
	gateResponse(t, m, buttonEvent(t, "tickets:claim:"+tk.ID, gateGuild, "333", 0), false, tk.ID)
	got, _ := m.store.load(gateGuild, tk.ID)
	if got == nil || got.ClaimerID != "333" {
		t.Fatalf("claimer = %+v, want 333", got)
	}

	m.mu.Lock()
	m.guilds[gateGuild].Types["staff"].AllowClaim = boolPtr(false)
	m.mu.Unlock()
	tk2 := gateTicket()
	if err := m.store.save(tk2); err != nil {
		t.Fatalf("save: %v", err)
	}
	desc := gateResponse(t, m, buttonEvent(t, "tickets:claim:"+tk2.ID, gateGuild, "333", 0), false, tk2.ID)
	if !strings.Contains(desc, "Claiming is disabled") {
		t.Fatalf("response = %q, want the disabled-claiming error", desc)
	}
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

// TestTicketCommandSurface pins the user-facing actions in both forms: the
// prefix commands and their slash twins, with a user option on add/remove.
func TestTicketCommandSurface(t *testing.T) {
	m := &TicketsModule{}
	prefix := map[string]bool{}
	for _, c := range m.Commands() {
		prefix[c.Name] = c.Execute != nil
	}
	for _, name := range []string{"close", "claim", "add", "remove"} {
		if !prefix[name] {
			t.Errorf("prefix command %q missing", name)
		}
	}
	slash := map[string]commands.SlashCommand{}
	for _, c := range m.SlashCommands() {
		slash[c.Name] = c
	}
	for _, name := range []string{"close", "claim", "add", "remove"} {
		c, ok := slash[name]
		if !ok || c.Execute == nil {
			t.Errorf("slash command %q missing", name)
			continue
		}
		if (name == "add" || name == "remove") && len(c.Options) != 1 {
			t.Errorf("/%s options = %d, want 1 (member)", name, len(c.Options))
		}
		if name == "add" || name == "remove" {
			if _, ok := c.Options[0].(discord.ApplicationCommandOptionUser); !ok {
				t.Errorf("/%s option type = %T, want user", name, c.Options[0])
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
	desc := gateResponse(t, m, buttonEvent(t, "tickets:close:"+tk.ID, gateGuild, "222", 0), true, tk.ID)
	if !strings.Contains(desc, "Only helpers or the ticket opener") {
		t.Fatalf("response = %q, want the close-permission error", desc)
	}
	if got, _ := m.store.load(gateGuild, tk.ID); got == nil || got.Status != "open" {
		t.Fatalf("ticket = %+v, want still open", got)
	}
}
