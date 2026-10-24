package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/config"
	"github.com/misfit/bot/embed"
	"github.com/misfit/bot/modules"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// ── fakes ──────────────────────────────────────────────────────────────────

// testLogger is a no-op modules.Logger.
type testLogger struct{}

func (testLogger) Debug(string, ...any) {}
func (testLogger) Info(string, ...any)  {}
func (testLogger) Warn(string, ...any)  {}
func (testLogger) Error(string, ...any) {}

// execCall records one ExecuteCommand invocation.
type execCall struct {
	name      string
	args      []string
	guildID   string
	channelID string
	asUserID  string
}

// fakeBot implements the commands.Interface methods the MCP server uses. The
// embedded nil interface covers the rest (never called in these tests).
type fakeBot struct {
	commands.Interface
	ownerID    string
	version    string
	startTime  time.Time
	latency    string
	loaded     []string
	available  []string
	moduleCmds []commands.ModuleSlashCommands
	manager    *modules.Manager
	updater    interface{}
	channels   map[string]discord.GuildChannel
	execCalls  []execCall
	execResult commands.CommandResult
	execErr    error
}

func (f *fakeBot) GetOwnerID() string                { return f.ownerID }
func (f *fakeBot) GetVersion() string                { return f.version }
func (f *fakeBot) GetStartTime() time.Time           { return f.startTime }
func (f *fakeBot) GetLatency() string                { return f.latency }
func (f *fakeBot) GetClient() interface{}            { return nil }
func (f *fakeBot) GetLoadedModuleNames() []string    { return f.loaded }
func (f *fakeBot) GetAvailableModuleNames() []string { return f.available }
func (f *fakeBot) GetAllModuleSlashCommandsByModule() []commands.ModuleSlashCommands {
	return f.moduleCmds
}
func (f *fakeBot) GetModuleManager() interface{} { return f.manager }
func (f *fakeBot) GetUpdater() interface{}       { return f.updater }
func (f *fakeBot) GetCachedChannel(id string) discord.GuildChannel {
	return f.channels[id]
}
func (f *fakeBot) LoadModule(string) error   { return nil }
func (f *fakeBot) UnloadModule(string) error { return nil }
func (f *fakeBot) ReloadModule(string) error { return nil }
func (f *fakeBot) ExecuteCommand(name string, args []string, guildID, channelID, asUserID string) (commands.CommandResult, error) {
	f.execCalls = append(f.execCalls, execCall{name, args, guildID, channelID, asUserID})
	return f.execResult, f.execErr
}

// fakeRest records CreateMessage calls and backs the message read/edit/delete
// tools with an in-memory store keyed by message ID.
type fakeRest struct {
	rest.Rest
	channelIDs []snowflake.ID
	messages   []discord.MessageCreate
	store      map[snowflake.ID]*discord.Message
}

func (f *fakeRest) CreateMessage(channelID snowflake.ID, mc discord.MessageCreate, _ ...rest.RequestOpt) (*discord.Message, error) {
	f.channelIDs = append(f.channelIDs, channelID)
	f.messages = append(f.messages, mc)
	return &discord.Message{ID: snowflake.MustParse("999"), ChannelID: channelID, CreatedAt: time.Now()}, nil
}

func (f *fakeRest) GetMessages(channelID snowflake.ID, around, before, after snowflake.ID, limit int, _ ...rest.RequestOpt) ([]discord.Message, error) {
	out := make([]discord.Message, 0, len(f.store))
	for _, m := range f.store {
		if m.ChannelID != channelID {
			continue
		}
		if before != 0 && m.ID >= before {
			continue
		}
		if after != 0 && m.ID <= after {
			continue
		}
		out = append(out, *m)
	}
	// Discord returns newest-first; the map iteration above is unordered, so
	// sort by ID (snowflakes are time-ordered) and then take the newest N.
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeRest) GetMessage(channelID snowflake.ID, messageID snowflake.ID, _ ...rest.RequestOpt) (*discord.Message, error) {
	m, ok := f.store[messageID]
	if !ok || m.ChannelID != channelID {
		return nil, fmt.Errorf("Unknown Message (10008)")
	}
	return m, nil
}

func (f *fakeRest) UpdateMessage(channelID snowflake.ID, messageID snowflake.ID, update discord.MessageUpdate, _ ...rest.RequestOpt) (*discord.Message, error) {
	m, ok := f.store[messageID]
	if !ok || m.ChannelID != channelID {
		return nil, fmt.Errorf("Unknown Message (10008)")
	}
	if update.Content != nil {
		m.Content = *update.Content
	}
	return m, nil
}

func (f *fakeRest) DeleteMessage(channelID snowflake.ID, messageID snowflake.ID, _ ...rest.RequestOpt) error {
	m, ok := f.store[messageID]
	if !ok || m.ChannelID != channelID {
		return fmt.Errorf("Unknown Message (10008)")
	}
	delete(f.store, messageID)
	return nil
}

// ── setup ──────────────────────────────────────────────────────────────────

// writeConfig writes a config.yml with the given mcp section into dir.
func writeConfig(t *testing.T, dir, mcpBlock string) {
	t.Helper()
	yaml := "bot:\n  name: \"Bot\"\n  owner_id: \"123\"\n" + mcpBlock
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newTestServer builds the real Handler() over an httptest.Server with fakes.
func newTestServer(t *testing.T) (*Server, *httptest.Server, *fakeBot, *fakeRest, string) {
	t.Helper()
	dir := t.TempDir()
	writeConfig(t, dir, "mcp:\n  enabled: true\n  token: \"testtoken\"\n")

	fb := &fakeBot{
		ownerID:   "123",
		version:   "test-version",
		startTime: time.Now().Add(-time.Hour),
		latency:   "5ms",
		loaded:    []string{"cleanup"},
		available: []string{"cleanup", "tickets"},
		manager:   modules.NewManager(),
		channels:  map[string]discord.GuildChannel{"456": discord.GuildTextChannel{}},
	}
	fr := &fakeRest{store: map[snowflake.ID]*discord.Message{}}
	s := New(Deps{
		Bot:       fb,
		Rest:      fr,
		ConfigDir: dir,
		LogDir:    dir,
		LogBase:   "bot",
		Logger:    testLogger{},
		ApplyCoreSetting: func(key, value string) error {
			cfg, err := config.Load(dir)
			if err != nil {
				return err
			}
			return cfg.Set(key, value)
		},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts, fb, fr, dir
}

// authClient returns an http.Client that injects the bearer token on every
// request (the SDK transport has no direct header option, so the RoundTripper
// is the injection point).
func authClient(token string) *http.Client {
	return &http.Client{Transport: &authRoundTripper{inner: http.DefaultTransport, token: token}}
}

type authRoundTripper struct {
	inner http.RoundTripper
	token string
}

func (a *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+a.token)
	return a.inner.RoundTrip(req)
}

// connectClient runs the SDK client Initialize against the test server.
func connectClient(t *testing.T, ts *httptest.Server, token string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             ts.URL,
		HTTPClient:           authClient(token),
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// toolText extracts the first TextContent from a CallToolResult.
func toolText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("no content in tool result")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("first content is not TextContent: %T", res.Content[0])
	}
	return tc.Text
}

// ── tests ──────────────────────────────────────────────────────────────────

func TestListTools(t *testing.T) {
	_, ts, _, _, _ := newTestServer(t)
	session := connectClient(t, ts, "testtoken")
	ctx := context.Background()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := []string{
		"bot_status", "list_guilds", "list_channels", "get_logs", "list_commands",
		"get_config", "module_get_config", "set_config", "module_set_config",
		"run_command", "send_message", "module_action", "update_action",
		"get_messages", "get_message", "edit_message", "delete_message",
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing tool %q (got %d tools)", name, len(res.Tools))
		}
	}
	if len(res.Tools) != len(want) {
		t.Errorf("expected %d tools, got %d", len(want), len(res.Tools))
	}
}

func TestRunCommand(t *testing.T) {
	_, ts, fb, _, _ := newTestServer(t)
	fb.execResult = commands.CommandResult{Title: "Pong", Description: "pong", Color: embed.ColorSuccess}
	session := connectClient(t, ts, "testtoken")
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "ping"},
	})
	if err != nil {
		t.Fatalf("CallTool run_command: %v", err)
	}
	if res.IsError {
		t.Fatalf("run_command returned an error: %s", toolText(t, res))
	}
	if len(fb.execCalls) != 1 {
		t.Fatalf("expected 1 ExecuteCommand call, got %d", len(fb.execCalls))
	}
	call := fb.execCalls[0]
	if call.name != "ping" {
		t.Errorf("ExecuteCommand name = %q, want ping", call.name)
	}
	if call.asUserID != "123" {
		t.Errorf("ExecuteCommand asUserID = %q, want 123 (fake owner)", call.asUserID)
	}
	text := toolText(t, res)
	if !strings.Contains(text, "Pong") {
		t.Errorf("result text %q does not contain Pong", text)
	}
}

func TestSendMessage(t *testing.T) {
	_, ts, _, fr, _ := newTestServer(t)
	session := connectClient(t, ts, "testtoken")
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "send_message",
		Arguments: map[string]any{"channel_id": "456", "content": "hello from mcp"},
	})
	if err != nil {
		t.Fatalf("CallTool send_message: %v", err)
	}
	if res.IsError {
		t.Fatalf("send_message returned an error: %s", toolText(t, res))
	}
	if len(fr.messages) != 1 {
		t.Fatalf("expected 1 CreateMessage call, got %d", len(fr.messages))
	}
	if fr.messages[0].Content != "hello from mcp" {
		t.Errorf("CreateMessage content = %q, want %q", fr.messages[0].Content, "hello from mcp")
	}
	if fr.channelIDs[0].String() != "456" {
		t.Errorf("CreateMessage channelID = %q, want 456", fr.channelIDs[0].String())
	}
}

// ── chat read/write tool tests ─────────────────────────────────────────────

// seedMessages populates the fake REST store with n messages in channel "456"
// (IDs 1000+i), newest last, so pagination order is deterministic.
func seedMessages(fr *fakeRest, n int) {
	for i := 1; i <= n; i++ {
		id := snowflake.MustParse(fmt.Sprintf("1000%d", i))
		fr.store[id] = &discord.Message{
			ID:        id,
			ChannelID: snowflake.MustParse("456"),
			Content:   fmt.Sprintf("msg %d", i),
			CreatedAt: time.Unix(int64(i), 0),
			Author: discord.User{
				ID: snowflake.MustParse("111"), Username: "alice", Bot: false,
			},
		}
	}
}

func TestGetMessages(t *testing.T) {
	_, ts, _, fr, _ := newTestServer(t)
	seedMessages(fr, 5)
	session := connectClient(t, ts, "testtoken")
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_messages",
		Arguments: map[string]any{"channel_id": "456", "limit": 3},
	})
	if err != nil {
		t.Fatalf("CallTool get_messages: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_messages returned an error: %s", toolText(t, res))
	}
	var views []messageView
	if err := json.Unmarshal([]byte(toolText(t, res)), &views); err != nil {
		t.Fatalf("get_messages result not JSON: %v", err)
	}
	if len(views) != 3 {
		t.Fatalf("got %d messages, want 3", len(views))
	}
	// Newest-first.
	if views[0].Content != "msg 5" || views[2].Content != "msg 3" {
		t.Errorf("get_messages order = %q,%q,%q, want msg5,msg4,msg3", views[0].Content, views[1].Content, views[2].Content)
	}
	if views[0].Author.Username != "alice" {
		t.Errorf("author = %q, want alice", views[0].Author.Username)
	}
}

func TestGetMessagesPaging(t *testing.T) {
	_, ts, _, fr, _ := newTestServer(t)
	seedMessages(fr, 5)
	session := connectClient(t, ts, "testtoken")
	ctx := context.Background()

	// Page backwards: everything strictly older than message 10003 (msg3).
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_messages",
		Arguments: map[string]any{"channel_id": "456", "before_id": "10003"},
	})
	if err != nil {
		t.Fatalf("CallTool get_messages(page): %v", err)
	}
	if res.IsError {
		t.Fatalf("get_messages(page) returned an error: %s", toolText(t, res))
	}
	var views []messageView
	if err := json.Unmarshal([]byte(toolText(t, res)), &views); err != nil {
		t.Fatalf("result not JSON: %v", err)
	}
	if len(views) != 2 || views[0].Content != "msg 2" || views[1].Content != "msg 1" {
		t.Errorf("before_id paging = %+v, want [msg2 msg1] newest-first", views)
	}
}

func TestGetMessageSingle(t *testing.T) {
	_, ts, _, fr, _ := newTestServer(t)
	seedMessages(fr, 3)
	session := connectClient(t, ts, "testtoken")
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_message",
		Arguments: map[string]any{"channel_id": "456", "message_id": "10002"},
	})
	if err != nil {
		t.Fatalf("CallTool get_message: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_message returned an error: %s", toolText(t, res))
	}
	var v messageView
	if err := json.Unmarshal([]byte(toolText(t, res)), &v); err != nil {
		t.Fatalf("result not JSON: %v", err)
	}
	if v.ID != "10002" || v.Content != "msg 2" {
		t.Errorf("get_message = %+v, want 10002/msg 2", v)
	}
}

func TestEditMessage(t *testing.T) {
	_, ts, _, fr, _ := newTestServer(t)
	seedMessages(fr, 2)
	session := connectClient(t, ts, "testtoken")
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "edit_message",
		Arguments: map[string]any{
			"channel_id": "456", "message_id": "10001", "content": "edited!",
		},
	})
	if err != nil {
		t.Fatalf("CallTool edit_message: %v", err)
	}
	if res.IsError {
		t.Fatalf("edit_message returned an error: %s", toolText(t, res))
	}
	if got := fr.store[snowflake.MustParse("10001")].Content; got != "edited!" {
		t.Errorf("stored content = %q, want edited!", got)
	}
}

func TestDeleteMessage(t *testing.T) {
	_, ts, _, fr, _ := newTestServer(t)
	seedMessages(fr, 2)
	session := connectClient(t, ts, "testtoken")
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "delete_message",
		Arguments: map[string]any{"channel_id": "456", "message_id": "10001"},
	})
	if err != nil {
		t.Fatalf("CallTool delete_message: %v", err)
	}
	if res.IsError {
		t.Fatalf("delete_message returned an error: %s", toolText(t, res))
	}
	if _, ok := fr.store[snowflake.MustParse("10001")]; ok {
		t.Error("message still in store after delete_message")
	}
}

func TestKillSwitch(t *testing.T) {
	_, ts, _, _, dir := newTestServer(t)
	session := connectClient(t, ts, "testtoken")
	ctx := context.Background()

	// Flipping mcp_enabled=false via set_config must make the next request 404.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "set_config",
		Arguments: map[string]any{"key": "mcp_enabled", "value": "false"},
	})
	if err != nil {
		t.Fatalf("CallTool set_config: %v", err)
	}
	if res.IsError {
		t.Fatalf("set_config returned an error: %s", toolText(t, res))
	}

	// The next request (a fresh connect) must 404 — the kill switch is live.
	writeConfig(t, dir, "mcp:\n  enabled: false\n  token: \"testtoken\"\n")
	req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer testtoken")
	req.Header.Set("Content-Type", "application/json")
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request after disable: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusNotFound {
		t.Errorf("status after disable = %d, want 404", httpResp.StatusCode)
	}
}

func TestAuthDirectHTTP(t *testing.T) {
	_, ts, _, _, dir := newTestServer(t)

	do := func(token string) int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(`{}`))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := do(""); code != http.StatusUnauthorized {
		t.Errorf("missing token: status = %d, want 401", code)
	}
	if code := do("garbage-not-a-token"); code != http.StatusUnauthorized {
		t.Errorf("garbage token: status = %d, want 401", code)
	}
	if code := do("wrongtoken"); code != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", code)
	}

	// Disabled → 404.
	writeConfig(t, dir, "mcp:\n  enabled: false\n  token: \"testtoken\"\n")
	if code := do("testtoken"); code != http.StatusNotFound {
		t.Errorf("disabled: status = %d, want 404", code)
	}

	// Empty token → 503.
	writeConfig(t, dir, "mcp:\n  enabled: true\n  token: \"\"\n")
	if code := do("testtoken"); code != http.StatusServiceUnavailable {
		t.Errorf("empty token: status = %d, want 503", code)
	}
}

// TestBotStatusNilClient asserts bot_status degrades gracefully with a nil
// client (version + uptime still appear, no panic).
func TestBotStatusNilClient(t *testing.T) {
	_, ts, _, _, _ := newTestServer(t)
	session := connectClient(t, ts, "testtoken")
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "bot_status"})
	if err != nil {
		t.Fatalf("CallTool bot_status: %v", err)
	}
	if res.IsError {
		t.Fatalf("bot_status returned an error: %s", toolText(t, res))
	}
	text := toolText(t, res)
	if !strings.Contains(text, "test-version") {
		t.Errorf("bot_status text %q missing version", text)
	}
	if !strings.Contains(text, "uptime:") {
		t.Errorf("bot_status text %q missing uptime", text)
	}
}

// TestGetConfigRedactsSecrets asserts get_config redacts the token + mcp_token.
func TestGetConfigRedactsSecrets(t *testing.T) {
	_, ts, _, _, _ := newTestServer(t)
	session := connectClient(t, ts, "testtoken")
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_config"})
	if err != nil {
		t.Fatalf("CallTool get_config: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_config returned an error: %s", toolText(t, res))
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(toolText(t, res)), &m); err != nil {
		t.Fatalf("get_config result not JSON: %v", err)
	}
	if m["mcp_token"] != "••••••••" {
		t.Errorf("mcp_token = %q, want redacted", m["mcp_token"])
	}
	if m["mcp_enabled"] != "true" {
		t.Errorf("mcp_enabled = %q, want true", m["mcp_enabled"])
	}
}
