package tickets

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

// mockBot is a minimal commands.Interface for tests: only GetName is
// overridden; the embedded nil interface panics if any other method is
// called (which would indicate a test setup gap).
type mockBot struct {
	commands.Interface
}

func (mockBot) GetName() string { return "TestBot" }

// mockRest is a pure in-memory rest.Rest that records the calls the module
// makes and returns canned responses. No HTTP, no deadlock.
type mockRest struct {
	rest.Rest // nil — panics if an un-overridden method is called
	mu        sync.Mutex
	calls     []restCall
}

type restCall struct {
	method        string
	path          string
	body          string
	appID         string
	token         string
	mentionsSet   bool
	mentionsParse int
}

func (m *mockRest) record(method, path, body string) {
	m.mu.Lock()
	m.calls = append(m.calls, restCall{method: method, path: path, body: body})
	m.mu.Unlock()
}

func (m *mockRest) CreateGuildChannel(guildID snowflake.ID, _ discord.GuildChannelCreate, _ ...rest.RequestOpt) (discord.GuildChannel, error) {
	m.record("POST", "/guilds/"+guildID.String()+"/channels", "")
	var ch discord.GuildTextChannel
	if err := json.Unmarshal([]byte(`{"id":"999","type":0,"name":"test-channel"}`), &ch); err != nil {
		return nil, err
	}
	return &ch, nil
}

func (m *mockRest) CreateMessage(channelID snowflake.ID, mc discord.MessageCreate, _ ...rest.RequestOpt) (*discord.Message, error) {
	mp := 0
	ms := mc.AllowedMentions != nil
	if ms {
		mp = len(mc.AllowedMentions.Parse)
	}
	m.record("POST", "/channels/"+channelID.String()+"/messages", mc.Content)
	m.mu.Lock()
	m.calls[len(m.calls)-1].mentionsSet = ms
	m.calls[len(m.calls)-1].mentionsParse = mp
	m.mu.Unlock()
	return &discord.Message{ID: snowflake.ID(456)}, nil
}

func (m *mockRest) UpdateInteractionResponse(appID snowflake.ID, token string, mu discord.MessageUpdate, _ ...rest.RequestOpt) (*discord.Message, error) {
	var content string
	if mu.Content != nil {
		content = *mu.Content
	}
	m.record("PATCH", "/webhooks/"+appID.String()+"/"+token+"/messages/@original", content)
	m.mu.Lock()
	m.calls[len(m.calls)-1].appID = appID.String()
	m.calls[len(m.calls)-1].token = token
	m.mu.Unlock()
	return &discord.Message{ID: snowflake.ID(123)}, nil
}

func (m *mockRest) GetGuild(guildID snowflake.ID, _ bool, _ ...rest.RequestOpt) (*discord.RestGuild, error) {
	m.record("GET", "/guilds/"+guildID.String(), "")
	return &discord.RestGuild{Guild: discord.Guild{Name: "Test Guild"}}, nil
}

func (m *mockRest) GetMember(guildID, userID snowflake.ID, _ ...rest.RequestOpt) (*discord.Member, error) {
	m.record("GET", "/guilds/"+guildID.String()+"/members/"+userID.String(), "")
	return nil, nil
}

func (m *mockRest) callsFiltered(substr string) []restCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []restCall
	for _, c := range m.calls {
		if strings.Contains(c.path, substr) {
			out = append(out, c)
		}
	}
	return out
}

func (m *mockRest) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// wireHarness records interaction callbacks (via Respond) so the test can
// assert the exact callback sequence.
type wireHarness struct {
	mu        sync.Mutex
	callbacks []recordedCallback
}

type recordedCallback struct {
	responseType discord.InteractionResponseType
	data         discord.InteractionResponseData
}

func (h *wireHarness) respond(rt discord.InteractionResponseType, data discord.InteractionResponseData, _ ...rest.RequestOpt) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.callbacks = append(h.callbacks, recordedCallback{responseType: rt, data: data})
	return nil
}

func (h *wireHarness) callbackCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.callbacks)
}

// buildModalEvent builds a real events.ModalSubmitInteractionCreate by
// JSON-decoding a discord.ModalSubmitInteraction fixture (baseInteraction is
// unexported, so this is the only route).
func buildModalEvent(t *testing.T, customID, guildID string, components ...map[string]any) *events.ModalSubmitInteractionCreate {
	t.Helper()
	data := map[string]any{
		"custom_id": customID,
	}
	if len(components) > 0 {
		data["components"] = components
	}
	fixture := map[string]any{
		"id":             "123",
		"application_id": "456",
		"token":          "tok",
		"user":           map[string]any{"id": "42", "username": "vixen"},
		"data":           data,
	}
	if guildID != "" {
		fixture["guild_id"] = guildID
	}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var mi discord.ModalSubmitInteraction
	if err := json.Unmarshal(raw, &mi); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	return &events.ModalSubmitInteractionCreate{
		ModalSubmitInteraction: mi,
		Respond: func(rt discord.InteractionResponseType, data discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			return nil
		},
	}
}

// newWireModule builds a TicketsModule with a mockRest and the given guild
// config.
func newWireModule(t *testing.T, h *wireHarness, guilds map[string]*Config) (*TicketsModule, *mockRest) {
	t.Helper()
	r := &mockRest{}
	return &TicketsModule{
		ctx:    &modules.Context{DataDir: t.TempDir(), Logger: testLogger{}, Rest: r, Bot: mockBot{}},
		store:  mustOpenStore(t),
		module: &ModuleConfig{Version: configVersion},
		guilds: guilds,
		loaded: true,
	}, r
}

func mustOpenStore(t *testing.T) *store {
	t.Helper()
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	return st
}

// TestModalWireSuccess verifies the full modal-submit REST sequence: exactly
// one interaction callback (defer, ephemeral), zero further interaction
// callbacks, zero UPDATE_MESSAGE callbacks, and exactly one PATCH
// /webhooks/{app}/{token}/messages/@original carrying the success message.
func TestModalWireSuccess(t *testing.T) {
	h := &wireHarness{}
	guilds := map[string]*Config{
		"789": {
			Version: configVersion,
			Types: map[string]*TypeConfig{
				"staff": {Key: "staff", Label: "Staff", Enabled: true, Category: "111"},
			},
			Panels: map[string]PanelConfig{
				"staff": {Name: "Staff", TypeKey: "staff", Questions: []QuestionConfig{{Label: "What is the issue?"}}},
			},
		},
	}
	m, r := newWireModule(t, h, guilds)

	// Components carry the submitted answer for the panel's question.
	// TextInput is wrapped in an ActionRow (type 1) — the LayoutComponent
	// the UnmarshalJSON expects.
	e := buildModalEvent(t, "tickets:openmodal:staff", "789",
		map[string]any{"type": 1, "components": []map[string]any{
			{"type": 4, "custom_id": "q0", "style": 1, "value": "server is down"},
		}})
	e.Respond = h.respond

	m.onOpenModalSubmit(e)

	// Exactly one interaction callback: the defer.
	if got := h.callbackCount(); got != 1 {
		t.Fatalf("callback count = %d, want 1", got)
	}
	h.mu.Lock()
	cb := h.callbacks[0]
	if cb.responseType != discord.InteractionResponseTypeDeferredCreateMessage {
		t.Fatalf("callback type = %v, want DeferredCreateMessage", cb.responseType)
	}
	// The defer must be ephemeral.
	mc, ok := cb.data.(discord.MessageCreate)
	if !ok {
		t.Fatalf("callback data is %T, want discord.MessageCreate", cb.data)
	}
	if mc.Flags&discord.MessageFlagEphemeral == 0 {
		t.Fatal("defer callback is not ephemeral")
	}
	h.mu.Unlock()

	// Exactly one PATCH /webhooks/{app}/{token}/messages/@original.
	patches := r.callsFiltered("/messages/@original")
	if len(patches) != 1 {
		t.Fatalf("PATCH count = %d, want 1", len(patches))
	}
	if !strings.Contains(patches[0].body, "Ticket <#999> opened as `staff-0001`.") {
		t.Fatalf("PATCH body = %q, want success message", patches[0].body)
	}
	// The PATCH must carry the APPLICATION id and the interaction token,
	// not the interaction id.
	if patches[0].appID != e.ApplicationID().String() {
		t.Fatalf("PATCH appID = %q, want %q (application id, not interaction id)", patches[0].appID, e.ApplicationID().String())
	}
	if patches[0].token != e.Token() {
		t.Fatalf("PATCH token = %q, want %q", patches[0].token, e.Token())
	}

	// The channel was created and messages were posted.
	if len(r.callsFiltered("/guilds/789/channels")) != 1 {
		t.Fatal("expected one POST /guilds/789/channels")
	}
	msgs := r.callsFiltered("/channels/999/messages")
	if len(msgs) != 2 {
		t.Fatalf("expected 2 POST /channels/999/messages (welcome + answers), got %d", len(msgs))
	}
	// The answers message (second) must contain the question label and the
	// submitted value, and must be posted with no pings.
	answers := msgs[1]
	if !strings.Contains(answers.body, "What is the issue?") {
		t.Fatalf("answers body missing question label: %q", answers.body)
	}
	if !strings.Contains(answers.body, "server is down") {
		t.Fatalf("answers body missing submitted value: %q", answers.body)
	}
	if !answers.mentionsSet {
		t.Fatal("answers message has no AllowedMentions (nil = Discord default parsing = pings)")
	}
	if answers.mentionsParse != 0 {
		t.Fatalf("answers message has %d mention parse types, want 0 (no pings)", answers.mentionsParse)
	}
}

// TestModalWireForeignCustomID verifies that a foreign custom_id produces zero
// callbacks and zero REST calls.
func TestModalWireForeignCustomID(t *testing.T) {
	h := &wireHarness{}
	m, r := newWireModule(t, h, map[string]*Config{})

	e := buildModalEvent(t, "other:modal:staff", "789")
	e.Respond = h.respond

	m.onOpenModalSubmit(e)

	if got := h.callbackCount(); got != 0 {
		t.Fatalf("callback count = %d, want 0", got)
	}
	if got := r.callCount(); got != 0 {
		t.Fatalf("REST call count = %d, want 0", got)
	}
}

// TestModalWireNoGuild verifies that a modal submit with no guild produces one
// pre-defer ephemeral CreateMessage and no PATCH.
func TestModalWireNoGuild(t *testing.T) {
	h := &wireHarness{}
	m, r := newWireModule(t, h, map[string]*Config{})

	e := buildModalEvent(t, "tickets:openmodal:staff", "")
	e.Respond = h.respond

	m.onOpenModalSubmit(e)

	// Exactly one interaction callback: CreateMessage (pre-defer).
	if got := h.callbackCount(); got != 1 {
		t.Fatalf("callback count = %d, want 1", got)
	}
	h.mu.Lock()
	cb := h.callbacks[0]
	h.mu.Unlock()
	if cb.responseType != discord.InteractionResponseTypeCreateMessage {
		t.Fatalf("callback type = %v, want CreateMessage", cb.responseType)
	}
	mc, ok := cb.data.(discord.MessageCreate)
	if !ok {
		t.Fatalf("callback data is %T, want discord.MessageCreate", cb.data)
	}
	if mc.Flags&discord.MessageFlagEphemeral == 0 {
		t.Fatal("CreateMessage callback is not ephemeral")
	}
	if mc.Content != "Tickets only work inside a server." {
		t.Fatalf("CreateMessage content = %q, want server-only message", mc.Content)
	}

	// No PATCH (no defer, no updateResult).
	if len(r.callsFiltered("/messages/@original")) != 0 {
		t.Fatal("expected no PATCH, got one")
	}
}

// TestModalWireUnknownPanel verifies that an unknown panel produces one defer
// and one PATCH with the "no longer configured" message.
func TestModalWireUnknownPanel(t *testing.T) {
	h := &wireHarness{}
	guilds := map[string]*Config{
		"789": {Version: configVersion, Types: map[string]*TypeConfig{}, Panels: map[string]PanelConfig{}},
	}
	m, r := newWireModule(t, h, guilds)

	e := buildModalEvent(t, "tickets:openmodal:unknown", "789")
	e.Respond = h.respond

	m.onOpenModalSubmit(e)

	if got := h.callbackCount(); got != 1 {
		t.Fatalf("callback count = %d, want 1", got)
	}
	h.mu.Lock()
	cb := h.callbacks[0]
	h.mu.Unlock()
	if cb.responseType != discord.InteractionResponseTypeDeferredCreateMessage {
		t.Fatalf("callback type = %v, want DeferredCreateMessage", cb.responseType)
	}

	patches := r.callsFiltered("/messages/@original")
	if len(patches) != 1 {
		t.Fatalf("PATCH count = %d, want 1", len(patches))
	}
	if !strings.Contains(patches[0].body, "This panel is no longer configured.") {
		t.Fatalf("PATCH body = %q, want unknown-panel message", patches[0].body)
	}
	if patches[0].appID != e.ApplicationID().String() {
		t.Fatalf("PATCH appID = %q, want %q", patches[0].appID, e.ApplicationID().String())
	}
	if patches[0].token != e.Token() {
		t.Fatalf("PATCH token = %q, want %q", patches[0].token, e.Token())
	}
}

// TestModalWireDisabledType verifies that a disabled type produces one defer
// and one PATCH with the "currently disabled" message.
func TestModalWireDisabledType(t *testing.T) {
	h := &wireHarness{}
	guilds := map[string]*Config{
		"789": {
			Version: configVersion,
			Types: map[string]*TypeConfig{
				"staff": {Key: "staff", Label: "Staff", Enabled: false, Category: "111"},
			},
			Panels: map[string]PanelConfig{
				"staff": {Name: "Staff", TypeKey: "staff"},
			},
		},
	}
	m, r := newWireModule(t, h, guilds)

	e := buildModalEvent(t, "tickets:openmodal:staff", "789")
	e.Respond = h.respond

	m.onOpenModalSubmit(e)

	if got := h.callbackCount(); got != 1 {
		t.Fatalf("callback count = %d, want 1", got)
	}
	h.mu.Lock()
	cb := h.callbacks[0]
	h.mu.Unlock()
	if cb.responseType != discord.InteractionResponseTypeDeferredCreateMessage {
		t.Fatalf("callback type = %v, want DeferredCreateMessage", cb.responseType)
	}

	patches := r.callsFiltered("/messages/@original")
	if len(patches) != 1 {
		t.Fatalf("PATCH count = %d, want 1", len(patches))
	}
	if !strings.Contains(patches[0].body, "This ticket type is currently disabled.") {
		t.Fatalf("PATCH body = %q, want disabled-type message", patches[0].body)
	}
	if patches[0].appID != e.ApplicationID().String() {
		t.Fatalf("PATCH appID = %q, want %q", patches[0].appID, e.ApplicationID().String())
	}
	if patches[0].token != e.Token() {
		t.Fatalf("PATCH token = %q, want %q", patches[0].token, e.Token())
	}
}
