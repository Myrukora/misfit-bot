package dashboard

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"

	"github.com/misfit/bot/internal/util"
	"github.com/misfit/bot/modules"
)

// audit.go — the dashboard's small, durable activity log: login successes and
// failures, page visits, and every authenticated mutation. Entries carry the
// user, level, IP, action and result, and are retained for 30 days. The log is
// visible to the owner and elevated users only.
//
// The file is JSONL beside the bot logs (<bot dir>/logs/dashboard-audit.jsonl,
// 0600). A corrupt or unwritable file degrades to memory-only; auditing never
// breaks a request.

const (
	auditRetention  = 30 * 24 * time.Hour
	auditMaxEntries = 5000
	defaultAuditCap = 200
	maxAuditCap     = 1000
)

type auditEntry struct {
	Time     time.Time `json:"time"`
	Event    string    `json:"event"` // page | login | logout | mutation
	Success  bool      `json:"success"`
	UserID   string    `json:"user_id,omitempty"`
	Username string    `json:"username,omitempty"`
	Level    string    `json:"level,omitempty"`
	IP       string    `json:"ip,omitempty"`
	Action   string    `json:"action"` // "GET /tos", "POST /api/settings/core", "Discord OAuth"
	Detail   string    `json:"detail,omitempty"`
	Status   int       `json:"status,omitempty"`
}

type auditFilter struct {
	Event  string
	UserID string
	Limit  int
}

type auditLog struct {
	mu        sync.Mutex
	path      string
	entries   []auditEntry
	retention time.Duration
	max       int
	logger    modules.Logger
	now       func() time.Time
	lastPrune time.Time
}

// newAuditLog opens (or creates) the JSONL audit file in dir. An empty dir or
// any I/O error yields a memory-only log.
func newAuditLog(dir string, logger modules.Logger) *auditLog {
	a := &auditLog{
		retention: auditRetention,
		max:       auditMaxEntries,
		logger:    logger,
		now:       time.Now,
		lastPrune: time.Now(),
	}
	if dir == "" {
		return a
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.logError("create audit dir %s: %v", dir, err)
		return a
	}
	a.path = filepath.Join(dir, "dashboard-audit.jsonl")
	a.load()
	return a
}

func (a *auditLog) logError(format string, args ...any) {
	if a.logger != nil {
		a.logger.Error("Dashboard audit: "+format, args...)
	}
}

// load reads the file, drops entries outside the retention window or corrupt
// lines, and rewrites the file when anything was dropped.
func (a *auditLog) load() {
	raw, err := os.ReadFile(a.path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		a.logError("read %s: %v", a.path, err)
		return
	}
	now := a.now().UTC()
	cutoff := now.Add(-a.retention)
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	dropped := 0
	entries := make([]auditEntry, 0, 256)
	for scanner.Scan() {
		var e auditEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			dropped++
			continue
		}
		e.Time = e.Time.UTC()
		if e.Time.Before(cutoff) {
			dropped++
			continue
		}
		entries = append(entries, e)
	}
	if len(entries) > a.max {
		entries = append([]auditEntry(nil), entries[len(entries)-a.max:]...)
		dropped++
	}
	a.entries = entries
	a.lastPrune = now
	if dropped > 0 {
		a.rewriteLocked()
	}
	_ = os.Chmod(a.path, 0o600)
}

func (a *auditLog) rewriteLocked() {
	if a.path == "" {
		return
	}
	var buf bytes.Buffer
	for _, e := range a.entries {
		raw, err := json.Marshal(e)
		if err != nil {
			continue
		}
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		a.logError("write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, a.path); err != nil {
		_ = os.Remove(tmp)
		a.logError("rename %s: %v", tmp, err)
	}
}

func (a *auditLog) record(e auditEntry) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now().UTC()
	if e.Time.IsZero() {
		e.Time = now
	} else {
		e.Time = e.Time.UTC()
	}
	a.entries = append(a.entries, e)
	if len(a.entries) > a.max {
		a.entries = append([]auditEntry(nil), a.entries[len(a.entries)-a.max:]...)
	}
	if a.path != "" {
		if raw, err := json.Marshal(e); err == nil {
			f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				a.logError("open %s: %v", a.path, err)
			} else {
				_, werr := f.Write(append(raw, '\n'))
				cerr := f.Close()
				if werr != nil {
					a.logError("append %s: %v", a.path, werr)
				} else if cerr != nil {
					a.logError("close %s: %v", a.path, cerr)
				}
			}
		}
	}
	if now.Sub(a.lastPrune) >= time.Hour {
		a.pruneLocked(now)
	}
}

// pruneLocked drops expired entries and rewrites the file when anything was
// removed. It holds a.mu.
func (a *auditLog) pruneLocked(now time.Time) {
	cutoff := now.Add(-a.retention)
	kept := a.entries[:0]
	dropped := 0
	for _, e := range a.entries {
		if e.Time.Before(cutoff) {
			dropped++
			continue
		}
		kept = append(kept, e)
	}
	a.entries = kept
	a.lastPrune = now
	if dropped > 0 {
		a.rewriteLocked()
	}
}

func (a *auditLog) list(filter auditFilter) []auditEntry {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked(a.now().UTC())
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultAuditCap
	}
	if limit > maxAuditCap {
		limit = maxAuditCap
	}
	out := make([]auditEntry, 0, limit)
	for i := len(a.entries) - 1; i >= 0 && len(out) < limit; i-- {
		e := a.entries[i]
		if filter.Event != "" && e.Event != filter.Event {
			continue
		}
		if filter.UserID != "" && e.UserID != filter.UserID {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (a *auditLog) count() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked(a.now().UTC())
	return len(a.entries)
}

// ── request logging ─────────────────────────────────────────────────────

// auditMiddleware records HTML page visits (including public /tos and /policy)
// and authenticated mutations. It sits inside authMiddleware so the session is
// in the request context.
func (m *DashboardModule) auditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		m.recordRequest(r, sw)
	})
}

// setAuditDetail lets a handler override the generic mutation detail for the
// request's audit row. It only reaches the audit middleware's statusWriter;
// other wrappers ignore it.
func setAuditDetail(w http.ResponseWriter, detail string) {
	if sw, ok := w.(interface{ setAuditDetail(string) }); ok {
		sw.setAuditDetail(detail)
	}
}

func (s *statusWriter) setAuditDetail(detail string) {
	s.auditDetail = detail
}

func (m *DashboardModule) recordRequest(r *http.Request, sw *statusWriter) {
	if m.audit == nil {
		return
	}
	path := r.URL.Path
	if strings.HasPrefix(path, "/static/") {
		return
	}
	// /callback and /logout have dedicated login/logout events. /login POST
	// starts OAuth and is represented by the callback outcome.
	if path == "/callback" || path == "/logout" {
		return
	}
	us := sessionOf(r)
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		// Known dashboard pages are logged regardless of the response content
		// type (a proxy/handler can strip it); unknown paths only when the
		// response is HTML, so JSON API reads and static assets stay out.
		ct := strings.ToLower(sw.Header().Get("Content-Type"))
		if !isDashboardPage(path) && !strings.HasPrefix(ct, "text/html") {
			return
		}
		e := auditEntry{
			Event:   "page",
			Success: sw.status < 400,
			IP:      clientIP(r),
			Action:  r.Method + " " + path,
			Detail:  pageLabel(path),
			Status:  sw.status,
		}
		if us != nil {
			e.UserID = us.userID.String()
			e.Username = us.username
			e.Level = m.resolveLevel(us)
		}
		m.audit.record(e)
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		if us == nil || path == "/login" {
			return
		}
		detail := mutationDetail(path)
		if sw.auditDetail != "" {
			// Handlers that know more than the route (e.g. archive deletion
			// reasons) supply a specific detail. Never include deleted content.
			detail = sw.auditDetail
		}
		m.audit.record(auditEntry{
			Event:    "mutation",
			Success:  sw.status < 400,
			UserID:   us.userID.String(),
			Username: us.username,
			Level:    m.resolveLevel(us),
			IP:       clientIP(r),
			Action:   r.Method + " " + path,
			Detail:   detail,
			Status:   sw.status,
		})
	}
}

func (m *DashboardModule) recordLogin(r *http.Request, success bool, user *discord.User, level, detail string, status int) {
	if m.audit == nil {
		return
	}
	e := auditEntry{
		Event:   "login",
		Success: success,
		IP:      clientIP(r),
		Action:  "Discord OAuth",
		Detail:  detail,
		Status:  status,
		Level:   level,
	}
	if user != nil {
		e.UserID = user.ID.String()
		e.Username = user.Username
	}
	m.audit.record(e)
}

func (m *DashboardModule) recordLogout(r *http.Request, us *userSession) {
	if m.audit == nil || us == nil {
		return
	}
	m.audit.record(auditEntry{
		Event:    "logout",
		Success:  true,
		UserID:   us.userID.String(),
		Username: us.username,
		Level:    m.resolveLevel(us),
		IP:       clientIP(r),
		Action:   "Logout",
		Detail:   "session ended",
	})
}

// clientIP extracts the visitor address. Forwarded headers are trusted only
// when the immediate peer is loopback/private (a local reverse proxy or
// cloudflared), so a direct internet client cannot spoof its address.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if net.ParseIP(first) != nil {
				return first
			}
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" && net.ParseIP(xr) != nil {
			return xr
		}
	}
	return host
}

func isDashboardPage(path string) bool {
	if path == "" {
		path = "/"
	}
	if path != "/" {
		path = strings.TrimSuffix(path, "/")
	}
	switch path {
	case "/", "/login", "/tos", "/policy", "/overview", "/config", "/permissions",
		"/logs", "/audit", "/modules", "/tickets", "/settings", "/configuration", "/admin":
		return true
	}
	return strings.HasPrefix(path, "/g/") ||
		strings.HasPrefix(path, "/guild/") ||
		strings.HasPrefix(path, "/tickets/")
}

func pageLabel(path string) string {
	switch path {
	case "/":
		return "Server picker"
	case "/login":
		return "Login"
	case "/tos":
		return "Terms of Service"
	case "/policy":
		return "Privacy Policy"
	case "/overview":
		return "Overview"
	case "/config":
		return "Configuration"
	case "/permissions":
		return "Permissions"
	case "/logs":
		return "Bot logs"
	case "/audit":
		return "Dashboard audit"
	case "/modules":
		return "Modules"
	case "/tickets":
		return "Tickets"
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "g" {
		switch parts[2] {
		case "commands":
			return "Server commands"
		case "tickets":
			return "Server tickets"
		case "welcome":
			return "Server welcome"
		case "moderation":
			return "Server moderation"
		case "reactionroles":
			return "Server reaction roles"
		case "modules":
			return "Server modules"
		case "imagefilter":
			return "Server image filter"
		}
		return "Server page"
	}
	if len(parts) >= 2 && parts[0] == "tickets" {
		return "Ticket transcript"
	}
	return path
}

func mutationDetail(path string) string {
	switch {
	case path == "/api/settings/core":
		return "Core settings update"
	case strings.HasPrefix(path, "/api/settings/module/"):
		return "Module settings update"
	case strings.HasPrefix(path, "/api/modules/"):
		return "Module lifecycle action"
	case strings.HasPrefix(path, "/api/guilds/"):
		return "Server module update"
	case strings.HasPrefix(path, "/api/tickets/"):
		return "Tickets update"
	case strings.HasPrefix(path, "/api/permissions/"):
		return "Permissions update"
	case path == "/api/backups":
		return "Backup action"
	case path == "/api/exec":
		return "Dashboard command execution"
	case path == "/api/restart":
		return "Bot restart"
	case path == "/api/shutdown":
		return "Bot shutdown"
	case strings.HasPrefix(path, "/api/update"):
		return "Updater action"
	case path == "/api/cmdcfg/toggle":
		return "Command override update"
	case path == "/api/ratelimit/reset":
		return "Rate limit reset"
	default:
		return ""
	}
}

// ── /audit page ─────────────────────────────────────────────────────────

func (m *DashboardModule) handleAuditPage(w http.ResponseWriter, r *http.Request) {
	event := strings.TrimSpace(r.URL.Query().Get("event"))
	switch event {
	case "page", "login", "logout", "mutation":
	default:
		event = ""
	}
	userID := util.ExtractID(strings.TrimSpace(r.URL.Query().Get("user")))
	if userID != "" && !validGuildID(userID) {
		userID = ""
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = defaultAuditCap
	}
	if limit > maxAuditCap {
		limit = maxAuditCap
	}
	var entries []auditEntry
	total := 0
	path := ""
	if m.audit != nil {
		entries = m.audit.list(auditFilter{Event: event, UserID: userID, Limit: limit})
		total = m.audit.count()
		path = m.audit.path
	}
	d := m.baseData(sessionOf(r))
	d.Page = "audit"
	query := url.Values{}
	if event != "" {
		query.Set("event", event)
	}
	if userID != "" {
		query.Set("user", userID)
	}
	if limit != defaultAuditCap {
		query.Set("limit", strconv.Itoa(limit))
	}
	d.Content = map[string]any{
		"entries":       entries,
		"total":         total,
		"path":          path,
		"retentionDays": int(auditRetention / (24 * time.Hour)),
		"filterEvent":   event,
		"filterUser":    userID,
		"limit":         limit,
		"query":         query.Encode(),
		"generated":     time.Now().UTC().Format("2006-01-02 15:04:05"),
	}
	m.tmpl.render(w, "rd_audit", d)
}
