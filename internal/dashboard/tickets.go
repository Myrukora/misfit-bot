package dashboard

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/misfit/bot/modules"
)

// tickets.go — dashboard ↔ tickets module integration (v3).
//
// Provider resolution: the dashboard never imports the tickets builtin; it
// resolves modules.TicketProvider / modules.TicketAdmin through the manager by
// name.
//
// Routes:
//
//	GET  /tickets                                  page: panels + open list
//	GET  /tickets/<guild>/<ticket>                 transcript viewer
//	GET  /api/tickets?guild=<id>&scope=open|closed JSON lists
//	POST /api/tickets/<guild>/panels/<name>/suspend|resume|resend
//	POST /api/tickets/<guild>/panels/<name>/questions   replace the open-time form
// POST /api/tickets/<guild>/<ticket>/close       same path as in-chat close
// GET  /api/tickets/<guild>/<ticket>/transcript  regenerate + download
//	GET  /api/ticketfiles/<guild>/<ticket>/<name>  mirrored attachment bytes

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

// ticketAdmin resolves the OPTIONAL modules.TicketAdmin (panel questions
// surface). A module that does not implement it simply has no panel surface.
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
	level := m.resolveLevel(us)

	switch {
	// GET /api/tickets?guild=&scope=
	case meth == "GET" && len(parts) == 1:
		guildID := r.URL.Query().Get("guild")
		if guildID == "" || !m.allowed(guildID) {
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
	// POST /api/tickets/<guild>/panels/<name>/questions — replace the panel's
	// open-time question form. MUST be matched BEFORE the panel-action case
	// below (both are len(parts)==5 with parts[2]=="panels").
	case meth == "POST" && len(parts) == 5 && parts[2] == "panels" && parts[4] == "questions":
		guildID, name := parts[1], parts[3]
		if !levelGEQ(level, lvlStaff) || !m.canManageGuild(us, guildID) {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		if !m.checkCSRF(r) {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		if !m.allowed(guildID) {
			writeError(w, http.StatusForbidden, "no access to this guild")
			return
		}
		adm, ok := m.ticketAdmin()
		if !ok {
			writeError(w, http.StatusNotFound, "tickets module does not support panel questions")
			return
		}
		var body struct {
			Questions []modules.PanelQuestion `json:"questions"`
		}
		if err := json.Unmarshal(readAll(r), &body); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid question payload: "+err.Error())
			return
		}
		if err := adm.SetPanelQuestions(guildID, name, body.Questions); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "panel": name})

	// POST /api/tickets/<guild>/panels/<name>/<action> — suspend|resume|resend
	// only (remove stays CLI-only); every one of them is a staff action.
	case meth == "POST" && len(parts) == 5 && parts[2] == "panels" && parts[4] != "questions":
		guildID, name, action := parts[1], parts[3], parts[4]
		if action != "suspend" && action != "resume" && action != "resend" {
			writeError(w, http.StatusBadRequest, "unsupported action")
			return
		}
		if !m.ticketsPanelAction(w, r, guildID, name, action, levelGEQ(level, lvlStaff)) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "panel": name, "action": action})
	// POST /api/tickets/<guild>/<ticket>/close
	case meth == "POST" && len(parts) == 4 && parts[3] == "close":
		guildID, ticketID := parts[1], parts[2]
		canClose := levelGEQ(level, lvlElevated) || m.ticketsDashCloseAllowed()
		if !canClose {
			writeError(w, http.StatusForbidden, "closing tickets from the dashboard requires elevated permissions")
			return
		}
		if !m.checkCSRF(r) {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		if !m.allowed(guildID) {
			writeError(w, http.StatusForbidden, "no access to this guild")
			return
		}
		if err := tp.CloseTicket(guildID, ticketID, us.userID.String()); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "closed": ticketID})

	// GET /api/tickets/<guild>/<ticket>/transcript — regenerate + download.
	// GET is not mutating, so no CSRF; the route is already auth-gated.
	case meth == "GET" && len(parts) == 4 && parts[3] == "transcript":
		guildID, ticketID := parts[1], parts[2]
		if !m.allowed(guildID) {
			writeError(w, http.StatusForbidden, "no access to this guild")
			return
		}
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

// ticketsPanelAction performs one panel mutation through the universal
// web-exec pipeline — the SAME code path and validation as [p]tickets panel ….
func (m *DashboardModule) ticketsPanelAction(w http.ResponseWriter, r *http.Request, guildID, name, action string, allowed bool) bool {
	if !allowed {
		writeError(w, http.StatusForbidden, "insufficient permissions")
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
	us := sessionOf(r)
	if us == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return false
	}
	res, err := m.bot.ExecuteCommand("tickets",
		[]string{"panel", action, name}, guildID, "", us.userID.String(), m.execMode())
	if err != nil {
		code := http.StatusBadRequest
		if strings.Contains(err.Error(), "unknown panel") || strings.Contains(err.Error(), "Unknown") {
			code = http.StatusNotFound
		} else if strings.Contains(err.Error(), "permission") || strings.Contains(err.Error(), "forbidden") {
			code = http.StatusForbidden
		}
		writeError(w, code, err.Error())
		return false
	}
	_ = res
	return true
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

	d := m.baseData(us)
	d.Page = "tickets"
	d.Level = level
	guildID := r.URL.Query().Get("guild")
	if guildID == "" && len(d.Guilds) > 0 {
		guildID = d.Guilds[0].ID
	}
	if guildID != "" && !m.allowed(guildID) {
		http.Error(w, "403 no access to this guild", http.StatusForbidden)
		return
	}
	m.renderTicketsList(w, us, level, guildID, false)
}

// handleTicketsInGuild renders the tickets page pinned to one guild
// (server-scoped /g/<id>/tickets route).
func (m *DashboardModule) handleTicketsInGuild(w http.ResponseWriter, r *http.Request, us *userSession, guildID string) {
	level := m.resolveLevel(us)
	m.renderTicketsList(w, us, level, guildID, true)
}

// renderTicketsList builds the tickets payload for a fixed guild and renders
// the list page. Shared by /tickets?guild= (top-level, global sidebar) and
// /g/<id>/tickets (server-scoped, scoped sidebar when scoped=true).
func (m *DashboardModule) renderTicketsList(w http.ResponseWriter, us *userSession, level, guildID string, scoped bool) {
	d := m.baseData(us)
	d.Page = "tickets"
	if scoped {
		d.Page = "gtickets"
	}
	d.Level = level
	if scoped {
		d.GuildID = guildID
		d.GuildName = m.guildDisplayName(guildID, us)
	}
	if guildID != "" && !m.allowed(guildID) {
		http.Error(w, "403 no access to this guild", http.StatusForbidden)
		return
	}

	payload := struct {
		GuildID string
		Open    any
		Closed  any
		Types   any
		Panels  any
		Error   string
	}{GuildID: guildID}
	if tp, ok := m.ticketProvider(); ok && guildID != "" {
		open, err := tp.ListOpenTickets(guildID)
		if err != nil {
			payload.Error = err.Error()
		} else {
			payload.Open = open
			payload.Closed, _ = tp.ListClosedTickets(guildID)
			payload.Types, _ = tp.ListTypes(guildID)
			if adm, ok := m.ticketAdmin(); ok {
				if panels, err := adm.ListPanels(guildID); err == nil {
					payload.Panels = panels
				}
			}
		}
	} else if !ok {
		payload.Error = "tickets module is not loaded"
	}
	// The template reads the whole payload off .Content — without this the
	// page renders nothing (rd_tickets' first action is .Content.Error).
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
