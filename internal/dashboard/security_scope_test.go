package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/oauth2"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
)

type scopeFakeBot struct {
	commands.Interface
	owner      string
	guildOwner string
	perms      discord.Permissions
}

func (f *scopeFakeBot) IsOwner(id string) bool        { return id == f.owner }
func (f *scopeFakeBot) GetGuildOwnerID(string) string { return f.guildOwner }
func (f *scopeFakeBot) GetUserPermissions(string, string) discord.Permissions {
	return f.perms
}

func testTextChannel(t *testing.T, id, guildID string) discord.GuildChannel {
	t.Helper()
	var ch discord.GuildTextChannel
	raw := `{"id":"` + id + `","guild_id":"` + guildID + `","type":0,"name":"c"}`
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		t.Fatal(err)
	}
	return &ch
}

func TestDashboardChannelScopeRejectsKnownForeignChannel(t *testing.T) {
	caches := cache.New(cache.WithCaches(cache.FlagChannels))
	caches.AddChannel(testTextChannel(t, "2", "1"))
	caches.AddChannel(testTextChannel(t, "3", "2"))
	m := &DashboardModule{client: &bot.Client{Caches: caches}}

	if err := m.checkChannelInGuild("1", "2"); err != nil {
		t.Fatalf("same-guild channel rejected: %v", err)
	}
	if err := m.checkChannelInGuild("1", "3"); err == nil {
		t.Fatal("foreign channel accepted")
	}
	if err := m.checkChannelInGuild("1", "<#3>"); err == nil {
		t.Fatal("foreign channel mention accepted")
	}
}

func TestDashboardRoleGrantHonorsHierarchy(t *testing.T) {
	caches := cache.New(cache.WithCaches(cache.FlagRoles | cache.FlagMembers))
	caches.AddRole(discord.Role{ID: 11, GuildID: 1, Position: 10})
	caches.AddRole(discord.Role{ID: 12, GuildID: 1, Position: 5})
	caches.AddRole(discord.Role{ID: 13, GuildID: 1, Position: 3})
	caches.AddMember(discord.Member{GuildID: 1, User: discord.User{ID: 10}, RoleIDs: []snowflake.ID{12}})
	m := &DashboardModule{
		bot:    &scopeFakeBot{owner: "99", guildOwner: "98"},
		client: &bot.Client{Caches: caches},
	}
	us := &userSession{userID: 10}

	for _, role := range []string{"11", "12"} {
		if err := m.checkRoleGrant(us, "1", role); err == nil {
			t.Fatalf("role %s at/above the member's top role was granted", role)
		}
	}
	if err := m.checkRoleGrant(us, "1", "13"); err != nil {
		t.Fatalf("lower role rejected: %v", err)
	}
}

func TestDashboardPunishmentRequiresDiscordPermission(t *testing.T) {
	m := &DashboardModule{bot: &scopeFakeBot{owner: "99", perms: discord.PermissionKickMembers}}
	us := &userSession{userID: 10}

	if m.punishmentAllowed(us, "1", "ban") {
		t.Fatal("ban allowed without Ban Members")
	}
	if !m.punishmentAllowed(us, "1", "kick") {
		t.Fatal("kick denied with Kick Members")
	}
	if m.punishmentAllowed(us, "1", "mute") {
		t.Fatal("mute allowed without Moderate Members")
	}
	if !m.punishmentAllowed(us, "1", "none") {
		t.Fatal("none punishment must always be allowed")
	}
}

func TestOAuthStateCookieBinding(t *testing.T) {
	m := &DashboardModule{
		cfg:      &DashboardConfig{SessionSecret: "test-secret"},
		sessions: newSessionStore(),
	}
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	w := httptest.NewRecorder()
	if !m.setOAuthStateCookie(w, r, "state-123") {
		t.Fatal("state cookie was not set")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != oauthStateCookie || c.Path != "/callback" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie attributes = %+v", c)
	}

	callback := httptest.NewRequest(http.MethodGet, "/callback?code=x&state=state-123", nil)
	callback.AddCookie(c)
	if !m.verifyOAuthState(callback, "state-123") {
		t.Fatal("valid state cookie rejected")
	}
	if m.verifyOAuthState(callback, "different-state") {
		t.Fatal("mismatched state accepted")
	}
	tampered := httptest.NewRequest(http.MethodGet, "/callback?code=x&state=state-123", nil)
	tampered.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: c.Value + "x"})
	if m.verifyOAuthState(tampered, "state-123") {
		t.Fatal("tampered state cookie accepted")
	}
}

func TestOAuthCallbackRequiresStateCookie(t *testing.T) {
	m := newLoginTestModule(t, "")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/callback?code=x&state=state-123", nil)
	m.handleCallback(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
}

func TestTicketAPIsRequireGuildManagement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.html")
	if err := os.WriteFile(path, []byte("<script>alert(1)</script>"), 0o600); err != nil {
		t.Fatal(err)
	}
	stub := &stubTickets{filePath: path}
	m, _ := ticketsTestModule(t, stub)
	m.sessions.put("regular", &userSession{
		userID:    snowflake.ID(2),
		username:  "regular",
		csrfToken: "tok2",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
		guildsAt:  time.Now(),
		oauthGuilds: []discord.OAuth2Guild{
			{ID: snowflake.ID(111), Permissions: 0},
		},
	})
	cookie := m.signCookie("regular")

	if w := ticketsReq(m, cookie, "GET", "/api/tickets/111/support-0007/transcript", "", ""); w.Code != http.StatusForbidden {
		t.Fatalf("transcript status = %d, want 403", w.Code)
	}
	if calls := stub.transcriptCallLog(); len(calls) != 0 {
		t.Fatalf("transcript reached the module: %+v", calls)
	}
	if w := ticketsReq(m, cookie, "GET", "/api/ticketfiles/111/support-0007/note.html", "", ""); w.Code != http.StatusForbidden {
		t.Fatalf("ticketfile status = %d, want 403", w.Code)
	}
	if w := ticketsReq(m, cookie, "GET", "/api/tickets/111/types", "", ""); w.Code != http.StatusForbidden {
		t.Fatalf("ticket types status = %d, want 403", w.Code)
	}
	if w := ticketsReq(m, cookie, "GET", "/g/111/tickets", "", ""); w.Code != http.StatusNotFound {
		t.Fatalf("tickets page status = %d, want 404", w.Code)
	}
}

func TestTicketAttachmentActiveContentIsDownload(t *testing.T) {
	dir := t.TempDir()
	htmlPath := filepath.Join(dir, "evil.html")
	if err := os.WriteFile(htmlPath, []byte("<script>alert(1)</script>"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, cookie := ticketsTestModule(t, &stubTickets{filePath: htmlPath})
	w := ticketsReq(m, cookie, "GET", "/api/ticketfiles/111/support-0007/evil.html", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := w.Header().Get("Content-Security-Policy"); got != "sandbox" {
		t.Fatalf("CSP = %q", got)
	}

	pngPath := filepath.Join(dir, "ok.png")
	if err := os.WriteFile(pngPath, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	m2, cookie2 := ticketsTestModule(t, &stubTickets{filePath: pngPath})
	w = ticketsReq(m2, cookie2, "GET", "/api/ticketfiles/111/support-0007/ok.png", "", "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("png response = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("inline image must not be forced to download")
	}
}
