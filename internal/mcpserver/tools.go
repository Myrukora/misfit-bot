package mcpserver

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/config"
	"github.com/misfit/bot/embed"
	"github.com/misfit/bot/internal/logutil"
	"github.com/misfit/bot/modules"
	"github.com/misfit/bot/updater"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// registerTools wires the 13 MCP tools onto the shared server. Every input
// field carries a jsonschema doc tag so the SDK infers a documented input
// schema for the agent.
func registerTools(s *Server) {
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "bot_status",
		Description: "Report the bot's version, latest version + update availability, uptime, gateway latency, cached guild/member/channel/role counts, loaded vs available module counts, and goroutine count.",
	}, s.botStatus)

	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "list_guilds",
		Description: "List the guilds in the bot's cache as a JSON array of {id, name, member_count}.",
	}, s.listGuilds)

	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "list_channels",
		Description: "List a guild's channels and roles as JSON {channels:[{id,name,type}], roles:[{id,name,position}]}.",
	}, s.listChannels)

	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "get_logs",
		Description: "Return the tail of the bot's log file as a JSON array of raw log lines.",
	}, s.getLogs)

	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "list_commands",
		Description: "List every registered bot command, grouped by module (core commands first), as text: name — description (usage: …).",
	}, s.listCommands)

	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "get_config",
		Description: "Return the bot's core config as a JSON map of key → value, with secrets redacted.",
	}, s.getConfig)

	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "module_get_config",
		Description: "Return a module's dashboard config values as a JSON map. guild_id empty = global scope.",
	}, s.moduleGetConfig)

	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "set_config",
		Description: "Set one core config key (owner-trusted). The value is validated by the bot's config writer.",
	}, s.setConfig)

	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "module_set_config",
		Description: "Set one key in a module's dashboard config. guild_id empty = global scope.",
	}, s.moduleSetConfig)

	mcp.AddTool(s.srv, &mcp.Tool{
		Name: "run_command",
		Description: "Execute any registered bot command through the bot's internal dispatcher with a virtual context — " +
			"nothing is posted to Discord; the command's captured response is returned. Use list_commands first to " +
			"discover names. guild_id/channel_id give the command its context (cleanup subcommands need channel_id).",
	}, s.runCommand)

	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "send_message",
		Description: "Send a message (plain text and/or an embed) to a channel. Returns the new message ID.",
	}, s.sendMessage)

	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "module_action",
		Description: "Load, unload, or reload a module. action is one of: load, unload, reload.",
	}, s.moduleAction)

	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "update_action",
		Description: "Drive the self-updater. action is one of: status, check, apply, test.",
	}, s.updateAction)
}

// ── read tools ─────────────────────────────────────────────────────────────

// botStatus reports a text summary of bot + runtime state. Nil-tolerant: a
// missing client/updater degrades the relevant lines rather than failing.
func (s *Server) botStatus(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
	var b strings.Builder
	b.WriteString("version: " + s.deps.Bot.GetVersion() + "\n")
	if u := s.updater(); u != nil {
		if latest := u.LatestVersion(); latest != "" {
			if updater.Ahead(s.deps.Bot.GetVersion(), latest) {
				b.WriteString("latest version: " + latest + " (update available)\n")
			} else {
				b.WriteString("latest version: " + latest + "\n")
			}
		}
	}
	b.WriteString("uptime: " + time.Since(s.deps.Bot.GetStartTime()).Round(time.Second).String() + "\n")
	b.WriteString("gateway latency: " + s.deps.Bot.GetLatency() + "\n")
	if c := s.client(); c != nil {
		b.WriteString(fmt.Sprintf("cached: %d guilds, %d members, %d channels, %d roles\n",
			c.Caches.GuildsLen(), c.Caches.MembersAllLen(), c.Caches.ChannelsLen(), c.Caches.RolesAllLen()))
	}
	b.WriteString(fmt.Sprintf("modules: %d loaded, %d available\n",
		len(s.deps.Bot.GetLoadedModuleNames()), len(s.deps.Bot.GetAvailableModuleNames())))
	b.WriteString("goroutines: " + strconv.Itoa(runtime.NumGoroutine()) + "\n")
	return textResult(b.String()), nil, nil
}

// listGuilds returns the cached guilds as a JSON array.
func (s *Server) listGuilds(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
	c := s.client()
	if c == nil {
		return nil, nil, fmt.Errorf("bot client not ready")
	}
	type guildView struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		MemberCount int    `json:"member_count"`
	}
	var out []guildView
	for g := range c.Caches.Guilds() {
		out = append(out, guildView{ID: g.ID.String(), Name: g.Name, MemberCount: g.MemberCount})
	}
	res, err := jsonResult(out)
	return res, nil, err
}

// ListChannelsIn is the input for list_channels.
type ListChannelsIn struct {
	GuildID string `json:"guild_id" jsonschema:"The guild ID to list channels and roles for"`
}

// listChannels returns a guild's channels + roles as JSON. The guild id is
// parsed (and rejected if not a snowflake) before any cache use, and the guild
// must exist in the cache. Mirrors the dashboard's apiGuild.
func (s *Server) listChannels(ctx context.Context, req *mcp.CallToolRequest, in *ListChannelsIn) (*mcp.CallToolResult, any, error) {
	c := s.client()
	if c == nil {
		return nil, nil, fmt.Errorf("bot client not ready")
	}
	gid, err := snowflake.Parse(in.GuildID)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid guild id: %s", in.GuildID)
	}
	if _, ok := c.Caches.Guild(gid); !ok {
		return nil, nil, fmt.Errorf("guild not in cache: %s", in.GuildID)
	}
	type channelView struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	type roleView struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Position int    `json:"position"`
	}
	out := struct {
		Channels []channelView `json:"channels"`
		Roles    []roleView    `json:"roles"`
	}{}
	for ch := range c.Caches.ChannelsForGuild(gid) {
		out.Channels = append(out.Channels, channelView{ID: ch.ID().String(), Name: ch.Name(), Type: channelTypeName(ch.Type())})
	}
	sort.Slice(out.Channels, func(i, j int) bool { return out.Channels[i].Name < out.Channels[j].Name })
	for role := range c.Caches.Roles(gid) {
		out.Roles = append(out.Roles, roleView{ID: role.ID.String(), Name: role.Name, Position: role.Position})
	}
	sort.Slice(out.Roles, func(i, j int) bool { return out.Roles[i].Position > out.Roles[j].Position })
	res, err := jsonResult(out)
	return res, nil, err
}

// channelTypeName renders a channel type as a short human label. Copied from
// the dashboard (internal/dashboard/api.go) — deliberately not shared, so the
// two packages stay independent.
func channelTypeName(t discord.ChannelType) string {
	switch t {
	case discord.ChannelTypeGuildText:
		return "Text"
	case discord.ChannelTypeGuildVoice:
		return "Voice"
	case discord.ChannelTypeGuildCategory:
		return "Category"
	case discord.ChannelTypeGuildNews:
		return "Announcement"
	case discord.ChannelTypeGuildStageVoice:
		return "Stage"
	case discord.ChannelTypeGuildForum:
		return "Forum"
	default:
		return "Other"
	}
}

// GetLogsIn is the input for get_logs.
type GetLogsIn struct {
	Lines int `json:"lines,omitempty" jsonschema:"Number of recent log lines to return (default 200, max 1000)"`
}

// getLogs tails the bot's log file. A missing file is a text note, not an
// error (mirrors the dashboard's apiLogs).
func (s *Server) getLogs(ctx context.Context, req *mcp.CallToolRequest, in *GetLogsIn) (*mcp.CallToolResult, any, error) {
	n := in.Lines
	if n < 1 {
		n = 200
	}
	if n > 1000 {
		n = 1000
	}
	path := logutil.ResolvePath(s.deps.LogDir, s.deps.LogBase)
	if _, err := os.Stat(path); err != nil {
		return textResult("no log file yet — is file logging enabled? (logging.enabled)"), nil, nil
	}
	lines, err := logutil.TailLines(path, n)
	if err != nil {
		return nil, nil, err
	}
	res, err := jsonResult(lines)
	return res, nil, err
}

// listCommands returns a grouped text listing: core commands first (noting the
// slash equivalents exist), then each module's commands under its name — the
// same grouping [p]help uses.
func (s *Server) listCommands(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
	var b strings.Builder
	b.WriteString("Core commands (each also has a slash equivalent):\n")
	for _, c := range commands.CoreCommands {
		fmt.Fprintf(&b, "  %s — %s (usage: %s)\n", c.Name, c.Description, c.Usage)
	}
	for _, mc := range s.deps.Bot.GetAllModuleCommandsByModule() {
		b.WriteString("\n" + mc.Name + ":\n")
		for _, c := range mc.Commands {
			fmt.Fprintf(&b, "  %s — %s (usage: %s)\n", c.Name, c.Description, c.Usage)
		}
	}
	return textResult(b.String()), nil, nil
}

// getConfig returns the core config as a JSON map with secrets redacted. It
// reads config.yml fresh (typed struct) so live writes are visible — not via a
// dashboard call.
func (s *Server) getConfig(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
	cfg, err := config.Load(s.deps.ConfigDir)
	if err != nil {
		return nil, nil, fmt.Errorf("config load failed: %w", err)
	}
	redact := func(v string) string {
		if v == "" {
			return ""
		}
		return "••••••••"
	}
	out := map[string]string{
		"prefix":                 cfg.Bot.Prefix,
		"owner_id":               cfg.Bot.OwnerID,
		"tos_url":                cfg.Bot.ToS,
		"privacy_url":            cfg.Bot.Privacy,
		"status":                 cfg.Bot.Status,
		"log_level":              cfg.Logging.Level,
		"log_enabled":            strconv.FormatBool(cfg.Logging.Enabled),
		"log_file_path":          cfg.Logging.FilePath,
		"modules_auto_load":      strconv.FormatBool(cfg.Modules.AutoLoad),
		"dashboard_listen":       cfg.Dashboard.Listen,
		"dashboard_public_url":   cfg.Dashboard.PublicURL,
		"token":                  redact(cfg.Bot.Token),
		"oauth_client_secret":    redact(cfg.OAuth.ClientSecret),
		"updater_enabled":        strconv.FormatBool(cfg.Updater.Enabled),
		"updater_repo":           cfg.Updater.Repo,
		"updater_branch":         cfg.Updater.Branch,
		"updater_token":          redact(cfg.Updater.Token),
		"updater_interval":       strconv.Itoa(cfg.Updater.CheckInterval),
		"updater_auto_pull":      strconv.FormatBool(cfg.Updater.AutoPull),
		"updater_notify_channel": cfg.Updater.NotifyChannel,
		"mcp_enabled":            strconv.FormatBool(cfg.MCP.Enabled),
		"mcp_token":              redact(cfg.MCP.Token),
	}
	res, err := jsonResult(out)
	return res, nil, err
}

// ModuleGetConfigIn is the input for module_get_config.
type ModuleGetConfigIn struct {
	Module  string `json:"module" jsonschema:"The module name"`
	GuildID string `json:"guild_id,omitempty" jsonschema:"Guild ID for guild-scoped config; empty = global"`
}

// moduleGetConfig returns a module's dashboard config values. A module that is
// not loaded, or not WebConfigurable, is an explicit tool error — absence of
// dashboard integration is a valid state, not a failure to hide.
func (s *Server) moduleGetConfig(ctx context.Context, req *mcp.CallToolRequest, in *ModuleGetConfigIn) (*mcp.CallToolResult, any, error) {
	mm := s.moduleManager()
	if mm == nil {
		return nil, nil, fmt.Errorf("module manager not available")
	}
	mod, ok := mm.Get(in.Module)
	if !ok {
		return nil, nil, fmt.Errorf("module not loaded: %s", in.Module)
	}
	wc, ok := modules.IsWebConfigurable(mod)
	if !ok {
		return nil, nil, fmt.Errorf("module %s has no dashboard integration (not WebConfigurable) — absence is a valid state", in.Module)
	}
	vals, err := wc.WebGetConfig(in.GuildID)
	if err != nil {
		return nil, nil, err
	}
	res, err := jsonResult(vals)
	return res, nil, err
}

// ── write / execute tools ──────────────────────────────────────────────────

// SetConfigIn is the input for set_config.
type SetConfigIn struct {
	Key   string `json:"key" jsonschema:"The core config key to set"`
	Value string `json:"value" jsonschema:"The value to set"`
}

// setConfig writes one core config key with full owner trust.
func (s *Server) setConfig(ctx context.Context, req *mcp.CallToolRequest, in *SetConfigIn) (*mcp.CallToolResult, any, error) {
	if err := s.applyCoreSetting(in.Key, in.Value); err != nil {
		return nil, nil, err
	}
	return textResult("set " + in.Key + " = " + in.Value), nil, nil
}

// ModuleSetConfigIn is the input for module_set_config.
type ModuleSetConfigIn struct {
	Module  string `json:"module" jsonschema:"The module name"`
	Key     string `json:"key" jsonschema:"The config key to set"`
	Value   string `json:"value" jsonschema:"The value to set"`
	GuildID string `json:"guild_id,omitempty" jsonschema:"Guild ID for guild-scoped config; empty = global"`
}

// moduleSetConfig writes one key in a module's dashboard config.
func (s *Server) moduleSetConfig(ctx context.Context, req *mcp.CallToolRequest, in *ModuleSetConfigIn) (*mcp.CallToolResult, any, error) {
	mm := s.moduleManager()
	if mm == nil {
		return nil, nil, fmt.Errorf("module manager not available")
	}
	mod, ok := mm.Get(in.Module)
	if !ok {
		return nil, nil, fmt.Errorf("module not loaded: %s", in.Module)
	}
	wc, ok := modules.IsWebConfigurable(mod)
	if !ok {
		return nil, nil, fmt.Errorf("module %s has no dashboard integration (not WebConfigurable)", in.Module)
	}
	if err := wc.WebSetConfig(in.GuildID, in.Key, in.Value); err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("set %s.%s = %s (guild %q)", in.Module, in.Key, in.Value, in.GuildID)), nil, nil
}

// RunCommandIn is the input for run_command.
type RunCommandIn struct {
	Command   string   `json:"command" jsonschema:"The bot command name to execute"`
	Args      []string `json:"args,omitempty" jsonschema:"Command arguments"`
	GuildID   string   `json:"guild_id,omitempty" jsonschema:"Guild ID giving the command its guild context"`
	ChannelID string   `json:"channel_id,omitempty" jsonschema:"Channel ID giving the command its channel context (cleanup subcommands need it)"`
}

// runCommand is the core tool: it runs any registered command through the
// bot's internal dispatcher with a virtual context. MCP acts as the bot owner
// (the bearer token IS the owner's credential).
//
// NOTE: MCP deliberately does NOT consult the dashboard's exec allowlist —
// the bearer token is the security boundary, not an allowlist. Do not "fix"
// this by adding an allowlist check later.
func (s *Server) runCommand(ctx context.Context, req *mcp.CallToolRequest, in *RunCommandIn) (*mcp.CallToolResult, any, error) {
	res, err := s.deps.Bot.ExecuteCommand(in.Command, in.Args, in.GuildID, in.ChannelID, s.deps.Bot.GetOwnerID(), commands.ExecKindPrefix)
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	// The error-red color marks a failed command; prefix the output so the
	// agent sees the failure without Discord.
	if res.Color == embed.ColorError {
		b.WriteString("error: ")
	}
	if res.Title != "" || res.Description != "" {
		fmt.Fprintf(&b, "[%s] %s\n", res.Title, res.Description)
	}
	if res.Text != "" {
		b.WriteString(res.Text)
	}
	return textResult(b.String()), nil, nil
}

// SendMessageIn is the input for send_message.
type SendMessageIn struct {
	ChannelID        string `json:"channel_id" jsonschema:"The channel ID to send to"`
	Content          string `json:"content,omitempty" jsonschema:"Plain text content (optional if an embed is provided)"`
	EmbedTitle       string `json:"embed_title,omitempty" jsonschema:"Embed title (optional)"`
	EmbedDescription string `json:"embed_description,omitempty" jsonschema:"Embed description (optional)"`
}

// sendMessage posts a message (plain text and/or an embed) to a channel. The
// channel must exist in the cache; content and embed are independently
// optional but at least one is required.
func (s *Server) sendMessage(ctx context.Context, req *mcp.CallToolRequest, in *SendMessageIn) (*mcp.CallToolResult, any, error) {
	ch := s.deps.Bot.GetCachedChannel(in.ChannelID)
	if ch == nil {
		return nil, nil, fmt.Errorf("channel not in cache: %s", in.ChannelID)
	}
	if in.Content == "" && in.EmbedTitle == "" && in.EmbedDescription == "" {
		return nil, nil, fmt.Errorf("content and embed are both empty — at least one is required")
	}
	chID, err := snowflake.Parse(in.ChannelID)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid channel id: %s", in.ChannelID)
	}
	mc := discord.MessageCreate{Content: in.Content}
	if in.EmbedTitle != "" || in.EmbedDescription != "" {
		mc.Embeds = []discord.Embed{{Title: in.EmbedTitle, Description: in.EmbedDescription}}
	}
	msg, err := s.deps.Rest.CreateMessage(chID, mc)
	if err != nil {
		return nil, nil, err
	}
	return textResult("sent message " + msg.ID.String()), nil, nil
}

// ModuleActionIn is the input for module_action.
type ModuleActionIn struct {
	Module string `json:"module" jsonschema:"The module name"`
	Action string `json:"action" jsonschema:"One of: load, unload, reload"`
}

// moduleAction loads, unloads, or reloads a module.
func (s *Server) moduleAction(ctx context.Context, req *mcp.CallToolRequest, in *ModuleActionIn) (*mcp.CallToolResult, any, error) {
	var err error
	switch in.Action {
	case "load":
		err = s.deps.Bot.LoadModule(in.Module)
	case "unload":
		err = s.deps.Bot.UnloadModule(in.Module)
	case "reload":
		err = s.deps.Bot.ReloadModule(in.Module)
	default:
		return nil, nil, fmt.Errorf("unknown action: %s (use load, unload, or reload)", in.Action)
	}
	if err != nil {
		return nil, nil, err
	}
	return textResult(in.Action + " " + in.Module + ": ok"), nil, nil
}

// UpdateActionIn is the input for update_action.
type UpdateActionIn struct {
	Action string `json:"action" jsonschema:"One of: status, check, apply, test"`
}

// updateAction drives the self-updater.
func (s *Server) updateAction(ctx context.Context, req *mcp.CallToolRequest, in *UpdateActionIn) (*mcp.CallToolResult, any, error) {
	u := s.updater()
	if u == nil {
		return nil, nil, fmt.Errorf("updater not available")
	}
	switch in.Action {
	case "status":
		res, err := jsonResult(u.Status())
		return res, nil, err
	case "check":
		res, err := u.Check(ctx)
		if err != nil {
			return nil, nil, err
		}
		return textResult(fmt.Sprintf("up to date: %v, behind: %d commits, %s", res.UpToDate, res.Behind, res.VersionSummary())), nil, nil
	case "apply":
		// context.Background(): Apply does git fetch + git merge --ff-only +
		// go build + binary swap. The request context dies with the MCP client's
		// timeout or disconnect, which would abort mid-flight AFTER the merge but
		// BEFORE the binary swap — the tree would be ahead while the process still
		// runs the old build, and nothing would ever re-apply it. Detached, the
		// apply either completes or is never started.
		if err := u.Apply(context.Background()); err != nil {
			return nil, nil, err
		}
		return textResult("update applied"), nil, nil
	case "test":
		if err := u.NotifyTest(); err != nil {
			return nil, nil, err
		}
		return textResult("test notification sent"), nil, nil
	default:
		return nil, nil, fmt.Errorf("unknown action: %s (use status, check, apply, or test)", in.Action)
	}
}
