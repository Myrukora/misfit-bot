// Package mcpserver implements the bot's built-in Model Context Protocol
// (MCP) server. It is mounted at /mcp on the dashboard listener (the dashboard
// never imports this package — the handler is injected via dashboard.Deps.MCP)
// and authenticates clients with the bearer token stored in config.yml
// (mcp.token). The config is re-read on every request, so flipping
// mcp_enabled / mcp_token takes effect live without a restart — that is the
// kill switch.
package mcpserver

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/config"
	"github.com/misfit/bot/modules"
	"github.com/misfit/bot/updater"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/rest"
)

// Deps is everything the MCP server needs. It deliberately does not import the
// dashboard: ApplyCoreSetting is injected (it is dashboard.ApplyCoreSetting) so
// the write/execute tools can persist core config without a dashboard import.
type Deps struct {
	Bot  commands.Interface
	Rest rest.Rest
	// ConfigDir is the bot config dir; config.yml is read fresh per request so
	// mcp_enabled / mcp_token changes are live (the kill switch).
	ConfigDir string
	// LogDir + LogBase resolve the bot's log file (logging.file_path split into
	// dir + basename, e.g. "logs" + "bot").
	LogDir  string
	LogBase string
	Logger  modules.Logger
	// ApplyCoreSetting writes one core config key with full owner trust. Nil
	// until wired via SetApplyCoreSetting (the dashboard is constructed after
	// this server, so the setter runs right after dashboard.New).
	ApplyCoreSetting func(key, value string) error
}

// Server is the MCP server: one shared *mcp.Server (tools registered once)
// wrapped in a per-request auth middleware. It is stateless per request over
// the dashboard listener — no lifecycle of its own, it dies with the process.
type Server struct {
	deps Deps
	srv  *mcp.Server

	applyMu sync.RWMutex
	apply   func(key, value string) error
}

// New builds the MCP server and registers its tools.
func New(deps Deps) *Server {
	s := &Server{
		deps:  deps,
		apply: deps.ApplyCoreSetting,
	}
	s.srv = mcp.NewServer(&mcp.Implementation{
		Name:    "misfit-bot",
		Version: deps.Bot.GetVersion(),
	}, nil)
	registerTools(s)
	return s
}

// SetApplyCoreSetting wires the core-config writer after the dashboard is
// constructed (the dashboard owns ApplyCoreSetting and is built after this
// server, because dashboard.New needs the MCP handler in its Deps).
func (s *Server) SetApplyCoreSetting(fn func(key, value string) error) {
	s.applyMu.Lock()
	s.apply = fn
	s.applyMu.Unlock()
}

// applyCoreSetting calls the wired writer, or a clear error when it is not
// wired yet.
func (s *Server) applyCoreSetting(key, value string) error {
	s.applyMu.RLock()
	fn := s.apply
	s.applyMu.RUnlock()
	if fn == nil {
		return fmt.Errorf("core config writer not wired")
	}
	return fn(key, value)
}

// Handler returns the auth middleware wrapping the streamable-HTTP MCP
// handler. The dashboard mounts it at /mcp.
func (s *Server) Handler() http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return s.srv
	}, nil)
	return http.HandlerFunc(s.authMiddleware(mcpHandler))
}

// authMiddleware enforces the bearer token on every request. It re-reads
// config.yml per request so mcp_enabled / mcp_token changes are live — this is
// the kill switch (disabling mcp makes the next request 404).
func (s *Server) authMiddleware(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := config.Load(s.deps.ConfigDir)
		if err != nil {
			http.Error(w, "mcp: config load failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if !cfg.MCP.Enabled {
			http.Error(w, "mcp disabled", http.StatusNotFound)
			return
		}
		if cfg.MCP.Token == "" {
			http.Error(w, "MCP token not generated", http.StatusServiceUnavailable)
			return
		}
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(auth, prefix)
		// ConstantTimeCompare returns 0 on a length mismatch, so a wrong-length
		// token is rejected without a timing oracle.
		if subtle.ConstantTimeCompare([]byte(token), []byte(cfg.MCP.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	}
}

// textResult wraps a plain-text body as a single TextContent result.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// jsonResult wraps a value as a JSON-string TextContent result (structured data
// is shipped as a JSON string so the agent can parse it).
func jsonResult(v any) (*mcp.CallToolResult, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return textResult(string(out)), nil
}

// updater returns the bot's updater manager, nil-tolerant.
func (s *Server) updater() *updater.Manager {
	if u := s.deps.Bot.GetUpdater(); u != nil {
		if m, ok := u.(*updater.Manager); ok {
			return m
		}
	}
	return nil
}

// client returns the bot's disgo client, nil-tolerant.
func (s *Server) client() *bot.Client {
	if c := s.deps.Bot.GetClient(); c != nil {
		if cl, ok := c.(*bot.Client); ok {
			return cl
		}
	}
	return nil
}

// moduleManager returns the bot's module manager, nil-tolerant.
func (s *Server) moduleManager() *modules.Manager {
	if m := s.deps.Bot.GetModuleManager(); m != nil {
		if mm, ok := m.(*modules.Manager); ok {
			return mm
		}
	}
	return nil
}
