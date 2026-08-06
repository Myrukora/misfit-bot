package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/custombot/bot/commands"
	"github.com/custombot/bot/config"
	"github.com/custombot/bot/embed"
	"github.com/custombot/bot/internal/util"
	"github.com/custombot/bot/modules"
	"github.com/custombot/bot/permissions"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/oauth2"
	"github.com/disgoorg/snowflake/v2"

	_ "embed"
)

// DashboardModule is a hot-loadable .so plugin module that runs a MEE6-style,
// role-tiered web dashboard in-process with the Discord gateway.
//
// It implements modules.WebConfigurable to self-configure from the web and to
// prove the field-render round-trip end-to-end.
type DashboardModule struct {
	ctx      *modules.Context
	cfg      *DashboardConfig
	client   *bot.Client // cached *bot.Client from ctx.Bot.GetClient()
	oauth    *oauth2.Client
	stateCtl oauth2.StateController // reused across oauth2.Client rebuilds so in-flight logins survive secret changes
	sessions *sessionStore
	tmpl     *templateBundle

	srv     *http.Server
	running bool
	stopped bool           // set on unload: refuses any further startServer (kills in-flight rebindSoon)
	serveWG sync.WaitGroup // tracks the active Serve goroutine; stopServer waits on it
	mu      sync.Mutex     // guards Start/Stop, cfg swaps, running/stopped/srv/lastErr
	dataDir string
	logger  modules.Logger
	lastErr string // last server bind error, surfaced in [p]dashboard status when not running
}

func (m *DashboardModule) Name() string    { return "dashboard" }
func (m *DashboardModule) Version() string { return "1.0.0" }
func (m *DashboardModule) Description() string {
	return "MEE6-style web dashboard with Discord OAuth login, metrics, command catalog, and tiered config"
}
func (m *DashboardModule) Author() string                         { return "custombot" }
func (m *DashboardModule) Dependencies() []string                 { return nil }
func (m *DashboardModule) SlashCommands() []commands.SlashCommand { return nil }

func (m *DashboardModule) OnLoad(ctx *modules.Context) error {
	m.ctx = ctx
	m.logger = ctx.Logger
	m.dataDir = ctx.DataDir
	m.sessions = newSessionStore()
	m.stopped = false // fresh module struct, but be explicit

	// Resolve the raw disgo client for cache/gateway/metrics access.
	if c, ok := ctx.Bot.GetClient().(*bot.Client); ok {
		m.client = c
	}

	cfg, err := loadConfig(ctx.DataDir)
	if err != nil {
		return fmt.Errorf("load dashboard config: %w", err)
	}
	// Auto-derive the OAuth client_id from the bot application if missing.
	if cfg.ClientID == "" && m.client != nil {
		cfg.ClientID = m.client.ApplicationID.String()
		_ = cfg.Save(ctx.DataDir)
	}
	if err := cfg.ensureSessionSecret(ctx.DataDir); err != nil {
		return fmt.Errorf("init session secret: %w", err)
	}
	m.cfg = cfg
	m.refreshOAuth()

	tmpl, err := loadTemplates()
	if err != nil {
		return fmt.Errorf("load templates: %w", err)
	}
	m.tmpl = tmpl

	// Start the server best-effort: a bind failure (e.g. 127.0.0.1:8080 already
	// in use) MUST NOT fail OnLoad — otherwise the [p]dashboard command is
	// unavailable and the owner is locked out of fixing the port. We log the
	// error and keep the module loaded so [p]dashboard set/restart can recover.
	if err := m.startServer(); err != nil {
		m.mu.Lock()
		m.lastErr = fmt.Sprintf("%v", err)
		m.mu.Unlock()
		m.logger.Error("Dashboard HTTP server failed to bind %s (module stays loaded; fix with `[p]dashboard set listen <addr>` then `[p]dashboard restart`, or set dashboard.listen in config.yml): %v", m.effectiveListen(), err)
	} else {
		m.mu.Lock()
		m.lastErr = ""
		m.mu.Unlock()
	}
	m.logger.Info("Dashboard module loaded (listen=%s configured=%v running=%v)", m.effectiveListen(), m.configured(), m.isRunning())
	return nil
}

func (m *DashboardModule) OnUnload() error {
	// Mark stopped BEFORE stopping the server: any in-flight rebindSoon
	// goroutine will then be refused by startServer, so an unloaded module
	// can never leave a zombie HTTP server behind.
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
	m.stopServer()
	return nil
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
func (m *DashboardModule) coreConfig() *config.Config {
	if m.ctx == nil || m.ctx.Bot == nil {
		return nil
	}
	cfg, err := config.Load(m.ctx.Bot.GetConfigDir())
	if err != nil {
		return nil
	}
	return cfg
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

func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func (m *DashboardModule) startServer() error {
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
		return fmt.Errorf("module is stopped")
	}
	if m.running {
		return nil
	}
	if m.client == nil {
		return fmt.Errorf("bot client unavailable")
	}
	handler := m.buildHandler()
	srv := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ln, err := net.Listen("tcp", srv.Addr) // detect address-in-use immediately
	if err != nil {
		return fmt.Errorf("listen %s: %w", srv.Addr, err)
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

func (m *DashboardModule) restartServer() error {
	m.stopServer()
	return m.startServer()
}

func (m *DashboardModule) isRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// ── WebConfigurable (dogfood + live self-config) ──────────────────────────

func (m *DashboardModule) WebConfigSchema() []modules.ConfigField {
	return []modules.ConfigField{
		{Key: "listen", Label: "Listen Address", Help: "Address the dashboard HTTP server binds to. Default 127.0.0.1:8080 (localhost only); use 0.0.0.0:8080 to accept LAN connections.", Type: modules.FieldTypeText, Scope: "global", Placeholder: "127.0.0.1:8080"},
		{Key: "public_url", Label: "Public URL", Help: "Public base URL where the dashboard is reachable (used to build the OAuth redirect URI).", Type: modules.FieldTypeText, Scope: "global", Placeholder: "https://dashboard.example.com"},
		{Key: "client_id", Label: "OAuth Client ID", Help: "Discord application client ID. Auto-derived from the bot application if left empty.", Type: modules.FieldTypeText, Scope: "global"},
		{Key: "client_secret", Label: "OAuth Client Secret", Help: "Discord application client secret (from the Developer Portal OAuth2 page).", Type: modules.FieldTypeSecret, Scope: "global"},
		{Key: "session_secret", Label: "Session Secret", Help: "Secret used to sign session cookies. Auto-generated if empty.", Type: modules.FieldTypeSecret, Scope: "global"},
		{Key: "allowed_guilds", Label: "Allowed Guilds", Help: "Optional allowlist of guild IDs. Comma or whitespace separated. Empty = allow all bot guilds.", Type: modules.FieldTypeTextarea, Scope: "global"},
	}
}

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
		"listen":         m.effectiveListen(),
		"public_url":     m.effectivePublicURL(),
		"client_id":      cfg.ClientID,
		"client_secret":  redactedIfSet(m.effectiveClientSecret()),
		"session_secret": redactedIfSet(cfg.SessionSecret),
		"allowed_guilds": strings.Join(cfg.AllowedGuilds, ", "),
	}
	return v, nil
}

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
		if err := m.ctx.Bot.SetConfig("dashboard_listen", value); err != nil {
			return err
		}
		m.rebindSoon(key)
		return nil
	case "public_url":
		if err := m.ctx.Bot.SetConfig("dashboard_public_url", value); err != nil {
			return err
		}
		m.rebindSoon(key)
		return nil
	case "client_secret":
		if err := m.ctx.Bot.SetConfig("oauth_client_secret", value); err != nil {
			return err
		}
		m.refreshOAuth()
		m.sessions.clear() // invalidate existing sessions (secret changed)
		return nil
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

func redactedIfSet(s string) string {
	if s == "" {
		return ""
	}
	return "••••••••"
}

// ── [p]dashboard Discord command (owner-only bootstrap) ──────────────────

func (m *DashboardModule) Commands() []commands.Command {
	return []commands.Command{
		{
			Name:        "dashboard",
			Description: "Manage the web dashboard (owner setup: view status, get OAuth URL, set secrets, restart server)",
			Usage:       "dashboard <status|url|lan|set|restart>",
			Category:    "dashboard",
			OwnerOnly:   true,
			Execute: func(ctx *commands.Context) error {
				if len(ctx.Args) == 0 {
					return m.cmdDashboardUsage(ctx)
				}
				switch strings.ToLower(ctx.Args[0]) {
				case "status":
					return m.cmdDashboardStatus(ctx)
				case "url":
					return m.cmdDashboardURL(ctx)
				case "set":
					return m.cmdDashboardSet(ctx, ctx.Args[1:])
				case "lan":
					return m.cmdDashboardLAN(ctx)
				case "restart":
					return m.cmdDashboardRestart(ctx)
				default:
					return m.cmdDashboardUsage(ctx)
				}
			},
		},
	}
}

func (m *DashboardModule) cmdDashboardUsage(ctx *commands.Context) error {
	p := m.ctx.Bot.GetPrefix()
	return ctx.Respond(embed.Info("📊 Dashboard", "Usage:\n`"+p+"dashboard status` — show status\n`"+p+"dashboard url` — show login URL & redirect URI\n`"+p+"dashboard lan` — bind all interfaces + show LAN URL\n`"+p+"dashboard set <key> <value>` — set config\n`"+p+"dashboard restart` — restart the HTTP server\n\nKeys: `listen, public_url, client_id, client_secret, session_secret, allowed_guilds`"))
}

func (m *DashboardModule) cmdDashboardStatus(ctx *commands.Context) error {
	m.mu.Lock()
	lastErr := m.lastErr
	m.mu.Unlock()
	running := m.isRunning()
	listen := m.effectiveListen()
	publicURL := m.effectivePublicURL()
	fields := []discord.EmbedField{
		{Name: "Listen", Value: "`" + listen + "`", Inline: nil},
		{Name: "Public URL", Value: "`" + publicURL + "`", Inline: nil},
		{Name: "LAN URL", Value: "`" + m.lanURL() + "`", Inline: util.PtrBool(false)},
		{Name: "Redirect URI", Value: "`" + m.effectiveBaseURL() + "/callback`", Inline: util.PtrBool(false)},
		{Name: "Configured", Value: boolEmoji(m.configured()), Inline: nil},
		{Name: "Running", Value: boolEmoji(running), Inline: nil},
	}
	if !running && lastErr != "" {
		fields = append(fields, discord.EmbedField{Name: "❌ Last Error", Value: lastErr, Inline: nil})
	}
	e := embed.New().WithTitle("📊 Dashboard Status").WithColor(embed.ColorInfo).WithFields(fields...).WithTimestamp(time.Now())
	return ctx.Respond(e)
}

func (m *DashboardModule) cmdDashboardURL(ctx *commands.Context) error {
	prefix := m.ctx.Bot.GetPrefix()
	if publicURL := m.effectivePublicURL(); publicURL != "" {
		login := publicURL + "/login"
		redirect := publicURL + "/callback"
		// A localhost-only public_url is only reachable from the server itself
		// — point the owner at the LAN path instead of leaving them confused.
		hint := ""
		if u, err := url.Parse(publicURL); err == nil && isLoopbackHost(u.Hostname()) {
			hint = "\n\n⚠️ `" + publicURL + "` is localhost-only — it is only reachable from this machine. For LAN access run `" + prefix + "dashboard lan`, or set `public_url` to an address other devices can reach."
		}
		return ctx.Respond(embed.Info("📊 Dashboard URLs", "Login: "+login+"\nOAuth redirect URI to register in the Developer Portal:\n`"+redirect+"`"+hint+"\n\n1) Add that redirect URI under OAuth2 → Redirects.\n2) Create a client secret and run:\n`"+prefix+"dashboard set client_secret <secret>`\n3) Restart: `"+prefix+"dashboard restart`"))
	}
	base := m.lanURL()
	note := ""
	if m.loopbackOnlyListen() {
		note = "\n\n⚠️ The server currently listens on `" + m.effectiveListen() + "` (this machine only). Run `" + prefix + "dashboard lan` to bind all interfaces so other LAN devices can reach it."
	}
	return ctx.Respond(embed.Info("📊 Dashboard URLs (LAN)", "LAN URL: "+base+"\nOAuth redirect URI to register in the Developer Portal:\n`"+base+"/callback`"+note+"\n\n⚠️ Discord requires HTTPS redirect URIs outside localhost. If the Developer Portal rejects this `http://` URI, expose the dashboard through a tunnel (cloudflared) and run:\n`"+prefix+"dashboard set public_url https://your.host`\nThen register `https://your.host/callback` and log in from anywhere."))
}

func (m *DashboardModule) cmdDashboardSet(ctx *commands.Context, args []string) error {
	if len(args) < 1 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "`dashboard set <key> <value>`\nKeys: listen, public_url, client_id, client_secret, session_secret, allowed_guilds\n\n`listen` and `public_url` → config.yml `dashboard:` section.\n`client_secret` → config.yml `oauth:` section (the shared Discord-app credential).\n`client_id`, `session_secret`, `allowed_guilds` → the dashboard module's own config file."))
	}
	key := args[0]
	value := ""
	if len(args) > 1 {
		value = strings.Join(args[1:], " ")
	}
	// listen & public_url are pinned in the core config.yml `dashboard:` section
	// so they can be set without loading the web UI (e.g. when the default
	// 8080 port is already taken). client_secret is pinned in the core config
	// `oauth:` section — the single shared Discord-app credential readable by
	// this dashboard and any future OAuth-using module. These three are routed
	// to the core config BEFORE the m.mu.Lock below; refreshOAuth reads the
	// secret without grabbing m.mu, so no self-deadlock.
	switch key {
	case "listen", "public_url":
		if err := m.ctx.Bot.SetConfig("dashboard_"+key, value); err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		m.rebindSoon(key)
		return ctx.Respond(embed.Success("✅ Set", "`"+key+"` written to config.yml — rebinding listener on `"+m.effectiveListen()+"`."))
	case "client_secret":
		if err := m.ctx.Bot.SetConfig("oauth_client_secret", value); err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		m.refreshOAuth()
		m.sessions.clear() // invalidate existing sessions (secret changed)
		return ctx.Respond(embed.Success("✅ Set", "`client_secret` written to config.yml (`oauth:` section). Future OAuth-using modules read it from there too."))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.cfg.Set(key, value); err != nil {
		return ctx.Respond(embed.Error("❌ Error", err.Error()))
	}
	if err := m.cfg.Save(m.dataDir); err != nil {
		return ctx.Respond(embed.Error("❌ Error", "save failed: "+err.Error()))
	}
	switch key {
	case "client_id", "session_secret":
		m.refreshOAuthLocked()
		m.sessions.clear()
	}
	return ctx.Respond(embed.Success("✅ Set", "`"+key+"` updated. Restart the dashboard if you changed OAuth config."))
}

// cmdDashboardLAN switches the dashboard to LAN mode in one step: bind all
// interfaces (0.0.0.0:<port>), drop a stale localhost-only public_url so the
// reported URL is the LAN URL, rebind the listener, and print where to go.
// A real (non-localhost) public_url is kept — it takes priority for OAuth
// redirects and the message reflects that.
func (m *DashboardModule) cmdDashboardLAN(ctx *commands.Context) error {
	prefix := m.ctx.Bot.GetPrefix()
	target := "0.0.0.0:" + m.listenPort()
	if err := m.ctx.Bot.SetConfig("dashboard_listen", target); err != nil {
		return ctx.Respond(embed.Error("❌ Error", err.Error()))
	}
	// A localhost-only public_url would keep url/status reporting a URL that
	// only works on this machine; a real public_url (tunnel/domain) is kept.
	// The listener rebind happens only after every config update succeeded.
	if u := m.effectivePublicURL(); u != "" {
		if parsed, err := url.Parse(u); err == nil && isLoopbackHost(parsed.Hostname()) {
			if err := m.ctx.Bot.SetConfig("dashboard_public_url", ""); err != nil {
				return ctx.Respond(embed.Error("❌ Error", err.Error()))
			}
		}
	}
	m.rebindSoon("listen")
	if u := m.effectivePublicURL(); u != "" {
		return ctx.Respond(embed.Success("✅ LAN mode", "Dashboard now listens on **all interfaces** (`"+target+"`).\n\nA `public_url` is configured (`"+u+"`) and takes priority for OAuth login — its redirect URI is:\n`"+u+"/callback`\n\nLAN devices can reach the dashboard at `"+m.lanURL()+"`; login redirects through `public_url`."))
	}
	base := m.lanURL()
	return ctx.Respond(embed.Success("✅ LAN mode", "Dashboard now listens on **all interfaces** (`"+target+"`).\n\n**LAN URL:** "+base+"\n\nRegister this redirect URI in the Developer Portal:\n`"+base+"/callback`\n\n⚠️ Discord requires HTTPS redirect URIs outside localhost. If the portal rejects the `http://` URI, expose the dashboard via a tunnel (cloudflared) and run `"+prefix+"dashboard set public_url https://your.host` instead."))
}

func (m *DashboardModule) cmdDashboardRestart(ctx *commands.Context) error {
	if err := m.restartServer(); err != nil {
		return ctx.Respond(embed.Error("❌ Error", "restart failed: "+err.Error()))
	}
	return ctx.Respond(embed.Success("✅ Dashboard", "HTTP server restarted on `"+m.effectiveListen()+"`."))
}

func boolEmoji(b bool) string {
	if b {
		return "✅ Yes"
	}
	return "⛔ No"
}

// permMgr is a convenience accessor for the permission manager.
func (m *DashboardModule) permMgr() *permissions.Manager {
	return m.ctx.Bot.GetPermissionManager()
}

// New is the exported plugin entry point.
func New() modules.Module { return &DashboardModule{} }
