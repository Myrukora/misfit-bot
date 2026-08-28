package dashboard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/config"
	"github.com/misfit/bot/modules"
	"github.com/misfit/bot/permissions"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/oauth2"
	"github.com/disgoorg/snowflake/v2"
)

// Dashboard is the bot's control surface: a role-tiered web dashboard running
// in-process with the Discord gateway. It is core infrastructure — always
// running, never a module, never unloadable. It reads its own config from the
// core config.yml (dashboard:/oauth: sections) plus a small 0600 file for
// per-installation secrets (session_secret, client_id, allowed_guilds).
type DashboardModule struct {
	bot      commands.Interface
	botName  string
	cfg      *DashboardConfig
	client   *bot.Client // cached *bot.Client from bot.GetClient()
	oauth    *oauth2.Client
	stateCtl oauth2.StateController // reused across oauth2.Client rebuilds so in-flight logins survive secret changes
	sessions *sessionStore
	tmpl     *templateBundle

	srv       *http.Server
	running   bool
	stopped   bool           // set on unload: refuses any further startServer (kills in-flight rebindSoon)
	serveWG   sync.WaitGroup // tracks the active Serve goroutine; stopServer waits on it
	mu        sync.Mutex     // guards Start/Stop, cfg swaps, running/stopped/srv/lastErr
	restartMu sync.Mutex     // serializes the whole restartServer sequence (bind→stop→start)
	dataDir   string
	logger    modules.Logger
	lastErr   string // last server bind error, surfaced in [p]dashboard status when not running

	// appName caches the Developer Portal application name fetched via REST
	// (fallback identity source when the gateway self-user cache is empty).
	appNameMu sync.Mutex
	appName   string
	appNameAt time.Time

	// coreConfig memo: config.yml is small but re-reading + re-parsing it on
	// every page render (effectiveListen/effectivePublicURL/redirectBaseURL)
	// is wasteful; a 2s TTL keeps rebind/presence reads responsive while
	// avoiding per-request disk I/O. invalidateCoreConfig drops it after a
	// config write so changes apply immediately.
	coreCfgMu   sync.Mutex
	coreCfgMemo *config.Config
	coreCfgAt   time.Time
}

// Deps carries the bot-facing dependencies the dashboard needs. It is the
// core-subsystem analogue of the old modules.Context: the dashboard reads
// everything through the commands.Interface and a couple of scalars.
type Deps struct {
	Bot     commands.Interface
	BotName string
	DataDir string
	Logger  modules.Logger
}

// New constructs the dashboard subsystem. It does NOT start the HTTP server —
// call Start() after the Discord gateway is up (OAuth guild checks need the
// client cache).
func New(deps Deps) *DashboardModule {
	m := &DashboardModule{
		bot:      deps.Bot,
		botName:  deps.BotName,
		dataDir:  deps.DataDir,
		logger:   deps.Logger,
		sessions: newSessionStore(),
	}
	if c, ok := deps.Bot.GetClient().(*bot.Client); ok {
		m.client = c
	}
	return m
}

// Start initializes config, OAuth, templates and the HTTP server. It is
// best-effort: a bind failure (e.g. the port is taken) is logged and the
// dashboard stays up so the owner can rebind via config — it never crashes
// the bot.
func (m *DashboardModule) Start() {
	// One-time migration: pre-restructure installs kept the dashboard config
	// at <config dir>/module_configs/dashboard/config.yml. Move it into the
	// data home so session_secret / allowed_guilds survive.
	if migrated, err := migrateLegacyConfig(m.dataDir, m.bot.GetConfigDir()); err != nil {
		m.logger.Warn("Dashboard: legacy config migration failed (continues with defaults): %v", err)
	} else if migrated {
		m.logger.Info("Dashboard: migrated config from module_configs/dashboard/config.yml to %s", cfgPath(m.dataDir))
	}

	cfg, err := loadConfig(m.dataDir)
	if err != nil {
		m.logger.Error("Dashboard: load config failed: %v", err)
		return
	}
	if cfg.ClientID == "" && m.client != nil {
		cfg.ClientID = m.client.ApplicationID.String()
		_ = cfg.Save(m.dataDir)
	}
	if err := cfg.ensureSessionSecret(m.dataDir); err != nil {
		m.logger.Error("Dashboard: init session secret failed: %v", err)
		return
	}
	m.cfg = cfg
	m.refreshOAuth()

	tmpl, err := loadTemplates()
	if err != nil {
		m.logger.Error("Dashboard: load templates failed: %v", err)
		return
	}
	m.tmpl = tmpl

	if err := m.startServer(); err != nil {
		m.mu.Lock()
		m.lastErr = fmt.Sprintf("%v", err)
		m.mu.Unlock()
		m.logger.Error("Dashboard HTTP server failed to bind %s (set dashboard.listen in config.yml to rebind): %v", m.effectiveListen(), err)
	} else {
		m.mu.Lock()
		m.lastErr = ""
		m.mu.Unlock()
	}
	m.logger.Info("Dashboard started (listen=%s configured=%v running=%v)", m.effectiveListen(), m.configured(), m.isRunning())
}

// Stop shuts down the HTTP server and marks the dashboard stopped so no
// in-flight rebind can leave a zombie listener behind.
func (m *DashboardModule) Stop() {
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
	m.stopServer()
}

// refreshOAuthLocked (re)builds the oauth2.Client from the current config.
// Callers MUST hold m.mu. It reuses the module's state controller so that
// in-flight OAuth login flows survive a client rebuild (e.g. a client_secret
// change while a user is mid-login). The client secret is resolved inline
// (NOT via effectiveClientSecret, which locks m.mu — non-reentrant): core
// config's oauth.client_secret is read first (coreConfig() never touches
// m.mu), then the module config's own value.
func (m *DashboardModule) refreshOAuthLocked() {
	if m.cfg == nil || m.cfg.ClientID == "" {
		m.oauth = nil
		return
	}
	id, err := snowflake.Parse(m.cfg.ClientID)
	if err != nil {
		m.oauth = nil
		return
	}
	secret := m.cfg.ClientSecret
	if cc := m.coreConfig(); cc != nil && strings.TrimSpace(cc.OAuth.ClientSecret) != "" {
		secret = strings.TrimSpace(cc.OAuth.ClientSecret)
	}
	if m.stateCtl == nil {
		m.stateCtl = oauth2.NewStateController()
	}
	m.oauth = oauth2.New(id, secret, oauth2.WithStateController(m.stateCtl))
}

// refreshOAuth acquires m.mu and rebuilds the oauth2.Client. Use this from
// paths that do NOT hold the lock; use refreshOAuthLocked otherwise.
func (m *DashboardModule) refreshOAuth() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshOAuthLocked()
}

// coreConfig reads the bot's main config.yml to honour the optional `dashboard`
// section. Returns nil if config.yml can't be loaded (fall back to module
// config). This is how the owner pins the listen port from the core config
// instead of the module's own file — e.g. when 8080 is already taken and the
// dashboard can't start to be reconfigured via the web.
//
// The result is memoized for 2s (see the struct comment) — callers invoke this
// on every request path. Write paths call invalidateCoreConfig so live changes
// (dashboard_listen/oauth secrets via [p]set or the web) apply immediately.
func (m *DashboardModule) coreConfig() *config.Config {
	if m.bot == nil {
		return nil
	}
	m.coreCfgMu.Lock()
	defer m.coreCfgMu.Unlock()
	if m.coreCfgMemo != nil && time.Since(m.coreCfgAt) < 2*time.Second {
		return m.coreCfgMemo
	}
	cfg, err := config.Load(m.bot.GetConfigDir())
	if err != nil {
		return nil
	}
	m.coreCfgMemo = cfg
	m.coreCfgAt = time.Now()
	return cfg
}

// invalidateCoreConfig drops the memoized core config after a write so the
// next read reflects the new file immediately.
func (m *DashboardModule) invalidateCoreConfig() {
	m.coreCfgMu.Lock()
	m.coreCfgMemo = nil
	m.coreCfgMu.Unlock()
}

// effectiveListen returns the bind address, preferring the core config's
// `dashboard.listen` when set, then the module config, then the default. The
// result is normalized to a bare host:port so a persisted URL-shaped value
// (e.g. "http://127.0.0.1:9090/") still binds instead of failing with
// "too many colons in address".
func (m *DashboardModule) effectiveListen() string {
	if cc := m.coreConfig(); cc != nil && strings.TrimSpace(cc.Dashboard.Listen) != "" {
		return config.NormalizeListen(cc.Dashboard.Listen)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg != nil && m.cfg.Listen != "" {
		return config.NormalizeListen(m.cfg.Listen)
	}
	return "127.0.0.1:8080"
}

// effectivePublicURL returns the public base URL, preferring the core config's
// `dashboard.public_url` when set, then the module config's value.
func (m *DashboardModule) effectivePublicURL() string {
	if cc := m.coreConfig(); cc != nil && strings.TrimSpace(cc.Dashboard.PublicURL) != "" {
		return strings.TrimRight(strings.TrimSpace(cc.Dashboard.PublicURL), "/")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg != nil && m.cfg.PublicURL != "" {
		return m.cfg.PublicURL
	}
	return ""
}

// effectiveClientSecret returns the Discord OAuth2 client secret, preferring
// the core config's `oauth.client_secret` (the single shared credential the
// owner sets once), then the module's 0600 config as a backwards-compatible
// fallback (for installs that set it before the secret moved to core config).
func (m *DashboardModule) effectiveClientSecret() string {
	if cc := m.coreConfig(); cc != nil && strings.TrimSpace(cc.OAuth.ClientSecret) != "" {
		return strings.TrimSpace(cc.OAuth.ClientSecret)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg != nil && m.cfg.ClientSecret != "" {
		return m.cfg.ClientSecret
	}
	return ""
}

// configured reports whether OAuth login is possible (client_id + client_secret
// set, using the effective client secret: core config oauth.client_secret,
// else module config). public_url is NOT required: when it's unset the OAuth
// redirect URI is derived per request from the browser's own origin (scheme +
// Host — see redirectBaseURL), so direct LAN/localhost access works out of the
// box; public_url only overrides the base for tunnel/reverse-proxy setups.
func (m *DashboardModule) configured() bool {
	m.mu.Lock()
	c := m.cfg
	m.mu.Unlock()
	if c == nil || c.ClientID == "" {
		return false
	}
	return m.effectiveClientSecret() != ""
}

// ── URL resolution ─────────────────────────────────────────────────────────

// effectiveBaseURL returns the base URL the dashboard is reachable at: the
// configured public_url when set, else the auto-detected LAN URL. Used for
// reporting ([p]dashboard url/status, the /setup page).
func (m *DashboardModule) effectiveBaseURL() string {
	if u := m.effectivePublicURL(); u != "" {
		return u
	}
	return m.lanURL()
}

// redirectBaseURL returns the base URL for the OAuth redirect URI, derived per
// request: the configured public_url when set (tunnel / reverse proxy), else
// the request's own origin (scheme://host) so direct LAN/localhost access
// works from whatever address the user opened. The result matches exactly what
// the owner must register in the Developer Portal.
func (m *DashboardModule) redirectBaseURL(r *http.Request) string {
	if u := m.effectivePublicURL(); u != "" {
		return u
	}
	if host := strings.TrimSpace(r.Host); host != "" {
		return m.requestScheme(r) + "://" + host
	}
	return m.lanURL()
}

// requestScheme reports whether the client connection is HTTPS, honouring the
// X-Forwarded-Proto header set by TLS-terminating reverse proxies/tunnels.
func (m *DashboardModule) requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); strings.EqualFold(p, "https") {
		return "https"
	}
	return "http"
}

// lanURL returns the dashboard's LAN-reachable base URL. A concrete
// non-loopback listen host (e.g. "192.168.1.5:8080") is used directly;
// wildcard/loopback binds fall back to the auto-detected primary LAN address.
// net.JoinHostPort keeps IPv6 hosts correctly bracketed.
func (m *DashboardModule) lanURL() string {
	return "http://" + net.JoinHostPort(m.lanHost(), m.listenPort())
}

// lanHost resolves the host part of the LAN URL: the configured concrete
// listen host when the listener binds one (and it's not loopback/wildcard), else
// the machine's primary LAN address (see lanIP). Loopback/wildcard listens are
// treated as "not concrete" so the reported URL is the address the owner
// should open from other devices (the url/status commands separately warn when
// the listener is still localhost-only).
func (m *DashboardModule) lanHost() string {
	host, _, err := net.SplitHostPort(m.effectiveListen())
	if err == nil && host != "" && !isLoopbackHost(host) && host != "0.0.0.0" && host != "::" {
		return host
	}
	return m.lanIP()
}

// lanIP returns the machine's primary LAN IPv4 address. The default-route
// interface is preferred (net.Dial("udp", ...) performs route lookup only —
// no packets are sent), so Docker bridges and VPN interfaces can't shadow the
// real LAN address. Falls back to the first suitable address from
// net.InterfaceAddrs (see lanIPFromAddrs), then 127.0.0.1.
func (m *DashboardModule) lanIP() string {
	if ip := primaryLANIP(); ip != "" {
		return ip
	}
	if addrs, err := interfaceAddrs(); err == nil {
		if ip := lanIPFromAddrs(addrs); ip != "" {
			return ip
		}
	}
	return "127.0.0.1"
}

// primaryLANIP asks the kernel for the local address of the default-route
// interface. The UDP dial never sends a packet; it only resolves the route.
var primaryLANIP = func() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil && !addr.IP.IsLoopback() {
		return addr.IP.String()
	}
	return ""
}

// interfaceAddrs is a seam for tests (defaults to net.InterfaceAddrs).
var interfaceAddrs = func() ([]net.Addr, error) { return net.InterfaceAddrs() }

// lanIPFromAddrs picks the most LAN-appropriate non-loopback IPv4 from addrs:
// 192.168.0.0/16 and 10.0.0.0/8 first (typical home/office LANs), then any
// other non-loopback IPv4. Docker's default bridge (172.17.0.0/16) and the
// CGNAT range (100.64.0.0/10, used by Tailscale et al.) are excluded entirely
// — reporting them would produce an unreachable LAN URL.
func lanIPFromAddrs(addrs []net.Addr) string {
	var fallback string
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipn.IP.To4()
		if ip == nil || ip.IsLoopback() || isExcludedLANIP(ip) {
			continue
		}
		if fallback == "" {
			fallback = ip.String()
		}
		if isPreferredLANIP(ip) {
			return ip.String()
		}
	}
	return fallback
}

// isPreferredLANIP reports whether ip is in a range typically used for LAN
// clients: 192.168.0.0/16 or 10.0.0.0/8. 172.16.0.0/12 is deliberately NOT
// preferred because Docker and other container runtimes default to subnets in
// that block (172.17-172.31), so a docker bridge could shadow the real LAN.
func isPreferredLANIP(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil {
		return false
	}
	return (ip[0] == 192 && ip[1] == 168) || ip[0] == 10
}

// isExcludedLANIP reports whether ip must never be reported as the LAN
// address: Docker's default bridge (172.17.0.0/16) and the CGNAT range
// (100.64.0.0/10) used by Tailscale and carriers.
func isExcludedLANIP(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil {
		return false
	}
	if ip[0] == 172 && ip[1] == 17 {
		return true
	}
	return ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127
}

// listenPort extracts the port from the effective listen address; defaults to
// 8080 when the address has no usable port.
func (m *DashboardModule) listenPort() string {
	_, port, err := net.SplitHostPort(m.effectiveListen())
	if err != nil || port == "" || port == "0" {
		return "8080"
	}
	return port
}

// loopbackOnlyListen reports whether the effective listen address binds the
// loopback interface only (127.0.0.1/::1/localhost). ":8080" and "0.0.0.0:8080"
// bind all interfaces and are NOT loopback-only.
func (m *DashboardModule) loopbackOnlyListen() bool {
	host, _, err := net.SplitHostPort(m.effectiveListen())
	if err != nil {
		return true // unparseable listen address: assume localhost-only
	}
	return isLoopbackHost(host)
}

// isLoopbackHost reports whether host is localhost, 127.0.0.1 or ::1.
func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// startServer binds the HTTP listener and serves until stopped.
func (m *DashboardModule) startServer() error {
	return m.startServerWithListener(nil)
}

// startServerWithListener binds and serves. When ln is non-nil it is used
// as-is — the caller has already proven it bindable while the previous server
// was still running (see restartServer), so a failed bind can never take the
// dashboard offline.
func (m *DashboardModule) startServerWithListener(ln net.Listener) error {
	// Resolve the listen address BEFORE taking m.mu. effectiveListen() locks
	// m.mu internally, so calling it under m.mu would self-deadlock (sync.Mutex
	// is non-reentrant). buildHandler() below does not touch m.mu.
	listen := m.effectiveListen()
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		if ln != nil {
			ln.Close()
		}
		return fmt.Errorf("module is stopped")
	}
	if m.running {
		// Already serving: the caller-supplied listener must not leak —
		// close it so the port is not left bound with no server accepting.
		if ln != nil {
			ln.Close()
		}
		return nil
	}
	if m.client == nil {
		if ln != nil {
			ln.Close()
		}
		return fmt.Errorf("bot client unavailable")
	}
	handler := m.buildHandler()
	srv := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	var err error
	if ln == nil {
		ln, err = net.Listen("tcp", srv.Addr) // detect address-in-use immediately
		if err != nil {
			return fmt.Errorf("listen %s: %w", srv.Addr, err)
		}
	}
	m.srv = srv
	m.running = true
	m.lastErr = ""
	m.serveWG.Add(1)
	go func() {
		defer m.serveWG.Done()
		serveErr := srv.Serve(ln)
		m.mu.Lock()
		m.running = false
		// Only clear m.srv if it still points at THIS server — a newer server
		// may have been started in the meantime (stopServer nils it first).
		if m.srv == srv {
			m.srv = nil
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			m.lastErr = fmt.Sprintf("server stopped: %v", serveErr)
		}
		m.mu.Unlock()
		if serveErr != nil && serveErr != http.ErrServerClosed {
			m.logger.Error("Dashboard http server stopped: %v", serveErr)
		}
	}()
	return nil
}

// stopServer gracefully shuts the server down and waits for it to exit.
func (m *DashboardModule) stopServer() {
	m.mu.Lock()
	srv := m.srv
	m.srv = nil
	m.mu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	// Wait for the Serve goroutine to actually exit (Shutdown closes the
	// listener first, so Serve returns promptly) — a subsequent startServer
	// must never observe the old server's stale running=true.
	m.serveWG.Wait()
}

// restartServer rebinds the HTTP server without a downtime window: when the
// target address differs from the current one, the new listener is created
// (and proven bindable) BEFORE the old server is stopped. If the new address
// can't bind, the old server keeps serving untouched and the error is
// returned — a bad dashboard_listen can never take the dashboard offline.
// A same-address restart (e.g. public_url-only change) is a plain in-place
// restart: the address is already bound by the running server.
func (m *DashboardModule) restartServer() error {
	// Serialize the full sequence: two concurrent configuration requests must
	// never interleave bind/stop/start — one could kill the other's freshly
	// started server or leave a pre-bound listener open (blocking rebinds).
	m.restartMu.Lock()
	defer m.restartMu.Unlock()

	listen := m.effectiveListen()
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	m.mu.Lock()
	curAddr := ""
	if m.srv != nil {
		curAddr = m.srv.Addr
	}
	m.mu.Unlock()

	if curAddr == listen {
		m.stopServer()
		return m.startServer()
	}

	// Different address: bind the NEW listener while the old server is still
	// serving. On failure the old server keeps running — no rollback needed.
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listen, err)
	}
	m.stopServer()
	if err := m.startServerWithListener(ln); err != nil {
		ln.Close() // don't leak the pre-bound listener (e.g. module stopped)
		return err
	}
	return nil
}

// isRunning reports whether the HTTP server is currently serving.
func (m *DashboardModule) isRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// ── WebConfigurable (dogfood + live self-config) ──────────────────────────

func (m *DashboardModule) WebConfigSchema() []modules.ConfigField {
	// listen / public_url / client_secret are intentionally NOT here — they now
	// live in the core settings page (Dashboard + Secrets sections), which is
	// the single obvious place to configure them. Only the fields that stay in
	// the 0600 module config file remain self-configurable here.
	return []modules.ConfigField{
		{Key: "client_id", Label: "OAuth Client ID", Help: "Discord application client ID. Auto-derived from the bot application if left empty.", Type: modules.FieldTypeText, Scope: "global"},
		{Key: "session_secret", Label: "Session Secret", Help: "Secret used to sign session cookies. Auto-generated if empty.", Type: modules.FieldTypeSecret, Scope: "global"},
		{Key: "allowed_guilds", Label: "Allowed Guilds", Help: "Optional allowlist of servers. Empty = allow all bot guilds. Checked servers stay accessible; unchecked ones are locked out of the dashboard.", Type: modules.FieldTypeMulti, Options: m.guildLabelOptions(), Scope: "global"},
		{Key: "exec_mode", Label: "Command execution way", Help: "Which command implementation the Run buttons and commands tab use: prefix (text commands; requires Discord's Message Content intent) or slash (works without the intent, matches Discord's native UI).", Type: modules.FieldTypeSelect, Options: []string{"prefix", "slash"}, Scope: "global"},
	}
}

// WebGetConfig returns the dashboard's global config for the web UI (secrets redacted).
func (m *DashboardModule) WebGetConfig(guildID string) (map[string]string, error) {
	if guildID != "" {
		return map[string]string{}, nil // dashboard has only global-scoped config
	}
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	// Secrets are passed through redacted; only the bot owner sees real values
	// via the dedicated /settings page (handled in pages.go), but WebGetConfig
	// itself always redacts to be safe, matching the WebConfigurable contract.
	v := map[string]string{
		"client_id":      cfg.ClientID,
		"session_secret": redactedIfSet(cfg.SessionSecret),
		"allowed_guilds": strings.Join(m.guildLabels(cfg.AllowedGuilds), "\n"),
		"exec_mode":      cfg.ExecMode,
	}
	return v, nil
}

// guildLabel renders "Name (ID)" for a cached guild, or the raw ID when the
// guild isn't cached.
func (m *DashboardModule) guildLabel(id string) string {
	if gid, err := snowflake.Parse(id); err == nil {
		if g, ok := m.client.Caches.Guild(gid); ok {
			return fmt.Sprintf("%s (%s)", g.Name, id)
		}
	}
	return id
}

// guildLabels maps guild IDs to picker labels.
func (m *DashboardModule) guildLabels(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, m.guildLabel(id))
	}
	return out
}

// guildLabelOptions lists every bot guild as a "Name (ID)" picker option.
func (m *DashboardModule) guildLabelOptions() []string {
	var out []string
	for g := range m.client.Caches.Guilds() {
		out = append(out, fmt.Sprintf("%s (%s)", g.Name, g.ID.String()))
	}
	sort.Strings(out)
	return out
}

// parseGuildLabels converts "Name (123456789012345678)" labels back to IDs;
// bare values (already IDs) pass through unchanged.
func parseGuildLabels(labels []string) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if i := strings.LastIndex(l, " ("); i > 0 && strings.HasSuffix(l, ")") {
			id := l[i+2 : len(l)-1]
			if _, err := snowflake.Parse(id); err == nil {
				out = append(out, id)
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

// execMode returns the configured command execution way ("prefix" or "slash"),
// defaulting to prefix when unset or invalid.
func (m *DashboardModule) execMode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.ExecMode == "slash" {
		return "slash"
	}
	return "prefix"
}

// WebSetConfig applies one WebConfigurable field to the dashboard config.
func (m *DashboardModule) WebSetConfig(guildID, key, value string) error {
	if guildID != "" {
		return fmt.Errorf("dashboard config is global-only")
	}
	// listen & public_url live in the core bot config.yml `dashboard:` section
	// (so the owner can pin the port from the main config — e.g. when 8080 is
	// taken and the web UI can't start). The OAuth client_secret lives in core
	// config's `oauth:` section (the single shared Discord-app credential). Only
	// client_id, session_secret, and allowed_guilds stay in the 0600 module file.
	switch key {
	case "listen":
		if err := m.bot.SetConfig("dashboard_listen", value); err != nil {
			return err
		}
		m.invalidateCoreConfig()
		m.rebindSoon(key)
		return nil
	case "public_url":
		if err := m.bot.SetConfig("dashboard_public_url", value); err != nil {
			return err
		}
		m.invalidateCoreConfig()
		m.rebindSoon(key)
		return nil
	case "client_secret":
		if err := m.bot.SetConfig("oauth_client_secret", value); err != nil {
			return err
		}
		m.invalidateCoreConfig()
		m.refreshOAuth()
		m.sessions.clear() // invalidate existing sessions (secret changed)
		return nil
	case "allowed_guilds":
		// Web UI sends "Name (ID)" labels, newline-separated (newline keeps
		// commas legal inside guild names); convert back to IDs before the
		// generic Set path (which splits on comma/space).
		value = strings.Join(parseGuildLabels(strings.Split(value, "\n")), ",")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.cfg.Set(key, value); err != nil {
		return err
	}
	if err := m.cfg.Save(m.dataDir); err != nil {
		return err
	}
	switch key {
	case "client_id", "session_secret":
		m.refreshOAuthLocked()
		m.sessions.clear() // invalidate existing sessions (secret changed)
	}
	return nil
}

// rebindSoon restarts the HTTP server in a background goroutine so the request
// that triggered the config change (a web POST or a [p]dashboard set) can
// finish before the listener is rebound on the new address. If the module is
// unloaded before the goroutine runs, startServer refuses via the stopped
// flag, so an unload can never leave a zombie server behind.
func (m *DashboardModule) rebindSoon(key string) {
	go func() {
		if err := m.restartServer(); err != nil {
			m.logger.Error("Dashboard restart after %s change failed: %v", key, err)
		}
	}()
}

// redactedIfSet masks a non-empty secret value.
func redactedIfSet(s string) string {
	if s == "" {
		return ""
	}
	return "••••••••"
}

// boolEmoji renders a boolean as a Discord-friendly check/cross.
func boolEmoji(b bool) string {
	if b {
		return "✅ Yes"
	}
	return "⛔ No"
}

// permMgr is a convenience accessor for the permission manager.
func (m *DashboardModule) permMgr() *permissions.Manager {
	return m.bot.GetPermissionManager()
}

