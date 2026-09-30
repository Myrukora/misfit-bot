package dashboard

import (
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
	mu   sync.Mutex
	sets []panelSetCall

	// TicketFilePath behavior: path returned / error to return.
	filePath string
	fileErr  error

	// List behavior: nil (the zero value) for every list means "empty", so the
	// existing route tests keep seeing the same results as before.
	openTickets   []modules.TicketSummary
	closedTickets []modules.TicketSummary
	types         []modules.TypeSummary
	panels        []modules.PanelSummary

	// TicketTranscript behavior: bytes returned / error to return, plus the
	// recorded calls. The zero value (nil bytes, nil error) keeps existing
	// tests unchanged.
	transcriptBytes []byte
	transcriptErr   error
	transcriptCalls []transcriptCall
}

type panelSetCall struct {
	guild     string
	panel     string
	questions []modules.PanelQuestion
}

func (s *stubTickets) setCalls() []panelSetCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]panelSetCall(nil), s.sets...)
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
func (s *stubTickets) CloseTicket(string, string, string) error { return nil }

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

func (s *stubTickets) SetPanelQuestions(guildID, panel string, questions []modules.PanelQuestion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets = append(s.sets, panelSetCall{guild: guildID, panel: panel, questions: questions})
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
func (providerOnlyTickets) CloseTicket(string, string, string) error { return nil }
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

func (f *fakeTicketBot) IsOwner(id string) bool { return id == f.owner }
func (f *fakeTicketBot) IsElevated(string) bool { return false }
func (f *fakeTicketBot) GetModuleManager() any  { return ticketGetter{mod: f.mod} }

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

// TestTicketsQuestionsBadBody422 pins that a malformed questions payload is
// refused with 422 and never reaches the module.
func TestTicketsQuestionsBadBody422(t *testing.T) {
	stub := &stubTickets{}
	m, cookie := ticketsTestModule(t, stub)
	w := ticketsReq(m, cookie, "POST", "/api/tickets/1/panels/p1/questions", `{"questions":"nope"}`, "tok")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", w.Code, w.Body.String())
	}
	if n := len(stub.setCalls()); n != 0 {
		t.Fatalf("module reached %d times on a bad body, want 0", n)
	}
}

// TestTicketsQuestionsRequiresCSRF pins the CSRF gate: without a matching token
// the request is 403 and the module is never called.
func TestTicketsQuestionsRequiresCSRF(t *testing.T) {
	stub := &stubTickets{}
	m, cookie := ticketsTestModule(t, stub)
	w := ticketsReq(m, cookie, "POST", "/api/tickets/1/panels/p1/questions",
		`{"questions":[{"label":"Why?"}]}`, "wrong-token")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", w.Code, w.Body.String())
	}
	if n := len(stub.setCalls()); n != 0 {
		t.Fatalf("module reached %d times without CSRF, want 0", n)
	}
}

// TestTicketsQuestionsForwardsPayload pins the happy path: the decoded payload
// reaches SetPanelQuestions with the guild, panel name and questions intact, and
// the response is the documented OK body.
func TestTicketsQuestionsForwardsPayload(t *testing.T) {
	stub := &stubTickets{}
	m, cookie := ticketsTestModule(t, stub)
	payload, err := json.Marshal(map[string]any{
		"questions": []modules.PanelQuestion{
			{Label: "What happened?", Style: "paragraph", Required: true},
			{Label: "Steam ID", Placeholder: "765…", Style: "short"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w := ticketsReq(m, cookie, "POST", "/api/tickets/g7/panels/support/questions", string(payload), "tok")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	calls := stub.setCalls()
	if len(calls) != 1 {
		t.Fatalf("module called %d times, want 1", len(calls))
	}
	got := calls[0]
	if got.guild != "g7" || got.panel != "support" {
		t.Fatalf("got guild=%q panel=%q, want g7/support", got.guild, got.panel)
	}
	if len(got.questions) != 2 {
		t.Fatalf("got %d questions, want 2", len(got.questions))
	}
	if got.questions[0].Label != "What happened?" || got.questions[0].Style != "paragraph" || !got.questions[0].Required {
		t.Fatalf("question 0 mangled: %+v", got.questions[0])
	}
	if got.questions[1].Placeholder != "765…" {
		t.Fatalf("question 1 mangled: %+v", got.questions[1])
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("body = %s, want ok:true", w.Body.String())
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
		GuildID string
		Open    any
		Closed  any
		Types   any
		Panels  any
		Error   string
	}{
		GuildID: "1",
		Open:    []modules.TicketSummary{{ID: "support-0001", Type: "support", OpenedAt: time.Now()}},
		Panels: []modules.PanelSummary{{
			Name: "p1", TypeKey: "support", ChannelID: "123", Suspended: true,
			Questions: []modules.PanelQuestion{
				{Label: "Q1", Style: "paragraph", Required: true, Placeholder: "h"},
				{Label: "Q2", Style: "short", Value: "v"},
			},
		}},
	}
	var sb strings.Builder
	if err := b.render(&sb, "rd_tickets", d); err != nil {
		t.Fatalf("render rd_tickets: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		`id="tickets-panels"`,
		`data-name="p1"`,
		`data-action="resume"`, // suspended panel offers Resume, not Suspend
		`tk-qeditor`, `tk-qrow-tpl`, `value="v"`,
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
		Ticket   any
		GuildID  string
		CloseURL string
	}{Ticket: tk, GuildID: "1", CloseURL: "/api/tickets/1/support-0007/close"}
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
	// The attachment renders its URL twice (anchor href + img src); the duplicate
	// link embed must not add any render of its own.
	if n := strings.Count(out, "https://example.com/dup.png"); n != 2 {
		t.Errorf("dup.png occurrences = %d, want 2 (attachment href+src only)", n)
	}
	if n := strings.Count(out, "🔗"); n != 1 {
		t.Errorf("link anchors = %d, want 1 (dup link embed not deduped)", n)
	}
}

// TestTicketsListPageRendersProviderRows is the handler-level regression for
// renderTicketsList: it drives the real /tickets?guild=<id> route and asserts
// the rendered page carries what the provider returned. The sibling
// TestTicketsPanelsCardRenders builds d.Content by hand and renders the
// template directly, so it kept passing while the handler never assigned its
// payload to d.Content. With a nil .Content the template aborts at its first
// .Content field access ({{if .Content.Error}}), so the pre-fix page emitted an
// empty content section — neither the data nor the "No manageable servers
// found." placeholder — for every provider result.
func TestTicketsListPageRendersProviderRows(t *testing.T) {
	now := time.Now()
	stub := &stubTickets{
		openTickets: []modules.TicketSummary{
			{ID: "staff-0001", Type: "staff", GuildID: "1", OpenerID: "42", OpenedAt: now},
		},
		closedTickets: []modules.TicketSummary{
			{ID: "staff-0002", Type: "staff", GuildID: "1", OpenerID: "43", ClosedAt: now},
		},
		types:  []modules.TypeSummary{{Key: "staff", Label: "Staff", Enabled: true}},
		panels: []modules.PanelSummary{{Name: "p1", TypeKey: "staff", ChannelID: "123"}},
	}
	m, cookie := ticketsTestModule(t, stub)

	w := ticketsReq(m, cookie, "GET", "/tickets?guild=1", "", "")
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
