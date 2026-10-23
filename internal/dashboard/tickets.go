package dashboard

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/misfit/bot/modules"
)

// tickets.go — dashboard ↔ tickets module integration (v4, dashboard-managed).
//
// The dashboard is the ONLY management surface: ticket types, panels and the
// per-guild log channel are edited here through modules.TicketAdmin. The
// module's only commands are the in-channel end-user actions
// (close/rename/add/remove) — the [p]tickets / /tickets configuration tree
// was removed.
//
// Live updates: GET /api/tickets/<guild>/stream and
// /api/tickets/<guild>/<ticket>/stream are SSE endpoints that poll the
// in-memory store and push re-rendered fragments, so the ticket list and the
// transcript viewer update without a page reload.
//
// Routes:
//
//	GET  /tickets                                   redirects → /g/<guild>/tickets
//	GET  /tickets/<guild>/<ticket>                  transcript viewer
//	GET  /api/tickets?guild=<id>&scope=open|closed  JSON lists
//	POST /api/tickets/<guild>/settings              log channel
//	GET  /api/tickets/<guild>/types                 type details
//	GET  /api/tickets/<guild>/types/<key>           one type (editor)
//	POST /api/tickets/<guild>/types                 create
//	POST /api/tickets/<guild>/types/<key>           update
//	POST /api/tickets/<guild>/types/<key>/delete    delete
//	GET  /api/tickets/<guild>/panels                panel list
//	GET  /api/tickets/<guild>/panels/<name>         one panel (editor)
//	POST /api/tickets/<guild>/panels                create
//	POST /api/tickets/<guild>/panels/<name>         update
//	POST /api/tickets/<guild>/panels/<name>/delete  delete
//	POST /api/tickets/<guild>/panels/<name>/suspend|resume|resend
//	POST /api/tickets/<guild>/<ticket>/close        same path as in-chat close
//	GET  /api/tickets/<guild>/<ticket>/transcript   regenerate + download
//	GET  /api/ticketfiles/<guild>/<ticket>/<name>   mirrored attachment bytes

func (m *DashboardModule) ticketProvider() (modules.TicketProvider, bool) {
	if m.bot == nil {
		return nil, false
	}
	getter, ok := m.bot.GetModuleManager().(interface {
		Get(string) (modules.Module, bool)
	})
	if !ok {
		return nil, false
	}
	mod, ok := getter.Get("tickets")
	if !ok {
		return nil, false
	}
	tp, ok := mod.(modules.TicketProvider)
	return tp, ok
}

// ticketAdmin resolves the OPTIONAL modules.TicketAdmin (management surface).
// A module that does not implement it has no management UI.
func (m *DashboardModule) ticketAdmin() (modules.TicketAdmin, bool) {
	if m.bot == nil {
		return nil, false
	}
	getter, ok := m.bot.GetModuleManager().(interface {
		Get(string) (modules.Module, bool)
	})
	if !ok {
		return nil, false
	}
	mod, ok := getter.Get("tickets")
	if !ok {
		return nil, false
	}
	adm, ok := mod.(modules.TicketAdmin)
	return adm, ok
}

// ticketTranscript resolves the OPTIONAL modules.TicketTranscript (the
// transcript download surface). A module that does not implement it has no
// transcript download: the route answers 404.
func (m *DashboardModule) ticketTranscript() (modules.TicketTranscript, bool) {
	if m.bot == nil {
		return nil, false
	}
	getter, ok := m.bot.GetModuleManager().(interface {
		Get(string) (modules.Module, bool)
	})
	if !ok {
		return nil, false
	}
	mod, ok := getter.Get("tickets")
	if !ok {
		return nil, false
	}
	tt, ok := mod.(modules.TicketTranscript)
	return tt, ok
}

// ── API ──────────────────────────────────────────────────────────────────

func (m *DashboardModule) routeTicketsAPI(w http.ResponseWriter, r *http.Request, meth string, parts []string) {
	us := sessionOf(r)
	if us == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	tp, ok := m.ticketProvider()
	if !ok {
		writeError(w, http.StatusNotFound, "tickets module is not loaded")
		return
	}

	// GET /api/tickets?guild=&scope= — ticket lists.
	if meth == "GET" && len(parts) == 1 {
		guildID := r.URL.Query().Get("guild")
		if guildID == "" || !m.allowed(guildID) || !m.canManageGuild(us, guildID) {
			writeError(w, http.StatusBadRequest, "guild parameter required")
			return
		}
		scope := r.URL.Query().Get("scope")
		var (
			tickets any
			err     error
		)
		if scope == "closed" {
			tickets, err = tp.ListClosedTickets(guildID)
		} else {
			tickets, err = tp.ListOpenTickets(guildID)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		types, _ := tp.ListTypes(guildID)
		writeJSON(w, http.StatusOK, map[string]any{"tickets": tickets, "types": types})
		return
	}

	// Everything below is guild-scoped: /api/tickets/<guild>/...
	if len(parts) < 3 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !m.allowed(parts[1]) {
		writeError(w, http.StatusForbidden, "no access to this guild")
		return
	}
	guildID := parts[1]
	adm, hasAdmin := m.ticketAdmin()

	switch parts[2] {
	case "stream":
		if meth != "GET" || len(parts) != 3 {
			writeError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		if !m.canManageGuild(us, guildID) {
			writeError(w, http.StatusForbidden, "guild management required")
			return
		}
		m.serveTicketListStream(w, r, guildID, tp)
		return

	case "settings":
		if !hasAdmin {
			writeError(w, http.StatusNotFound, "tickets module has no admin surface")
			return
		}
		if meth != "POST" || len(parts) != 3 {
			writeError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		if !m.ticketWriteAllowed(w, r, guildID) {
			return
		}
		var in modules.TicketGuildSettings
		if err := json.Unmarshal(readAll(r), &in); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid payload: "+err.Error())
			return
		}
		if err := adm.SetGuildSettings(guildID, in); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "types":
		m.routeTicketTypes(w, r, meth, parts, guildID, us, adm, hasAdmin)

	case "panels":
		m.routeTicketPanels(w, r, meth, parts, guildID, us, adm, hasAdmin)

	default:
		// /api/tickets/<guild>/<ticket>/close|transcript
		switch {
		case meth == "POST" && len(parts) == 4 && parts[3] == "close":
			guildID, ticketID := parts[1], parts[2]
			canClose := levelGEQ(m.resolveLevel(us), lvlElevated) || m.ticketsDashCloseAllowed()
			if !canClose {
				writeError(w, http.StatusForbidden, "closing tickets from the dashboard requires elevated permissions")
				return
			}
			if !m.checkCSRF(r) {
				writeError(w, http.StatusForbidden, "invalid CSRF token")
				return
			}
			if err := tp.CloseTicket(guildID, ticketID, us.userID.String()); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "closed": ticketID})

		case meth == "GET" && len(parts) == 4 && parts[3] == "stream":
			guildID, ticketID := parts[1], parts[2]
			if !m.canManageGuild(us, guildID) {
				writeError(w, http.StatusForbidden, "guild management required")
				return
			}
			m.serveTranscriptStream(w, r, guildID, ticketID, tp)
			return

		case meth == "GET" && len(parts) == 4 && parts[3] == "transcript":
			guildID, ticketID := parts[1], parts[2]
			if tk, err := tp.GetTicket(guildID, ticketID); err != nil || tk == nil {
				writeError(w, http.StatusNotFound, "ticket not found")
				return
			}
			tt, ok := m.ticketTranscript()
			if !ok {
				writeError(w, http.StatusNotFound, "tickets module does not support transcript download")
				return
			}
			data, err := tt.RefreshTranscript(guildID, ticketID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="ticket-`+ticketID+`.html"`)
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			w.Write(data)

		default:
			writeError(w, http.StatusNotFound, "not found")
		}
	}
}

// ticketWriteAllowed enforces the management gate for ticket config writes:
// managing this guild + CSRF + the dashboard's guild allowlist.
func (m *DashboardModule) ticketWriteAllowed(w http.ResponseWriter, r *http.Request, guildID string) bool {
	if !m.canManageGuild(sessionOf(r), guildID) {
		writeError(w, http.StatusForbidden, "you may not manage this guild")
		return false
	}
	if !m.checkCSRF(r) {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return false
	}
	if !m.allowed(guildID) {
		writeError(w, http.StatusForbidden, "no access to this guild")
		return false
	}
	return true
}

// routeTicketTypes handles /api/tickets/<guild>/types[/<key>[/delete]].
func (m *DashboardModule) routeTicketTypes(w http.ResponseWriter, r *http.Request, meth string, parts []string, guildID string, us *userSession, adm modules.TicketAdmin, hasAdmin bool) {
	if !hasAdmin {
		writeError(w, http.StatusNotFound, "tickets module has no admin surface")
		return
	}
	if !m.canManageGuild(us, guildID) {
		writeError(w, http.StatusForbidden, "you may not manage this guild")
		return
	}
	switch {
	case meth == "GET" && len(parts) == 3:
		types, err := adm.ListTypeDetails(guildID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"types": types})

	case meth == "GET" && len(parts) == 4:
		t, err := adm.GetType(guildID, parts[3])
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, t)

	case meth == "POST" && len(parts) == 3:
		if !m.ticketWriteAllowed(w, r, guildID) {
			return
		}
		var in modules.TypeDetail
		if err := json.Unmarshal(readAll(r), &in); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid payload: "+err.Error())
			return
		}
		t, err := adm.CreateType(guildID, in)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, t)

	case meth == "POST" && len(parts) == 4:
		if !m.ticketWriteAllowed(w, r, guildID) {
			return
		}
		var in modules.TypeDetail
		if err := json.Unmarshal(readAll(r), &in); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid payload: "+err.Error())
			return
		}
		t, err := adm.UpdateType(guildID, parts[3], in)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, t)

	case meth == "POST" && len(parts) == 5 && parts[4] == "delete":
		if !m.ticketWriteAllowed(w, r, guildID) {
			return
		}
		if err := adm.DeleteType(guildID, parts[3]); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": parts[3]})

	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// routeTicketPanels handles /api/tickets/<guild>/panels[/<name>[/action]].
func (m *DashboardModule) routeTicketPanels(w http.ResponseWriter, r *http.Request, meth string, parts []string, guildID string, us *userSession, adm modules.TicketAdmin, hasAdmin bool) {
	if !hasAdmin {
		writeError(w, http.StatusNotFound, "tickets module has no admin surface")
		return
	}
	if !m.canManageGuild(us, guildID) {
		writeError(w, http.StatusForbidden, "you may not manage this guild")
		return
	}
	switch {
	case meth == "GET" && len(parts) == 3:
		panels, err := adm.ListPanels(guildID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"panels": panels})

	case meth == "GET" && len(parts) == 4:
		p, err := adm.GetPanel(guildID, parts[3])
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, p)

	case meth == "POST" && len(parts) == 3:
		if !m.ticketWriteAllowed(w, r, guildID) {
			return
		}
		var in modules.PanelInput
		if err := json.Unmarshal(readAll(r), &in); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid payload: "+err.Error())
			return
		}
		p, err := adm.CreatePanel(guildID, in)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, p)

	case meth == "POST" && len(parts) == 4:
		if !m.ticketWriteAllowed(w, r, guildID) {
			return
		}
		var in modules.PanelInput
		if err := json.Unmarshal(readAll(r), &in); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid payload: "+err.Error())
			return
		}
		p, err := adm.UpdatePanel(guildID, parts[3], in)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, p)

	case meth == "POST" && len(parts) == 5:
		if !m.ticketWriteAllowed(w, r, guildID) {
			return
		}
		name, action := parts[3], parts[4]
		if action == "delete" {
			if err := adm.DeletePanel(guildID, name); err != nil {
				writeError(w, http.StatusUnprocessableEntity, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": name})
			return
		}
		if action != "suspend" && action != "resume" && action != "resend" {
			writeError(w, http.StatusBadRequest, "unsupported action")
			return
		}
		p, err := adm.PanelAction(guildID, name, action)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "panel": p})

	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// ticketsDashCloseAllowed reads the module's allow_dashboard_close flag.
func (m *DashboardModule) ticketsDashCloseAllowed() bool {
	getter, ok := m.bot.GetModuleManager().(interface {
		Get(string) (modules.Module, bool)
	})
	if !ok {
		return false
	}
	mod, ok := getter.Get("tickets")
	if !ok {
		return false
	}
	wc, ok := mod.(modules.WebConfigurable)
	if !ok {
		return false
	}
	vals, err := wc.WebGetConfig("")
	if err != nil {
		return false
	}
	return vals["allow_dashboard_close"] == "true"
}

// serveTicketFile serves mirrored attachment files for the transcript viewer:
// GET /api/ticketfiles/<guild>/<ticket>/<name> — auth-gated; path validation
// (ticket id + file name, no traversal) lives in the module behind
// TicketProvider.TicketFilePath, which also proves the file exists.
func (m *DashboardModule) serveTicketFile(w http.ResponseWriter, r *http.Request, guildID, ticketID, filename string) {
	us, _, ok := m.sessionFromCookie(r)
	if !ok || us == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if !m.allowed(guildID) {
		http.Error(w, "403", http.StatusForbidden)
		return
	}
	tp, ok := m.ticketProvider()
	if !ok {
		http.NotFound(w, r)
		return
	}
	tk, err := tp.GetTicket(guildID, ticketID)
	if err != nil || tk == nil {
		http.NotFound(w, r)
		return
	}
	full, err := tp.TicketFilePath(guildID, ticketID, filename)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, filename, st.ModTime(), f)
}

// ── Page handlers ─────────────────────────────────────────────────────────

// handleTicketsPage routes the /tickets prefix: /tickets/<guild>/<ticket>
// is the stable standalone transcript deep link. The bare /tickets list is
// per-server by design, so it redirects to the server-scoped page
// (/g/<guild>/tickets) — the hub carries no tickets surface of its own.
func (m *DashboardModule) handleTicketsPage(w http.ResponseWriter, r *http.Request) {
	us, _, ok := m.sessionFromCookie(r)
	if !ok || us == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	level := m.resolveLevel(us)
	path := strings.TrimPrefix(r.URL.Path, "/tickets")
	path = strings.Trim(path, "/")

	if path != "" {
		segs := strings.Split(path, "/")
		if len(segs) == 2 {
			m.handleTranscriptPage(w, r, us, level, segs[0], segs[1])
			return
		}
		http.NotFound(w, r)
		return
	}

	// Bare /tickets: jump to the per-server tickets page. ?guild= keeps old
	// links (e.g. "back to tickets" from a transcript) on their guild; without
	// one, fall back to the first guild the user can manage.
	guildID := r.URL.Query().Get("guild")
	if guildID == "" {
		if guilds := m.manageableGuildIDs(us); len(guilds) > 0 {
			guildID = guilds[0]
		}
	}
	if guildID == "" || !m.canManageGuild(us, guildID) {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/g/"+guildID+"/tickets", http.StatusSeeOther)
}

// ticketsPageData is the per-server Tickets page payload: the ticket lists,
// the dashboard-managed configuration (types, panels, settings) and the
// channel/role pickers for the editors.
type ticketsPageData struct {
	GuildID      string
	Open         any
	Closed       any
	TypeDetails  []modules.TypeDetail
	Panels       []modules.PanelSummary
	Settings     modules.TicketGuildSettings
	Channels     []entityOpt       // text/announcement: panel + log targets
	Categories   []entityOpt       // category channels (type category)
	Roles        []entityOpt       // role pickers + role-name display
	ChannelNames map[string]string // channel ID → name for table rows
	People       map[string]ticketPerson
	Error        string
}

// ticketPerson is the display identity of a ticket opener, resolved from the
// guild member cache (empty when the member is not cached).
type ticketPerson struct {
	Name   string
	Avatar string
}

// ticketPeople resolves opener identities for the list rows: cached members
// only, so a page render never fans out into REST calls.
func (m *DashboardModule) ticketPeople(guildID string, lists ...[]modules.TicketSummary) map[string]ticketPerson {
	out := map[string]ticketPerson{}
	if m.bot == nil {
		return out
	}
	for _, list := range lists {
		for _, t := range list {
			id := t.OpenerID
			if id == "" {
				continue
			}
			if _, ok := out[id]; ok {
				continue
			}
			p := ticketPerson{}
			if mem := m.bot.GetCachedMember(guildID, id); mem != nil {
				p.Name = mem.User.EffectiveName()
				p.Avatar = mem.User.EffectiveAvatarURL()
			}
			out[id] = p
		}
	}
	return out
}

// renderTicketsList builds the tickets payload for one guild and renders the
// server-scoped list page (/g/<id>/tickets, scoped sidebar).
func (m *DashboardModule) renderTicketsList(w http.ResponseWriter, us *userSession, level, guildID string) {
	d := m.baseData(us)
	d.Page = "gtickets"
	d.Level = level
	d.GuildID = guildID
	d.GuildName = m.guildDisplayName(guildID, us)
	if !m.allowed(guildID) {
		http.Error(w, "403 no access to this guild", http.StatusForbidden)
		return
	}

	payload := ticketsPageData{GuildID: guildID}
	if tp, ok := m.ticketProvider(); ok {
		open, err := tp.ListOpenTickets(guildID)
		if err != nil {
			payload.Error = err.Error()
		} else {
			payload.Open = open
			closed, _ := tp.ListClosedTickets(guildID)
			payload.Closed = closed
			payload.People = m.ticketPeople(guildID, open, closed)
			if adm, ok := m.ticketAdmin(); ok {
				payload.Panels, _ = adm.ListPanels(guildID)
				payload.TypeDetails, _ = adm.ListTypeDetails(guildID)
				payload.Settings, _ = adm.GetGuildSettings(guildID)
			}
		}
	} else {
		payload.Error = "tickets module is not loaded"
	}

	// Entity pickers for the type/panel/settings editors. Staff managing a
	// guild are members of it, so canViewGuildEntities is normally true; when
	// it isn't (e.g. an elevated user outside the guild) the editors still
	// work for the panels/types that already exist, just without name lists.
	if m.canViewGuildEntities(us, guildID) {
		if detail, err := m.buildGuildDetail(guildID); err == nil {
			payload.ChannelNames = make(map[string]string, len(detail.Channels))
			for _, ch := range detail.Channels {
				payload.ChannelNames[ch.ID] = ch.Name
				switch ch.Type {
				case "Text", "Announcement":
					payload.Channels = append(payload.Channels, ch)
				case "Category":
					payload.Categories = append(payload.Categories, ch)
				}
			}
			payload.Roles = make([]entityOpt, 0, len(detail.Roles))
			for _, ro := range detail.Roles {
				payload.Roles = append(payload.Roles, entityOpt{ID: ro.ID, Name: ro.Name})
			}
		}
	}
	d.Content = payload
	m.tmpl.render(w, "rd_tickets", d)
}

func (m *DashboardModule) handleTranscriptPage(w http.ResponseWriter, r *http.Request, us *userSession, level string, guildID, ticketID string) {
	if !m.allowed(guildID) {
		http.Error(w, "403 no access to this guild", http.StatusForbidden)
		return
	}
	tp, ok := m.ticketProvider()
	if !ok {
		http.NotFound(w, r)
		return
	}
	tk, err := tp.GetTicket(guildID, ticketID)
	if err != nil || tk == nil {
		http.NotFound(w, r)
		return
	}
	d := m.baseData(us)
	d.ShowSidebar = false // transcript is a standalone, printable page
	d.Page = "transcript"
	d.Level = level
	d.Content = struct {
		Ticket   any
		GuildID  string
		CloseURL string
	}{Ticket: tk, GuildID: guildID,
		CloseURL: "/api/tickets/" + url.PathEscape(guildID) + "/" + url.PathEscape(ticketID) + "/close"}
	m.tmpl.render(w, "rd_transcript", d)
}

// ── live updates (SSE) ───────────────────────────────────────────────────

// sseStart switches the response to text/event-stream and flushes the headers
// so the browser opens the EventSource immediately.
func sseStart(w http.ResponseWriter) (http.Flusher, bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	return fl, true
}

// writeSSE sends one JSON frame. The payload is marshalled, so the data line
// never contains a raw newline (the HTML fragments are JSON strings).
func writeSSE(w http.ResponseWriter, fl http.Flusher, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	fl.Flush()
}

// serveTicketListStream pushes re-rendered open/closed lists whenever the
// provider's snapshot changes (new ticket, close, delete).
func (m *DashboardModule) serveTicketListStream(w http.ResponseWriter, r *http.Request, guildID string, tp modules.TicketProvider) {
	fl, ok := sseStart(w)
	if !ok {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	last := ""
	for {
		open, err := tp.ListOpenTickets(guildID)
		if err == nil {
			closed, _ := tp.ListClosedTickets(guildID)
			sig := ticketListSig(open, closed)
			if sig != last {
				data := ticketsPageData{
					GuildID: guildID, Open: open, Closed: closed,
					People: m.ticketPeople(guildID, open, closed),
				}
				openHTML, errOpen := m.tmpl.fragment("tk_open_list", data)
				closedHTML, errClosed := m.tmpl.fragment("tk_closed_list", data)
				if errOpen == nil && errClosed == nil {
					last = sig
					writeSSE(w, fl, "tickets", map[string]string{"open": openHTML, "closed": closedHTML})
				}
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func ticketListSig(open, closed []modules.TicketSummary) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "o%d|c%d;", len(open), len(closed))
	for _, t := range open {
		fmt.Fprintf(h, "%s|%s|%d|%d;", t.ID, t.Status, t.OpenedAt.Unix(), len(t.Preview))
	}
	for _, t := range closed {
		fmt.Fprintf(h, "%s|%d|%d;", t.ID, t.ClosedAt.Unix(), len(t.Preview))
	}
	return fmt.Sprintf("%x", h.Sum64())
}

// serveTranscriptStream pushes the re-rendered conversation whenever the
// ticket's log or status changes (new message, edit, delete, close).
func (m *DashboardModule) serveTranscriptStream(w http.ResponseWriter, r *http.Request, guildID, ticketID string, tp modules.TicketProvider) {
	fl, ok := sseStart(w)
	if !ok {
		return
	}
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	last := ""
	for {
		tk, err := tp.GetTicket(guildID, ticketID)
		if err == nil && tk != nil {
			sig := ticketLogSig(tk)
			if sig != last {
				html, ferr := m.tmpl.fragment("tk_conversation", map[string]any{"Ticket": tk, "GuildID": guildID})
				if ferr == nil {
					last = sig
					writeSSE(w, fl, "messages", map[string]any{
						"html": html, "count": len(tk.Log), "status": tk.Status,
					})
				}
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func ticketLogSig(tk *modules.Ticket) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s|%d|", tk.Status, len(tk.Log))
	for _, e := range tk.Log {
		fmt.Fprintf(h, "%s|%s|%t%t|", e.MsgID, e.Content, e.Edited, e.Deleted)
	}
	return fmt.Sprintf("%x", h.Sum64())
}
