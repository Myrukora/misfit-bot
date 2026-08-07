package main

import (
	"net/http"
	"strings"
	"time"
)

// buildHandler assembles the global middleware chain and route table.
// Order: panic-recovery → request-log → security-headers → auth(cookie) → router.
// Panic recovery is mandatory: the dashboard runs in-process with the gateway,
// so an unrecovered panic would crash the whole bot.
func (m *DashboardModule) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSubFS()))))
	mux.HandleFunc("/", m.route)

	var h http.Handler = mux
	h = m.authMiddleware(h)
	h = m.securityHeaders(h)
	h = m.logMiddleware(h)
	h = m.recoverMiddleware(h)
	return h
}

// recoverMiddleware turns handler panics into 500s — the dashboard runs in-process, so a panic must never escape.
func (m *DashboardModule) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				m.logger.Error("Dashboard panic recovered: %v", rec)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// logMiddleware logs one line per request with status and duration.
func (m *DashboardModule) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		uid := ""
		if us := sessionOf(r); us != nil {
			uid = us.userID.String()
		}
		m.logger.Info("dashboard %s %s -> %d (%s) user=%s", r.Method, r.URL.Path, sw.status, time.Since(start), uid)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status code before delegating to the wrapped writer.
func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// securityHeaders sets conservative security headers (CSP, nosniff, frame denial).
func (m *DashboardModule) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https://cdn.discordapp.com data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

// route dispatches all non-static requests. API lives under /api, pages at the
// root, OAuth flow at /login,/callback,/logout.
func (m *DashboardModule) route(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" || path == "" {
		m.requireAuthed(m.handleIndex)(w, r)
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch parts[0] {
	case "login":
		if r.Method == "GET" {
			m.handleLoginPage(w, r)
		} else {
			m.handleLoginStart(w, r)
		}
		return
	case "logout":
		m.handleLogout(w, r)
		return
	case "callback":
		m.handleCallback(w, r)
		return
	case "commands":
		if r.Method == "GET" {
			m.requireAuthed(m.handleCommandsPage)(w, r)
		}
		return
	case "guild":
		if len(parts) < 2 {
			http.NotFound(w, r)
			return
		}
		id := parts[1]
		if r.Method == "GET" {
			m.requireGuild(id, func(w http.ResponseWriter, r *http.Request) { m.handleGuildPage(w, r, id) })(w, r)
		}
		return
	case "modules":
		if r.Method == "GET" {
			m.requireOwner(m.handleModulesPage)(w, r)
		}
		return
	case "settings":
		if r.Method == "GET" {
			m.handleSettingsPage(w, r)
		}
		return
	case "permissions":
		if r.Method == "GET" {
			m.requireOwner(m.handlePermissionsPage)(w, r)
		}
		return
	case "logs":
		if r.Method == "GET" {
			m.requireOwner(m.handleLogsPage)(w, r)
		}
		return
	case "api":
		m.routeAPI(w, r, parts[1:])
		return
	}
	http.NotFound(w, r)
}

// baseData builds the renderData shared by all pages (user, level, guilds, nav
// flags, CSRF). Pass the page-specific payload via Content separately.
func (m *DashboardModule) baseData(us *userSession) renderData {
	id := m.botIdentity()
	d := renderData{Bot: id.Name, BotAvatar: id.Avatar, User: nil, Level: lvlRegular, Guilds: []guildOpt{}, CSRF: "", ShowSidebar: true}
	if us != nil {
		level := m.resolveLevel(us)
		var guilds []guildOpt
		for _, id := range m.manageableGuildIDs(us) {
			if g := m.guildSummary(id, us); g != nil {
				guilds = append(guilds, *g)
			}
		}
		d.User = &userJSON{ID: us.userID.String(), Username: us.username, Avatar: us.avatar}
		d.Level = level
		d.Guilds = guilds
		d.CSRF = us.csrfToken
		d.IsOwner = level == lvlOwner
		d.IsElevated = level == lvlElevated
		d.IsStaff = level == lvlStaff
		d.IsRegular = level == lvlRegular
		// Owner/elevated see core/global config nav. Staff still see guild nav.
		d.ShowConfig = level == lvlOwner || level == lvlElevated
		d.ShowStaff = level == lvlOwner || level == lvlElevated || level == lvlStaff
	}
	return d
}
