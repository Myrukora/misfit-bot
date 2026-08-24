package main

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/misfit/bot/modules"
)

// ── TicketProvider resolution ─────────────────────────────────────────────
// The dashboard never imports the tickets plugin; it resolves the provider
// through the module manager by name and type-asserts the contract interface.

func (m *DashboardModule) ticketProvider() (modules.TicketProvider, bool) {
	if m.ctx == nil || m.ctx.Bot == nil {
		return nil, false
	}
	getter, ok := m.ctx.Bot.GetModuleManager().(interface {
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

// ── API: /api/tickets/... ────────────────────────────────────────────────

// routeTicketsAPI dispatches:
//
//	GET  /api/tickets?guild=<id>            → open tickets + groups (staff+)
//	GET  /api/tickets/<guild>/<ticket>      → one ticket JSON (staff+)
//	POST /api/tickets/<guild>/<ticket>/close → close (owner/elevated or flag)
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
	case meth == "GET" && len(parts) == 1:
		guildID := r.URL.Query().Get("guild")
		if guildID == "" || !m.allowed(guildID) {
			writeError(w, http.StatusBadRequest, "guild parameter required")
			return
		}
		tickets, err := tp.ListOpenTickets(guildID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		groups, _ := tp.ListGroups(guildID)
		writeJSON(w, http.StatusOK, map[string]any{"tickets": tickets, "groups": groups})

	case meth == "GET" && len(parts) == 3:
		guildID, ticketID := parts[1], parts[2]
		if !m.allowed(guildID) {
			writeError(w, http.StatusForbidden, "no access to this guild")
			return
		}
		tk, err := tp.GetTicket(guildID, ticketID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if tk == nil {
			writeError(w, http.StatusNotFound, "ticket not found")
			return
		}
		writeJSON(w, http.StatusOK, tk)

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

	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// ticketsDashCloseAllowed reads the tickets module's allow_dashboard_close
// flag through its WebConfigurable surface (never panics if absent).
func (m *DashboardModule) ticketsDashCloseAllowed() bool {
	getter, ok := m.ctx.Bot.GetModuleManager().(interface {
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

// ── Page handlers ─────────────────────────────────────────────────────────

// handleTicketsPage renders the open-tickets list (staff+). Transcript view
// is reached at /tickets/<guildID>/<ticketID>.
func (m *DashboardModule) handleTicketsPage(w http.ResponseWriter, r *http.Request) {
	us, _, ok := m.sessionFromCookie(r)
	if !ok || us == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	level := m.resolveLevel(us)
	path := strings.TrimPrefix(r.URL.Path, "/tickets")
	path = strings.Trim(path, "/")

	// Transcript view.
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
	// Same access control as routeTicketsAPI/handleTranscriptPage: never leak
	// another guild's ticket metadata to an authenticated but unauthorized user.
	if guildID != "" && !m.allowed(guildID) {
		http.Error(w, "403 no access to this guild", http.StatusForbidden)
		return
	}

	payload := struct {
		GuildID string
		Tickets any
		Groups  any
		Error   string
	}{GuildID: guildID}
	if tp, ok := m.ticketProvider(); ok && guildID != "" {
		tickets, err := tp.ListOpenTickets(guildID)
		if err != nil {
			payload.Error = err.Error()
		} else {
			payload.Tickets = tickets
			payload.Groups, _ = tp.ListGroups(guildID)
		}
	} else if !ok {
		payload.Error = "tickets module is not loaded"
	}
	d.Content = payload
	m.tmpl.render(w, "tickets", d)
}

// handleTranscriptPage renders one ticket's full conversation log.
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
	d.Page = "tickets"
	d.Level = level
	d.Content = struct {
		Ticket   any
		GuildID  string
		CloseURL string
	}{Ticket: tk, GuildID: guildID,
		CloseURL: "/api/tickets/" + url.PathEscape(guildID) + "/" + url.PathEscape(ticketID) + "/close"}
	m.tmpl.render(w, "transcript", d)
}
