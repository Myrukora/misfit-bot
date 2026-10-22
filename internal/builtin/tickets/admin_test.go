package tickets

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/disgoorg/disgo/rest"

	"github.com/misfit/bot/modules"
)

// adminModule builds a loaded module over a temp DataDir. srvURL is the REST
// stub base URL ("" = no Discord calls are expected).
func adminModule(t *testing.T, srvURL string) *TicketsModule {
	t.Helper()
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	var rc rest.Rest
	if srvURL != "" {
		rc = rest.New(rest.NewClient("fake-token", rest.WithURL(srvURL)))
	}
	return &TicketsModule{
		ctx:    &modules.Context{DataDir: dir, Logger: testLogger{}, Rest: rc},
		store:  st,
		module: &ModuleConfig{Version: configVersion},
		guilds: map[string]*Config{
			"111": {Version: configVersion, Types: map[string]*TypeConfig{}, Panels: map[string]PanelConfig{}},
		},
		loaded: true,
	}
}

// TestAdminTypesCRUD pins the dashboard type editor's data path: create (key
// normalized, fields stored), list/get, full update, delete, and the
// duplicate/enable-without-category guards.
func TestAdminTypesCRUD(t *testing.T) {
	m := adminModule(t, "")
	const gid = "111"

	created, err := m.CreateType(gid, modules.TypeDetail{
		Key: "Support", Label: "Support", Category: "222333444555666777", Enabled: true,
		PingRoles: []string{"9"}, HelperRoles: []string{"8", "8"}, AccessRoles: []string{"7"},
		WelcomeMessage: "hi", EmbedBody: "body",
		ButtonLabel: "Open", ButtonEmoji: "🎫", Color: 0x5865F2,
		AllowClaim: true, AllowClose: true,
	})
	if err != nil {
		t.Fatalf("CreateType: %v", err)
	}
	if created.Key != "support" {
		t.Errorf("created key = %q, want support (normalized)", created.Key)
	}
	if len(created.HelperRoles) != 1 {
		t.Errorf("helper roles = %v, want deduped [8]", created.HelperRoles)
	}
	if got, err := m.GetType(gid, "support"); err != nil || got.Label != "Support" {
		t.Fatalf("GetType = %+v, %v", got, err)
	}
	if list, err := m.ListTypeDetails(gid); err != nil || len(list) != 1 {
		t.Fatalf("ListTypeDetails = %+v, %v", list, err)
	}

	// Duplicate key rejected.
	if _, err := m.CreateType(gid, modules.TypeDetail{Key: "support", Label: "Again", Category: "222333444555666777"}); err == nil {
		t.Fatal("duplicate CreateType should error")
	}

	// Full update replaces fields; the key stays the path key.
	updated, err := m.UpdateType(gid, "support", modules.TypeDetail{
		Label: "Help desk", Category: "333444555666777888", Enabled: false,
		PingRoles: []string{"5"}, AccessRoles: []string{}, Color: 0x00FF00,
		AllowClaim: false, AllowClose: true,
	})
	if err != nil {
		t.Fatalf("UpdateType: %v", err)
	}
	if updated.Key != "support" || updated.Label != "Help desk" || updated.Enabled {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.AllowClaim || !updated.AllowClose {
		t.Errorf("allow flags = claim %v close %v, want false/true", updated.AllowClaim, updated.AllowClose)
	}

	if err := m.DeleteType(gid, "support"); err != nil {
		t.Fatalf("DeleteType: %v", err)
	}
	if list, _ := m.ListTypeDetails(gid); len(list) != 0 {
		t.Fatalf("types after delete = %+v, want empty", list)
	}
	if err := m.DeleteType(gid, "support"); err == nil {
		t.Fatal("second DeleteType should error")
	}
}

// TestAdminTypeValidation pins the editor's server-side validation — every
// invalid payload must be rejected before touching the config.
func TestAdminTypeValidation(t *testing.T) {
	m := adminModule(t, "")
	const gid = "111"
	base := modules.TypeDetail{Key: "ok", Label: "OK", Category: "222333444555666777", AllowClaim: true, AllowClose: true}

	cases := []struct {
		name string
		mut  func(*modules.TypeDetail)
		want string
	}{
		{"bad key", func(d *modules.TypeDetail) { d.Key = "bad key!" }, "type keys"},
		{"empty label", func(d *modules.TypeDetail) { d.Label = " " }, "label"},
		{"bad category", func(d *modules.TypeDetail) { d.Category = "general" }, "category"},
		{"enabled without category", func(d *modules.TypeDetail) { d.Enabled = true; d.Category = "" }, "category"},
		{"bad ping role", func(d *modules.TypeDetail) { d.PingRoles = []string{"nope"} }, "role ID"},
		{"bad color", func(d *modules.TypeDetail) { d.Color = 0x1000000 }, "color"},
		{"long button", func(d *modules.TypeDetail) { d.ButtonLabel = strings.Repeat("x", 81) }, "button label"},
		{"bad emoji", func(d *modules.TypeDetail) { d.ButtonEmoji = "<:broken" }, "emoji"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mut(&in)
			if _, err := m.CreateType(gid, in); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CreateType error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestAdminPanelsCRUD pins panel create/update/action/delete against a REST
// stub: one message per create/resend, in-place edit on update, and message
// deletion on remove.
func TestAdminPanelsCRUD(t *testing.T) {
	var mu sync.Mutex
	var created, deleted int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
			mu.Lock()
			created++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"900","channel_id":"222","content":""}`))
		case r.Method == http.MethodPatch:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"900","channel_id":"222"}`))
		case r.Method == http.MethodDelete:
			mu.Lock()
			deleted++
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	m := adminModule(t, srv.URL)
	const gid = "111"

	if _, err := m.CreateType(gid, modules.TypeDetail{Key: "support", Label: "Support", Category: "333444555666777888", Enabled: true, AllowClaim: true, AllowClose: true}); err != nil {
		t.Fatalf("CreateType: %v", err)
	}

	p, err := m.CreatePanel(gid, modules.PanelInput{
		Name: "p1", ChannelID: "222", TypeKey: "support",
		Title: "Need help?", Description: "Open a ticket",
		Questions: []modules.PanelQuestion{{Label: "Why?"}},
	})
	if err != nil {
		t.Fatalf("CreatePanel: %v", err)
	}
	if p.Name != "p1" || len(p.Questions) != 1 {
		t.Fatalf("created panel = %+v", p)
	}

	// Unknown type is rejected.
	if _, err := m.CreatePanel(gid, modules.PanelInput{Name: "p2", ChannelID: "222", TypeKey: "missing"}); err == nil {
		t.Fatal("CreatePanel with unknown type should error")
	}

	// Update edits in place (same message id).
	updated, err := m.UpdatePanel(gid, "p1", modules.PanelInput{
		Name: "p1", ChannelID: "222", TypeKey: "support", Title: "Updated", ModalTitle: "Details",
	})
	if err != nil {
		t.Fatalf("UpdatePanel: %v", err)
	}
	if updated.Title != "Updated" || updated.ModalTitle != "Details" {
		t.Fatalf("updated panel = %+v", updated)
	}
	if list, _ := m.ListPanels(gid); len(list) != 1 {
		t.Fatalf("panels = %+v, want 1", list)
	}

	// Suspend/resume flip the flag; resend posts a fresh message.
	if p, err := m.PanelAction(gid, "p1", "suspend"); err != nil || !p.Suspended {
		t.Fatalf("suspend = %+v, %v", p, err)
	}
	if p, err := m.PanelAction(gid, "p1", "resume"); err != nil || p.Suspended {
		t.Fatalf("resume = %+v, %v", p, err)
	}
	if _, err := m.PanelAction(gid, "p1", "resend"); err != nil {
		t.Fatalf("resend: %v", err)
	}
	if _, err := m.PanelAction(gid, "p1", "explode"); err == nil {
		t.Fatal("unknown panel action should error")
	}

	if err := m.DeletePanel(gid, "p1"); err != nil {
		t.Fatalf("DeletePanel: %v", err)
	}
	if list, _ := m.ListPanels(gid); len(list) != 0 {
		t.Fatalf("panels after delete = %+v", list)
	}

	mu.Lock()
	gotCreated, gotDeleted := created, deleted
	mu.Unlock()
	if gotCreated != 2 {
		t.Errorf("messages created = %d, want 2 (initial + resend)", gotCreated)
	}
	if gotDeleted != 1 {
		t.Errorf("messages deleted = %d, want 1", gotDeleted)
	}
}

// TestAdminGuildSettings pins the log-channel read/write and its validation.
func TestAdminGuildSettings(t *testing.T) {
	m := adminModule(t, "")
	const gid = "111"

	got, err := m.GetGuildSettings(gid)
	if err != nil || got.LogChannel != "" {
		t.Fatalf("initial settings = %+v, %v", got, err)
	}
	if err := m.SetGuildSettings(gid, modules.TicketGuildSettings{LogChannel: "222333444555666777"}); err != nil {
		t.Fatalf("SetGuildSettings: %v", err)
	}
	if got, _ := m.GetGuildSettings(gid); got.LogChannel != "222333444555666777" {
		t.Fatalf("settings = %+v", got)
	}
	if err := m.SetGuildSettings(gid, modules.TicketGuildSettings{LogChannel: "not-a-channel"}); err == nil {
		t.Fatal("invalid log channel should error")
	}
	// Empty clears.
	if err := m.SetGuildSettings(gid, modules.TicketGuildSettings{}); err != nil {
		t.Fatalf("clear settings: %v", err)
	}
	if got, _ := m.GetGuildSettings(gid); got.LogChannel != "" {
		t.Fatalf("cleared settings = %+v", got)
	}
}

// TestAdminGuards pins the shared ready check: unloaded modules and malformed
// guild IDs must error instead of touching state.
func TestAdminGuards(t *testing.T) {
	unloaded := &TicketsModule{guilds: map[string]*Config{}}
	if _, err := unloaded.ListTypeDetails("111"); err == nil {
		t.Fatal("unloaded ListTypeDetails should error")
	}
	loaded := adminModule(t, "")
	if _, err := loaded.ListTypeDetails("null"); err == nil {
		t.Fatal("invalid guild id should error")
	}
	if _, err := loaded.CreatePanel("111", modules.PanelInput{Name: "p", ChannelID: "222", TypeKey: "support"}); err == nil {
		t.Fatal("panel with unknown type should error")
	}
}
