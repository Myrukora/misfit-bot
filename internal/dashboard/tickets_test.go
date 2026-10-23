package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/oauth2"
	"github.com/disgoorg/snowflake/v2"
	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

// ── Test doubles ─────────────────────────────────────────────────────────

// stubTickets is a recording modules.Module that also implements the two
// OPTIONAL tickets contracts (TicketProvider + TicketAdmin). It exists so the
// route tests can assert BOTH the HTTP shape and that a rejected request never
// reaches the module (i.e. no panel mutation is attempted).
type stubTickets struct {
	mu sync.Mutex

	// TicketFilePath behavior: path returned / error to return.
	filePath string
	fileErr  error

	// List behavior: nil (the zero value) for every list means "empty", so
	// tests that don't configure a list behave exactly as before.
	openTickets   []modules.TicketSummary
	closedTickets []modules.TicketSummary
	types         []modules.TypeSummary
	panels        []modules.PanelSummary

	// Dashboard management (TicketAdmin): in-memory state + recorded calls.
	typeDetails    []modules.TypeDetail
	settings       modules.TicketGuildSettings
	panelWrites    []panelWriteCall
	panelDeletes   []string
	panelActions   []panelActionCall
	typeWrites     []typeWriteCall
	typeDeletes    []string
	settingsWrites []settingsWriteCall

	// TicketTranscript behavior: bytes returned / error to return, plus the
	// recorded calls. The zero value (nil bytes, nil error) keeps existing
	// tests unchanged.
	transcriptBytes []byte
	transcriptErr   error
	transcriptCalls []transcriptCall
}

type panelWriteCall struct {
	guild string
	name  string
	in    modules.PanelInput
}

type panelActionCall struct {
	guild, name, action string
}

type typeWriteCall struct {
	guild, key string
	in         modules.TypeDetail
}

type settingsWriteCall struct {
	guild string
	in    modules.TicketGuildSettings
}

func (s *stubTickets) panelWriteLog() []panelWriteCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]panelWriteCall(nil), s.panelWrites...)
}

func (s *stubTickets) panelActionLog() []panelActionCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]panelActionCall(nil), s.panelActions...)
}

func (s *stubTickets) typeWriteLog() []typeWriteCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]typeWriteCall(nil), s.typeWrites...)
}

func (s *stubTickets) settingsWriteLog() []settingsWriteCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]settingsWriteCall(nil), s.settingsWrites...)
}

type transcriptCall struct {
	guild  string
	ticket string
}

func (s *stubTickets) transcriptCallLog() []transcriptCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]transcriptCall(nil), s.transcriptCalls...)
}

// modules.Module
func (s *stubTickets) Name() string                  { return "tickets" }
func (s *stubTickets) Version() string               { return "test" }
func (s *stubTickets) Description() string           { return "stub" }
func (s *stubTickets) Author() string                { return "test" }
func (s *stubTickets) OnLoad(*modules.Context) error { return nil }
func (s *stubTickets) OnUnload() error               { return nil }
func (s *stubTickets) Commands() []commands.Command  { return nil }
func (s *stubTickets) Dependencies() []string        { return nil }
func (s *stubTickets) SlashCommands() []commands.SlashCommand {
	return nil
}

// modules.TicketProvider
// Each list returns its configured value; the zero value (nil) means "empty",
// so tests that don't configure a list behave exactly as before.
func (s *stubTickets) ListOpenTickets(string) ([]modules.TicketSummary, error) {
	return s.openTickets, nil
}
func (s *stubTickets) ListClosedTickets(string) ([]modules.TicketSummary, error) {
	return s.closedTickets, nil
}
func (s *stubTickets) ListTypes(string) ([]modules.TypeSummary, error) {
	return s.types, nil
}
func (s *stubTickets) GetTicket(guildID, ticketID string) (*modules.Ticket, error) {
	// The existence check in serveTicketFile must pass for the streaming case.
	return &modules.Ticket{ID: ticketID, GuildID: guildID, Status: "closed"}, nil
}

func (s *stubTickets) TicketFilePath(guildID, ticketID, name string) (string, error) {
	if s.fileErr != nil {
		return "", s.fileErr
	}
	return s.filePath, nil
}

// modules.TicketAdmin
func (s *stubTickets) ListPanels(string) ([]modules.PanelSummary, error) {
	return s.panels, nil
}

func (s *stubTickets) GetPanel(_ string, name string) (modules.PanelSummary, error) {
	for _, p := range s.panels {
		if p.Name == name {
			return p, nil
		}
	}
	return modules.PanelSummary{}, errors.New("unknown panel")
}

func (s *stubTickets) CreatePanel(guildID string, in modules.PanelInput) (modules.PanelSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panelWrites = append(s.panelWrites, panelWriteCall{guild: guildID, name: in.Name, in: in})
	p := modules.PanelSummary{
		Name: in.Name, TypeKey: in.TypeKey, ChannelID: in.ChannelID,
		Title: in.Title, Description: in.Description, ModalTitle: in.ModalTitle,
		Questions: in.Questions,
	}
	s.panels = append(s.panels, p)
	return p, nil
}

func (s *stubTickets) UpdatePanel(guildID, name string, in modules.PanelInput) (modules.PanelSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panelWrites = append(s.panelWrites, panelWriteCall{guild: guildID, name: name, in: in})
	p := modules.PanelSummary{
		Name: name, TypeKey: in.TypeKey, ChannelID: in.ChannelID,
		Title: in.Title, Description: in.Description, ModalTitle: in.ModalTitle,
		Questions: in.Questions,
	}
	for i := range s.panels {
		if s.panels[i].Name == name {
			s.panels[i] = p
			return p, nil
		}
	}
	// The route tests care that the write is forwarded; an unknown name is
	// treated as an upsert by the stub (the module validates existence).
	s.panels = append(s.panels, p)
	return p, nil
}

func (s *stubTickets) DeletePanel(_ string, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panelDeletes = append(s.panelDeletes, name)
	return nil
}

func (s *stubTickets) PanelAction(guildID, name, action string) (modules.PanelSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panelActions = append(s.panelActions, panelActionCall{guild: guildID, name: name, action: action})
	return modules.PanelSummary{Name: name}, nil
}

func (s *stubTickets) ListTypeDetails(string) ([]modules.TypeDetail, error) {
	return s.typeDetails, nil
}

func (s *stubTickets) GetType(_ string, key string) (modules.TypeDetail, error) {
	for _, t := range s.typeDetails {
		if t.Key == key {
			return t, nil
		}
	}
	return modules.TypeDetail{}, errors.New("unknown type")
}

func (s *stubTickets) CreateType(guildID string, in modules.TypeDetail) (modules.TypeDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.typeWrites = append(s.typeWrites, typeWriteCall{guild: guildID, key: in.Key, in: in})
	s.typeDetails = append(s.typeDetails, in)
	return in, nil
}

func (s *stubTickets) UpdateType(guildID, key string, in modules.TypeDetail) (modules.TypeDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in.Key = key
	s.typeWrites = append(s.typeWrites, typeWriteCall{guild: guildID, key: key, in: in})
	return in, nil
}

func (s *stubTickets) DeleteType(_ string, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.typeDeletes = append(s.typeDeletes, key)
	return nil
}

func (s *stubTickets) GetGuildSettings(string) (modules.TicketGuildSettings, error) {
	return s.settings, nil
}

func (s *stubTickets) SetGuildSettings(guildID string, in modules.TicketGuildSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = in
	s.settingsWrites = append(s.settingsWrites, settingsWriteCall{guild: guildID, in: in})
	return nil
}

// modules.TicketTranscript
func (s *stubTickets) RefreshTranscript(guildID, ticketID string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcriptCalls = append(s.transcriptCalls, transcriptCall{guild: guildID, ticket: ticketID})
	if s.transcriptErr != nil {
		return nil, s.transcriptErr
	}
	return s.transcriptBytes, nil
}

var (
	_ modules.Module           = (*stubTickets)(nil)
	_ modules.TicketProvider   = (*stubTickets)(nil)
	_ modules.TicketAdmin      = (*stubTickets)(nil)
	_ modules.TicketTranscript = (*stubTickets)(nil)
)

// providerOnlyTickets is a modules.Module that implements TicketProvider but
// NOT TicketTranscript, so the transcript download route must answer 404
// (the module has no download surface).
type providerOnlyTickets struct{}

func (providerOnlyTickets) Name() string                  { return "tickets" }
func (providerOnlyTickets) Version() string               { return "test" }
func (providerOnlyTickets) Description() string           { return "stub" }
func (providerOnlyTickets) Author() string                { return "test" }
func (providerOnlyTickets) OnLoad(*modules.Context) error { return nil }
func (providerOnlyTickets) OnUnload() error               { return nil }
func (providerOnlyTickets) Commands() []commands.Command  { return nil }
func (providerOnlyTickets) Dependencies() []string        { return nil }
func (providerOnlyTickets) SlashCommands() []commands.SlashCommand {
	return nil
}

func (providerOnlyTickets) ListOpenTickets(string) ([]modules.TicketSummary, error) {
	return nil, nil
}
func (providerOnlyTickets) ListClosedTickets(string) ([]modules.TicketSummary, error) {
	return nil, nil
}
func (providerOnlyTickets) ListTypes(string) ([]modules.TypeSummary, error) {
	return nil, nil
}
func (providerOnlyTickets) GetTicket(guildID, ticketID string) (*modules.Ticket, error) {
	return &modules.Ticket{ID: ticketID, GuildID: guildID, Status: "closed"}, nil
}
func (providerOnlyTickets) TicketFilePath(string, string, string) (string, error) {
	return "", errors.New("absent")
}

// ticketGetter resolves the "tickets" module name (the shared stubModuleGetter
// only knows "imagefilter").
type ticketGetter struct{ mod modules.Module }

func (g ticketGetter) Get(name string) (modules.Module, bool) {
	if name != "tickets" || g.mod == nil {
		return nil, false
	}
	return g.mod, true
}

// fakeTicketBot is the minimal commands.Interface the tickets routes touch:
// resolveLevel (IsOwner) + ticketProvider/ticketAdmin (GetModuleManager).
type fakeTicketBot struct {
	commands.Interface
	owner string
	mod   modules.Module
}

func (f *fakeTicketBot) IsOwner(id string) bool                         { return id == f.owner }
func (f *fakeTicketBot) IsElevated(string) bool                         { return false }
func (f *fakeTicketBot) GetModuleManager() any                          { return ticketGetter{mod: f.mod} }
func (f *fakeTicketBot) GetVersion() string                             { return "test" }
func (f *fakeTicketBot) GetCachedMember(string, string) *discord.Member { return nil }

// ticketsTestModule builds the dashboard module with the stub tickets module
// loaded and a signed owner session cookie.
func ticketsTestModule(t *testing.T, mod modules.Module) (*DashboardModule, string) {
	t.Helper()
	m := &DashboardModule{
		bot:      &fakeTicketBot{owner: "1", mod: mod},
		botName:  "TestBot",
		cfg:      &DashboardConfig{SessionSecret: "test-secret", Listen: "127.0.0.1:0"},
		client:   &bot.Client{Caches: cache.New()},
		oauth:    oauth2.New(snowflake.ID(1), "sec"),
		sessions: newSessionStore(),
		logger:   deadlockLogger{},
		dataDir:  t.TempDir(),
	}
	// Page routes render through m.tmpl, and baseData resolves the bot identity
	// via botIdentity(), which with an empty self-user cache falls through to
	// applicationName() → client.Rest.GetCurrentApplication() and dereferences a
	// nil Rest (these fixtures set no REST client). Seed both.
	tmpl, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	m.tmpl = tmpl
	m.client.Caches.SetSelfUser(discord.OAuth2User{User: discord.User{ID: snowflake.ID(1), Username: "TestBot"}})
	key := "sess-key"
	m.sessions.put(key, &userSession{
		userID:    snowflake.ID(1),
		username:  "owner",
		csrfToken: "tok",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
	})
	return m, m.signCookie(key)
}

// ticketsReq drives one request through the full middleware chain; csrf is the
// value sent in the CSRF header (pass "" to omit it).
func ticketsReq(m *DashboardModule, cookie, method, target, body, csrf string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	if csrf != "" {
		r.Header.Set(csrfHeader, csrf)
	}
	w := httptest.NewRecorder()
	m.buildHandler().ServeHTTP(w, r)
	return w
}

// ── Tests ────────────────────────────────────────────────────────────────

// TestTicketsPanelActionRejectsUnsupported pins that the dashboard exposes only
// suspend|resume|resend: "remove" (CLI-only) must be refused with 400 before any
// command dispatch happens.
func TestTicketsPanelActionRejectsUnsupported(t *testing.T) {
	stub := &stubTickets{}
	m, cookie := ticketsTestModule(t, stub)
	w := ticketsReq(m, cookie, "POST", "/api/tickets/1/panels/p1/remove", "{}", "tok")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unsupported action") {
		t.Fatalf("body = %s, want 'unsupported action'", w.Body.String())
	}
}

// TestTicketsPanelUpdateBadBody422 pins that a malformed panel payload is
// refused with 422 and never reaches the module.
func TestTicketsPanelUpdateBadBody422(t *testing.T) {
	stub := &stubTickets{}
	m, cookie := ticketsTestModule(t, stub)
	w := ticketsReq(m, cookie, "POST", "/api/tickets/1/panels/p1", `{"questions":"nope"}`, "tok")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", w.Code, w.Body.String())
	}
	if n := len(stub.panelWriteLog()); n != 0 {
		t.Fatalf("module reached %d times on a bad body, want 0", n)
	}
}

// TestTicketsPanelUpdateRequiresCSRF pins the CSRF gate: without a matching
// token the request is 403 and the module is never called.
func TestTicketsPanelUpdateRequiresCSRF(t *testing.T) {
	stub := &stubTickets{}
	m, cookie := ticketsTestModule(t, stub)
	w := ticketsReq(m, cookie, "POST", "/api/tickets/1/panels/p1",
		`{"channel_id":"123","type":"support","questions":[{"label":"Why?"}]}`, "wrong-token")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", w.Code, w.Body.String())
	}
	if n := len(stub.panelWriteLog()); n != 0 {
		t.Fatalf("module reached %d times without CSRF, want 0", n)
	}
}

// TestTicketsPanelUpdateForwardsPayload pins the happy path: a panel write
// reaches UpdatePanel with the guild, name and all fields intact.
func TestTicketsPanelUpdateForwardsPayload(t *testing.T) {
	stub := &stubTickets{}
	m, cookie := ticketsTestModule(t, stub)
	payload, err := json.Marshal(map[string]any{
		"name": "ignored", "channel_id": "123", "type": "support",
		"title": "Need help?", "description": "Open a ticket", "modal_title": "Details",
		"questions": []modules.PanelQuestion{
			{Label: "What happened?", Style: "paragraph", Required: true},
			{Label: "Steam ID", Placeholder: "765…", Style: "short"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w := ticketsReq(m, cookie, "POST", "/api/tickets/g7/panels/support", string(payload), "tok")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	calls := stub.panelWriteLog()
	if len(calls) != 1 {
		t.Fatalf("module called %d times, want 1", len(calls))
	}
	got := calls[0]
	if got.guild != "g7" || got.name != "support" {
		t.Fatalf("got guild=%q name=%q, want g7/support", got.guild, got.name)
	}
	if got.in.Title != "Need help?" || got.in.ChannelID != "123" || got.in.TypeKey != "support" {
		t.Fatalf("fields mangled: %+v", got.in)
	}
	if len(got.in.Questions) != 2 || got.in.Questions[0].Label != "What happened?" {
		t.Fatalf("questions mangled: %+v", got.in.Questions)
	}
}

// TestTicketsTypeCreateForwardsPayload pins the type editor write path.
func TestTicketsTypeCreateForwardsPayload(t *testing.T) {
	stub := &stubTickets{}
	m, cookie := ticketsTestModule(t, stub)
	payload, err := json.Marshal(map[string]any{
		"key": "support", "label": "Support", "category": "123", "enabled": true,
		"ping_roles": []string{"9"}, "helper_roles": []string{"8"},
		"access_roles": []string{}, "color": 0x5865F2,
		"allow_close": true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w := ticketsReq(m, cookie, "POST", "/api/tickets/g7/types", string(payload), "tok")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	calls := stub.typeWriteLog()
	if len(calls) != 1 || calls[0].guild != "g7" || calls[0].key != "support" {
		t.Fatalf("module calls = %+v, want one g7/support", calls)
	}
	if calls[0].in.Label != "Support" || !calls[0].in.Enabled || calls[0].in.Color != 0x5865F2 {
		t.Fatalf("type fields mangled: %+v", calls[0].in)
	}
}

// TestTicketsSettingsSaveForwardsPayload pins the log-channel write.
func TestTicketsSettingsSaveForwardsPayload(t *testing.T) {
	stub := &stubTickets{}
	m, cookie := ticketsTestModule(t, stub)
	w := ticketsReq(m, cookie, "POST", "/api/tickets/g7/settings", `{"log_channel":"123"}`, "tok")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	calls := stub.settingsWriteLog()
	if len(calls) != 1 || calls[0].guild != "g7" || calls[0].in.LogChannel != "123" {
		t.Fatalf("settings calls = %+v, want one g7/123", calls)
	}
}

// TestServeTicketFileStreamsBytes pins the mirrored-file endpoint: on success the
// exact file bytes are served (the module owns path validation).
func TestServeTicketFileStreamsBytes(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.png")
	want := []byte("\x89PNG\r\n\x1a\nmirrored-bytes")
	if err := os.WriteFile(file, want, 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	stub := &stubTickets{filePath: file}
	m, cookie := ticketsTestModule(t, stub)
	w := ticketsReq(m, cookie, "GET", "/api/ticketfiles/1/support-0007/a.png", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if got := w.Body.Bytes(); string(got) != string(want) {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

// TestServeTicketFileNotFoundOnProviderError pins the delegation: when the
// module refuses the path (unknown guild/ticket/name), the endpoint is a 404 —
// the dashboard performs no path joining of its own.
func TestServeTicketFileNotFoundOnProviderError(t *testing.T) {
	stub := &stubTickets{fileErr: errors.New("invalid file name")}
	m, cookie := ticketsTestModule(t, stub)
	w := ticketsReq(m, cookie, "GET", "/api/ticketfiles/1/support-0007/gone.png", "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", w.Code, w.Body.String())
	}
}

// TestTicketsTranscriptDownload pins the regenerate+download endpoint: the
// module's rendered bytes are served with attachment headers, and the module
// is called exactly once with the routed guild + ticket.
func TestTicketsTranscriptDownload(t *testing.T) {
	want := []byte("<!doctype html><html><body>transcript</body></html>")
	stub := &stubTickets{transcriptBytes: want}
	m, cookie := ticketsTestModule(t, stub)
	w := ticketsReq(m, cookie, "GET", "/api/tickets/111/support-0007/transcript", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/html; charset=utf-8", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); cd != `attachment; filename="ticket-support-0007.html"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	if got := w.Body.String(); got != string(want) {
		t.Fatalf("body = %q, want %q", got, want)
	}
	calls := stub.transcriptCallLog()
	if len(calls) != 1 || calls[0].guild != "111" || calls[0].ticket != "support-0007" {
		t.Fatalf("module calls = %+v, want one 111/support-0007", calls)
	}
}

// TestTicketsTranscriptDownloadForbidden pins the guild gate: a guild outside
// the allowlist is a 403 and never reaches the module.
func TestTicketsTranscriptDownloadForbidden(t *testing.T) {
	stub := &stubTickets{transcriptBytes: []byte("x")}
	m, cookie := ticketsTestModule(t, stub)
	m.cfg.AllowedGuilds = []string{"999"}
	w := ticketsReq(m, cookie, "GET", "/api/tickets/111/support-0007/transcript", "", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", w.Code, w.Body.String())
	}
	if n := len(stub.transcriptCallLog()); n != 0 {
		t.Fatalf("module reached %d times on a forbidden guild, want 0", n)
	}
}

// TestTicketsTranscriptDownloadUnimplemented pins the OPTIONAL contract: a
// provider without TicketTranscript gets a 404 (no download surface).
func TestTicketsTranscriptDownloadUnimplemented(t *testing.T) {
	m, cookie := ticketsTestModule(t, providerOnlyTickets{})
	w := ticketsReq(m, cookie, "GET", "/api/tickets/111/support-0007/transcript", "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", w.Code, w.Body.String())
	}
}

// ── Rendering ────────────────────────────────────────────────────────────

// TestTicketsPanelsCardRenders pins the panels table: one row per panel with a
// Resume action when suspended, and the per-row question editor prefilled from
// the panel's questions (plus the blank clone template).
func TestTicketsPanelsCardRenders(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	d := mkData(lvlOwner)
	d.ShowSidebar = false
	d.Page = "gtickets"
	d.GuildID = "1"
	d.GuildName = "G"
	d.Content = struct {
		GuildID      string
		Open         any
		Closed       any
		TypeDetails  []modules.TypeDetail
		Panels       []modules.PanelSummary
		Settings     modules.TicketGuildSettings
		Channels     []entityOpt
		Categories   []entityOpt
		Roles        []entityOpt
		ChannelNames map[string]string
		People       map[string]ticketPerson
		Error        string
	}{
		GuildID: "1",
		Open:    []modules.TicketSummary{{ID: "support-0001", Type: "support", OpenedAt: time.Now()}},
		TypeDetails: []modules.TypeDetail{
			{Key: "support", Label: "Support", Enabled: true, Category: "123"},
		},
		Panels: []modules.PanelSummary{{
			Name: "p1", TypeKey: "support", ChannelID: "123", Suspended: true,
			Questions: []modules.PanelQuestion{
				{Label: "Q1", Style: "paragraph", Required: true, Placeholder: "h"},
				{Label: "Q2", Style: "short", Value: "v"},
			},
		}},
		ChannelNames: map[string]string{"123": "help-desk"},
	}
	var sb strings.Builder
	if err := b.render(&sb, "rd_tickets", d); err != nil {
		t.Fatalf("render rd_tickets: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		`id="tickets-settings"`,
		`id="tickets-types"`,
		`id="tickets-panels"`,
		`data-name="p1"`,
		`data-action="resume"`, // suspended panel offers Resume, not Suspend
		`#help-desk`,           // channel name resolved from ChannelNames
		`id="tk-panel-modal"`, `id="tk-type-modal"`,
		`<template id="tk-qrow-tpl">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "No panels posted yet") {
		t.Errorf("empty-state rendered alongside populated panels")
	}
}

// TestTranscriptEmbedsAndCloseReason pins the transcript additions: the
// channel_deleted close reason, embed rendering through the mirrored-file
// endpoint, and link-embed dedup (a "link" embed whose URL is already shown as
// an attachment in the same entry must not render a second anchor).
func TestTranscriptEmbedsAndCloseReason(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	now := time.Now()
	tk := &modules.Ticket{
		ID: "support-0007", Type: "support", GuildID: "1", Status: "closed",
		CloseReason: "channel_deleted", ClosedAt: now,
		Log: []modules.LogEntry{{
			MsgID: "m1", AuthorID: "2", AuthorName: "opener", Timestamp: now,
			Embeds: []modules.Media{
				{URL: "https://media.tenor.com/x.gif", Kind: "image", Filename: "x.gif", LocalPath: "files/x.gif"},
				{URL: "https://example.com/page", Kind: "link", Filename: "page"},
				{URL: "https://example.com/dup.png", Kind: "link"},
			},
			Attachments: []modules.Media{{URL: "https://example.com/dup.png", Kind: "image", Filename: "dup.png"}},
		}},
	}
	d := mkData(lvlOwner)
	d.ShowSidebar = false
	d.Page = "transcript"
	d.Content = struct {
		Ticket  any
		GuildID string
	}{Ticket: tk, GuildID: "1"}
	var sb strings.Builder
	if err := b.render(&sb, "rd_transcript", d); err != nil {
		t.Fatalf("render rd_transcript: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		"channel deleted",
		`/api/ticketfiles/1/support-0007/x.gif`, // embed LocalPath mirroring
		`class="zoomable msg-media"`,
		`https://example.com/page`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	// The attachment renders its URL once (img src); the duplicate link embed
	// must not add any render of its own.
	if n := strings.Count(out, "https://example.com/dup.png"); n != 1 {
		t.Errorf("dup.png occurrences = %d, want 1 (attachment src only)", n)
	}
	if n := strings.Count(out, "🔗"); n != 1 {
		t.Errorf("link anchors = %d, want 1 (dup link embed not deduped)", n)
	}
}

// TestTicketsListPageRendersProviderRows is the handler-level regression for
// renderTicketsList: it drives the real /g/<id>/tickets route and asserts the
// rendered page carries what the provider returned. The sibling
// TestTicketsPanelsCardRenders builds d.Content by hand and renders the
// template directly, so it kept passing while the handler never assigned its
// payload to d.Content. With a nil .Content the template aborts at its first
// .Content field access ({{if .Content.Error}}), so a pre-fix page would have
// emitted an empty content section for every provider result.
func TestTicketsListPageRendersProviderRows(t *testing.T) {
	now := time.Now()
	stub := &stubTickets{
		openTickets: []modules.TicketSummary{
			{ID: "staff-0001", Type: "staff", GuildID: "1", OpenerID: "42", OpenedAt: now},
		},
		closedTickets: []modules.TicketSummary{
			{ID: "staff-0002", Type: "staff", GuildID: "1", OpenerID: "43", ClosedAt: now},
		},
		types: []modules.TypeSummary{{Key: "staff", Label: "Staff", Enabled: true}},
		typeDetails: []modules.TypeDetail{
			{Key: "staff", Label: "Staff", Enabled: true, Category: "123"},
		},
		panels: []modules.PanelSummary{{Name: "p1", TypeKey: "staff", ChannelID: "123"}},
	}
	m, cookie := ticketsTestModule(t, stub)

	w := ticketsReq(m, cookie, "GET", "/g/1/tickets", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	out := w.Body.String()
	for _, want := range []string{
		"Open tickets",   // the open-ticket card heading
		"Closed tickets", // the archive card heading
		"staff-0001",     // the open ticket row
		"staff-0002",     // the closed ticket row
		"Ticket types",   // the types card heading
		`data-name="p1"`, // the panel row
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in rendered page", want)
		}
	}
	if strings.Contains(out, "No manageable servers found.") {
		t.Errorf("page rendered the no-guild placeholder: .Content.GuildID was empty for a guild-scoped request")
	}
}

// TestHubTicketsRouteRedirectsPerServer pins that the hub carries no tickets
// list of its own: bare /tickets 303s to the server-scoped page, honoring a
// ?guild= link when the caller can manage that guild.
func TestHubTicketsRouteRedirectsPerServer(t *testing.T) {
	m, cookie := ticketsTestModule(t, &stubTickets{})

	w := ticketsReq(m, cookie, "GET", "/tickets?guild=1", "", "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %s)", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/g/1/tickets" {
		t.Errorf("Location = %q, want /g/1/tickets", loc)
	}

	// No guild the caller can manage → 404, not a rendered hub list page.
	m.cfg.AllowedGuilds = []string{"999"}
	w = ticketsReq(m, cookie, "GET", "/tickets", "", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (body %s)", w.Code, w.Body.String())
	}
}

// TestTicketStreamsEmitSnapshots pins the live-update endpoints: the list
// stream sends the rendered cards and the transcript stream sends the rendered
// conversation, as SSE frames.
func TestTicketStreamsEmitSnapshots(t *testing.T) {
	stub := &stubTickets{openTickets: []modules.TicketSummary{{
		ID: "staff-0001", Type: "support", OpenerID: "111", OpenedAt: time.Now(),
	}}}
	m, cookie := ticketsTestModule(t, stub)

	stream := func(target string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		req := httptest.NewRequest("GET", target, nil).WithContext(ctx)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		w := httptest.NewRecorder()
		m.buildHandler().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200 (body %s)", target, w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Fatalf("%s Content-Type = %q, want text/event-stream", target, ct)
		}
		return w.Body.String()
	}

	list := stream("/api/tickets/1/stream")
	if !strings.Contains(list, "event: tickets") || !strings.Contains(list, "staff-0001") {
		t.Fatalf("list stream = %q, want a tickets frame with the open ticket", list)
	}

	conv := stream("/api/tickets/1/staff-0001/stream")
	if !strings.Contains(conv, "event: messages") || !strings.Contains(conv, "No messages recorded") {
		t.Fatalf("transcript stream = %q, want a messages frame", conv)
	}
}
