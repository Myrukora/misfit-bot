package dashboard

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/misfit/bot/modules"
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
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https://cdn.discordapp.com https://media.discordapp.net data:; media-src 'self' https://cdn.discordapp.com https://media.discordapp.net; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

// route dispatches all non-static requests. API lives under /api, pages at the
// root, OAuth flow at /login,/callback,/logout.
func (m *DashboardModule) route(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" || path == "" {
		if r.Method == "GET" {
			m.requireAuthed(m.handleServersPage)(w, r)
			return
		}
		methodNotAllowed(w)
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
	case "overview":
		if r.Method == "GET" {
			m.requireOwner(m.handleIndex)(w, r)
			return
		}
		methodNotAllowed(w)
		return
	case "admin":
		// Super-owner only: bot-wide administration (identity, logging,
		// dashboard infra, updater, secrets, backups). resolveLevel returns
		// lvlOwner only for config owner_id — exactly the "super owner".
		if r.Method == "GET" {
			m.requireOwner(m.handleAdminPage)(w, r)
			return
		}
		methodNotAllowed(w)
		return
	case "commands":
		if r.Method == "GET" {
			m.requireAuthed(m.handleCommandsPage)(w, r)
			return
		}
		methodNotAllowed(w)
		return
	case "g":
		// Server-scoped dashboard: /g/<id> (→ /g/<id>/commands), /g/<id>/commands,
		// /g/<id>/tickets, /g/<id>/modules, … Everything under this prefix is
		// scoped to ONE guild; core config and bot-wide pages are NOT reachable
		// here.
		if r.Method != "GET" {
			methodNotAllowed(w)
			return
		}
		if len(parts) < 2 {
			http.NotFound(w, r)
			return
		}
		gid := parts[1]
		guarded := m.requireGuild(gid, func(w http.ResponseWriter, r *http.Request) {
			if len(parts) == 2 {
				// /g/<id> → the first per-server page (no dedicated home page).
				http.Redirect(w, r, "/g/"+gid+"/commands", http.StatusSeeOther)
				return
			}
			m.handleGuildScopedPage(w, r, gid, parts[2])
		})
		guarded(w, r)
		return
	case "guild":
		if len(parts) < 2 {
			http.NotFound(w, r)
			return
		}
		id := parts[1]
		if r.Method == "GET" {
			m.requireGuild(id, func(w http.ResponseWriter, r *http.Request) { m.handleGuildPage(w, r, id) })(w, r)
			return
		}
		methodNotAllowed(w)
		return
	case "modules":
		if r.Method == "GET" {
			m.requireOwner(m.handleModulesPage)(w, r)
			return
		}
		methodNotAllowed(w)
		return
	case "settings":
		// Task 11c: the Configuration tab is the new home for core config;
		// /settings redirects (module settings moved to per-module sections).
		if r.Method == "GET" {
			m.requireAuthed(func(w http.ResponseWriter, r *http.Request) {
				// Preserve the selected guild so old /settings?guild=<id>
				// links keep targeting the same server.
				target := "/configuration"
				if g := r.URL.Query().Get("guild"); g != "" {
					target += "?guild=" + url.QueryEscape(g)
				}
				http.Redirect(w, r, target, http.StatusSeeOther)
			})(w, r)
			return
		}
		methodNotAllowed(w)
		return
	case "configuration":
		// Superseded by /admin (super owner) and per-server module pages;
		// kept as a redirect so old links still land somewhere sensible.
		if r.Method == "GET" {
			m.requireAuthed(func(w http.ResponseWriter, r *http.Request) {
				us := sessionOf(r)
				level := m.resolveLevel(us)
				switch {
				case level == lvlOwner:
					http.Redirect(w, r, "/admin", http.StatusSeeOther)
				case len(r.URL.Query().Get("guild")) > 0 && m.canManageGuild(us, r.URL.Query().Get("guild")):
					http.Redirect(w, r, "/g/"+r.URL.Query().Get("guild")+"/modules", http.StatusSeeOther)
				default:
					http.Redirect(w, r, "/", http.StatusSeeOther)
				}
			})(w, r)
			return
		}
		methodNotAllowed(w)
		return
	case "tickets":
		if r.Method == "GET" {
			m.requireAuthed(m.handleTicketsPage)(w, r)
			return
		}
		methodNotAllowed(w)
		return
	case "permissions":
		if r.Method == "GET" {
			m.requireOwner(m.handlePermissionsPage)(w, r)
			return
		}
		methodNotAllowed(w)
		return
	case "logs":
		if r.Method == "GET" {
			m.requireOwner(m.handleLogsPage)(w, r)
			return
		}
		methodNotAllowed(w)
		return
	case "api":
		m.routeAPI(w, r, parts[1:])
		return
	}
	http.NotFound(w, r)
}

// methodNotAllowed writes a plain 405 (page routes only accept GET).
func methodNotAllowed(w http.ResponseWriter) {
	http.Error(w, "405 method not allowed", http.StatusMethodNotAllowed)
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
		// Per-module sidebar sections (Task 10): every loaded module that
		// declares WebTabs and/or is WebConfigurable gets a sidebar group.
		d.ModuleNav = m.moduleNav(us)
	}
	return d
}

// moduleNav assembles the per-module sidebar groups for the current session:
// one group per loaded module that implements WebTabser (extra tabs) or
// WebConfigurable (implicit Settings link). Visibility mirrors the tickets
// page: extra tabs render for any authed user; the Settings link only for
// viewers who can reach module config (staff+ sees guild-scoped fields).
func (m *DashboardModule) moduleNav(us *userSession) []moduleNavItem {
	if m.bot == nil {
		return nil
	}
	mgr, ok := m.bot.GetModuleManager().(*modules.Manager)
	if !ok {
		return nil
	}
	level := m.resolveLevel(us)
	var out []moduleNavItem
	for _, name := range m.bot.GetLoadedModuleNames() {
		if name == "dashboard" {
			continue // the dashboard itself has its own nav entries
		}
		mod, ok := mgr.Get(name)
		if !ok {
			continue
		}
		item := moduleNavItem{Name: name}
		// Settings link: only when the module opted into WebConfigurable
		// (same HasWebConfig filter as webCfg) AND the viewer can reach the
		// settings page (staff+; regular users have no settings).
		if _, isWC := m.webCfg(name); isWC {
			if levelGEQ(level, lvlStaff) || level == lvlOwner || level == lvlElevated {
				item.Settings = "/configuration#" + name
			}
		}
		if wt, isWT := modules.IsWebTabser(mod); isWT {
			for _, tab := range wt.WebTabs() {
				if tab.Slug == "" {
					continue
				}
				item.Tabs = append(item.Tabs, navTabItem{Name: tab.Name, URL: tab.Slug})
			}
		}
		if item.Settings == "" && len(item.Tabs) == 0 {
			continue // nothing to show for this module
		}
		// Active state: current page path belongs to one of this module's tabs.
		out = append(out, item)
	}
	// Sort groups by module name for stable nav order.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
