# CLAUDE.md — Custom Discord Bot

## Overview

A modular Discord bot in Go. The web dashboard and the cleanup/tickets/imagefilter feature modules are compiled into the single binary (core infrastructure, always on); Lua scripts (`.lua` files) and Python modules (directories with `main.py`) load dynamically via subprocess IPC. Inspired by Red-DiscordBot but fully standalone. Designed for Linux only.

## Tech Stack

| Component | Technology | Version |
|-----------|-----------|---------|
| Language | Go | 1.26.4 |
| Discord Library | [disgo](https://github.com/disgoorg/disgo) | v0.19.6 |
| Config | YAML (`gopkg.in/yaml.v3`) | v3.0.1 |
| Snowflake IDs | `github.com/disgoorg/snowflake/v2` | v2.0.3 |
| Module System | Compiled-in core (dashboard, cleanup, tickets, imagefilter) + dynamic Lua/Python modules | stdlib |
| Lua Modules | [gopher-lua](https://github.com/yuin/gopher-lua) | v1.1.2 |
| Python Modules | Subprocess IPC (per-module venv) | Python 3 |
| MCP Server | [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) | v1.8.0 |
| Image Filter | [onnxruntime_go](https://github.com/yalue/onnxruntime_go) + ONNX Runtime C lib | v1.36.0 / 1.30.0 |
| Voice | [hraban/opus](https://github.com/hraban/opus) (cgo, needs `libopus-dev`) | v2.0.0 |
| Logging | `log/slog` (stdlib) + file output | stdlib |
| Target Platform | Ubuntu Server (Linux amd64) | - |

## Project Structure

```
/home/sam/bot/
├── cmd/bot/main.go           # Entry point — Discord connection, event handling, command dispatch
├── commands/
│   ├── command.go            # Core types: Command, SlashCommand, Context, Interface
│   ├── slashargs.go          # Slash→prefix arg vector: declared option order, subcommand-group aware
│   └── core.go               # 13 core commands + auto-generated slash equivalents
├── config/
│   └── config.go             # YAML config loading/saving, Config struct, Set() with validation
├── embed/
│   └── embed.go              # Discord embed helpers (Success, Error, Info, Warning, New)
├── logger/
│   ├── logger.go             # Async slog JSON to stdout + file
│   └── rotating_writer.go    # Daily-rotating file writer (30 days kept)
├── modules/
│   ├── module.go             # Module interface, Manager (Lua/Python dynamic loading + builtin registration)
│   ├── lua_loader.go         # Lua module loader
│   ├── lua_module.go         # Lua module wrapper (implements Module interface)
│   ├── lua_bridge.go         # Go-Lua bridge (ctx object, logging, bot info)
│   ├── python_loader.go      # Python module loader (spawns process, waits for ready)
│   ├── python_module.go      # Python module wrapper (implements Module interface)
│   ├── python_bridge.go      # Go-Python bridge (IPC callbacks → Discord Rest)
│   ├── python_ipc.go         # IPC protocol (stdin/stdout JSON messaging)
│   ├── python_venv.go        # Per-module venv + pip install management
│   ├── voice.go              # VoiceManager handed to modules (join/leave/play/volume via FFmpeg)
│   ├── tickets_contract.go   # TicketProvider/TicketTranscript interfaces for the dashboard
│   └── imagefilter_contract.go # ImageFilterAdmin interface for the dashboard
├── sdk/python/misfit/     # Python SDK for module authors
│   ├── module.py             # Module ABC (name, version, on_load, commands, etc.)
│   ├── commands.py           # Command/SlashCommand dataclasses
│   ├── context.py            # Context/BotContext/Logger (IPC-backed)
│   ├── ipc.py                # IPC class (JSON over stdin/stdout)
│   └── runner.py             # Runner script (launched by Go, imports user main.py)
├── ratelimit/
│   └── ratelimit.go          # Per-user sliding-window limiter (10 cmds / 5s, owner bypass)
├── onboarding/
│   └── onboarding.go         # First-run setup wizard
├── scripts/
│   ├── version.sh            # VERSION reader + SemVer validator (CI/install/updater twin)
│   ├── setup_imagefilter.sh  # Idempotent ONNX Runtime + CLIP export/verify provisioner
│   ├── setup_onnx.sh         # Downloads the ONNX Runtime C library into lib/onnxruntime/
│   └── export_clip_onnx.py   # CLIP vision tower → ONNX export (+ verify) per variant
├── permissions/
│   └── permissions.go        # Three-tier permission system (owner + elevated > guild owner > roles)
├── updater/
│   ├── updater.go            # Self-update Manager: poll loop, git pull → rebuild → self-exec restart
│   ├── github.go             # GitHub REST client (commits/compare/PRs, Bearer token)
│   ├── notify.go             # PR/commit embed builders + temporary embed tester samples
│   ├── state.go              # updater_state.json persistence (last SHA, seen PRs)
│   └── updater_test.go       # Embed format, diffing, seeding, state round-trip tests
├── go.mod
├── go.sum
├── bot                       # Compiled binary
├── config.yml                # Runtime config
├── modules/                  # Dynamic (Lua/Python) loader infra + compiled-in builtin data dirs
│   ├── module.go             # Module interface, Manager (Lua/Python dynamic loading + builtin registration)
│   ├── lua_loader.go         # Lua module loader
│   ├── lua_module.go         # Lua module wrapper (implements Module interface)
│   ├── lua_bridge.go         # Go-Lua bridge (ctx object, logging, bot info)
│   ├── lua_webconfig.go      # Lua dashboard integration (<name>.dashboard.lua → WebConfigurable)
│   ├── python_loader.go      # Python module loader (spawns process, waits for ready)
│   ├── python_module.go      # Python module wrapper (implements Module interface)
│   ├── python_bridge.go      # Go-Python bridge (IPC callbacks → Discord Rest)
│   ├── python_ipc.go         # IPC protocol (stdin/stdout JSON messaging)
│   ├── python_venv.go        # Per-module venv + pip install management
│   ├── Go/                   # (legacy plugin dir — feature modules are compiled-in now)
│   ├── Lua/                  # Lua modules (each a folder: <name>/<name>.lua, optional <name>.dashboard.lua)
│   ├── Python/               # Python modules (each a folder with main.py, optional dashboard.py)
│   ├── cleanup/              # cleanup builtin runtime data (DataDir)
│   ├── tickets/              # tickets builtin runtime data (DataDir)
│   └── imagefilter/          # imagefilter builtin runtime data (DataDir)
├── internal/
│   ├── dashboard/            # Web dashboard — compiled-in core subsystem (HTTP server, always on)
│   │   ├── server.go         # Router + middleware (panic-recovery mandatory, CSP)
│   │   ├── pages.go          # Page handlers + baseData + fieldRender (schema-driven fields)
│   │   ├── auth.go           # OAuth2 login, signed session cookies, mutual-guild enforcement
│   │   ├── acl.go            # 4 RBAC tiers (owner/elevated/staff/regular) + route guards
│   │   ├── commands.go       # Command catalog filtered via canUse (mirrors [p]help)
│   │   ├── metrics.go        # Live metrics snapshot from cache + runtime
│   │   ├── api.go/api2.go/api3.go # Tiered JSON API (+ imagefilter integration)
│   │   ├── tickets.go        # Tickets module integration (lists, transcripts, files; transcript regeneration via modules.TicketTranscript)
│   │   ├── templates.go      # go:embed templates + render()/FuncMap
│   │   ├── web/templates/    # rd_*.html only (rd_header/rd_footer chrome, rd_field partial)
│   │   └── web/static/redesign/ # style.css (token system) + app.js (page-activation) + logo
│   ├── mcpserver/            # MCP server — bearer-token endpoint mounted at /mcp on the dashboard listener
│   │   ├── server.go         # mcp.Server + per-request bearer auth (live mcp_enabled/mcp_token read)
│   │   └── tools.go          # the 13 tools (read + write/execute)
│   ├── logutil/              # Shared log-file helpers (ResolvePath/TailLines) for dashboard + MCP
│   └── builtin/              # Compiled-in feature modules (gated by enabled_modules)
│       ├── cleanup/          # Message cleanup (9 subcommands)
│       ├── tickets/          # Ticket system
│       └── imagefilter/      # CLIP image filter (dashboard-only)
├── loaded_modules.json       # Module persistence (auto-managed)
└── logs/
    └── bot-YYYY-MM-DD.log    # JSON log output (daily rotation, 30 files kept)
```

## Core Architecture

### Entry Point (`cmd/bot/main.go`)

Startup sequence:
1. `main()`: auto-creates `modules/` (+ `Go/`, `Lua/`, `Python/` subfolders) and `logs/`; checks for `config.yml` (runs onboarding if missing); creates logger, permission manager, rate limiter; constructs the updater manager + starts its poll loop
2. `run()`: creates the disgo client (`FlagGuilds` + `FlagMembers` + `FlagRoles` cache) and opens the gateway
3. Re-attaches the REST client to the updater (`updaterMgr.SetRest`)
4. Initializes the voice manager
5. Registers the Lua and Python loaders with the module manager
6. `loadCoreModules(ba)` — loads saved modules (from `loaded_modules.json`) or AutoLoad-scans `modules/Lua/` + `modules/Python/`
7. `modules.AdoptLegacyBuiltinData` for cleanup/tickets/imagefilter — migrates plugin-era `modules/Go/<name>/` data forward
8. `ModMgr.RegisterBuiltinsWithFilter(...)` — registers the compiled-in builtins (cleanup/tickets/imagefilter), gated by `enabled_modules`
9. `registerSlashCommands()` — registers all slash commands with Discord
10. `migrateFromPluginEra()` — one-time cleanup of stale plugin-era `.so` files
11. MCP token generation (if empty) + `mcpserver.New`
12. `dashboard.New` + `dash.Start()` — the dashboard gets `Deps.MCP` (the MCP handler)
13. Applies the persisted presence status
14. Waits on SIGINT/SIGTERM/SIGHUP (shutdown → `dash.Stop()` + `Client.Close()`), `shutdownCh` (`[p]shutdown`), or `restartCh` (`[p]restart` / updater hand-off); on restart the `main()` loop re-enters `run()` and `syscall.Exec`s the new binary if the updater applied one

**`command.go`** — core types:

```go
type Command struct {
    Name           string
    Description    string
    Usage          string
    Category       string
    RequiredPerm   discord.Permissions
    OwnerOnly      bool
    SuperOwnerOnly bool               // only bot owner (not elevated), checked before CanUse
    Aliases        []string
    WebArgs        []WebArg           // optional typed args for the dashboard runner
    Execute        func(ctx *Context) error
}

type SlashCommand struct {
    Name           string
    Description    string
    Category       string
    Options        []discord.ApplicationCommandOption
    RequiredPerm   discord.Permissions
    OwnerOnly      bool
    SuperOwnerOnly bool
    Execute        func(ctx *Context) error
}

type Context struct {
    Bot       Interface
    ChannelID string
    GuildID   string
    Author    discord.User
    Args      []string
    IsSlash   bool
    Web       bool   // true: invoked from the web dashboard (virtual context)
    MessageID string // invoking message ID (prefix commands only; "" for slash)
    Respond   func(embeds ...discord.Embed) error
    ReplyText func(text string) error
}

// Auto-delete rule (dispatcher): the bot auto-deletes ONLY error-colored embeds
// (red, embed.ColorError) after 7s. Every other response — success, info,
// warning, usage listings, status reports, plain text — stays on screen
// permanently. No per-command "preserve" opt-in; the single rule covers all.
```

**`Interface`** — contract between commands and bot core:

```go
type Interface interface {
    IsOwner(userID string) bool
    IsElevated(userID string) bool
    CanUse(userID string, perms discord.Permissions, requiredPerm discord.Permissions, ownerOnly bool, guildOwnerID string) bool
    GetUserPermissions(userID string, guildID string) discord.Permissions
    GetGuildOwnerID(guildID string) string
    GetSelfUserID() string
    GetPrefix() string
    GetName() string
    GetVersion() string
    GetOwnerID() string
    GetToS() string
    GetPrivacy() string
    SetConfig(key, value string) error
    GetConfigDir() string
    GetLoadedModuleNames() []string
    LoadModule(name string) error
    UnloadModule(name string) error
    ReloadModule(name string) error
    UnloadAllModules() error
    GetModuleManager() interface{}
    GetUpdater() interface{}
    CommandOverrides() *CommandOverrides       // per-command override store (nil when disabled)
    GetAllModuleCommands() []Command
    GetAllModuleCommandsByModule() []ModuleCommands // module name → its prefix commands (load order); used by [p]help to group each module's commands under a category named after the module
    GetAvailableModuleNames() []string
    IsBuiltinModule(name string) bool          // true for cleanup/tickets — imagefilter is also gated by enabled_modules but is not Discord-toggleable
    SetEnabledModule(name string, enabled bool) error // persists enabled_modules; applies on next restart
    GetPermissionManager() *permissions.Manager
    SetPresence(activityType string, status, text string) error
    GetLatency() string
    Shutdown()
    Restart()
    GetCachedMember(guildID, userID string) *discord.Member
    GetCachedGuild(guildID string) *discord.Guild
    GetCachedRole(guildID, roleID string) *discord.Role
    GetCachedChannel(channelID string) discord.GuildChannel
    GetMemberRoles(guildID, userID string) []discord.Role
    GetClient() interface{}                    // raw *bot.Client (cache/gateway/rest) for in-process modules
    GetStartTime() time.Time                    // bot process start time (uptime source)
    StatusRateLimit(userID string) (allowed bool, wait time.Duration)
    ResetRateLimit(userID string)
    ExecuteCommand(name string, args []string, guildID, channelID, asUserID, kind string) (CommandResult, error) // web exec: virtual captured context, nothing posted to Discord
}

type ModuleCommands struct {
    Name     string
    Commands []Command
}

type CommandResult struct {
    Title       string
    Description string
    Color       int
    Text        string
}

type WebArg struct {
    Name     string
    Label    string
    Type     string // text | number | toggle | select
    Required bool
    Options  []string // for select
}

const (
    ExecKindPrefix = "prefix"
    ExecKindSlash  = "slash"
)
```

**`core.go`** — 13 core commands:
- `ping`, `uptime`, `info`, `help` — public
- `modules` — `OwnerOnly: true` — list modules, or `modules enable|disable <name>` for compiled-in features (cleanup/tickets only; imagefilter is gated by the same `enabled_modules` map but has no Discord toggle, only its dashboard page); applies after restart
- `load`, `unload`, `reload` — `OwnerOnly: true`, supports `all`
- `shutdown`, `restart` — `OwnerOnly: true`
- `permissions` — `OwnerOnly: true` — `add|remove|list <user>` (elevated users)
- `ratelimit` — `OwnerOnly: true` — `status|reset [user_id]`
- `update` — `OwnerOnly: true` — check/now/status/test/set subcommands for the self-updater

**Permissions flow at dispatch level:**
1. If `SuperOwnerOnly` and not bot owner → denied immediately
2. Then `CanUse` checks: owner/elevated bypass everything, `OwnerOnly` blocks non-owners, guild owner bypasses `RequiredPerm`, otherwise check Discord permission

**Auto-delete:** The bot auto-deletes **only error-colored embeds** (red, `embed.ColorError` = `0xED4245`, i.e. anything built with `embed.Error(...)`) after **7s** so they can be read. **Every other response — success, info, warning, usage/reference listings, status reports, plain text — stays on screen permanently.** There is no per-command "preserve" list (removed) and no opt-in hook: the dispatcher inspects the first embed's color and deletes iff it's red (`isErrorResponse()` + `errorAutoDeleteDelay` in `main.go`). Plain-text `ctx.ReplyText` and Lua/Python bridge responses never auto-delete.

**Slash command re-registration:** Mutex-guarded (`registerSlashMu`) via `registerSlashCommands`. Multiple concurrent `reload all` calls serialize on `SetGlobalCommands`.

### Module System (`modules/`)

Module interface:
```go
type Module interface {
    Name() string
    Version() string
    Description() string
    Author() string
    OnLoad(ctx *Context) error
    OnUnload() error
    Commands() []commands.Command
    SlashCommands() []commands.SlashCommand
    Dependencies() []string
}
```

**Optional `WebConfigurable` contract** (the opt-in interface the dashboard uses
for module settings — additive & non-breaking; modules that don't implement it
are simply unaffected). A module is the **single source of truth** for what
settings exist and exactly how each renders — the dashboard never introspects
module internals, it only renders whatever `WebConfigSchema()` returns:

```go
type WebConfigurable interface {
    WebConfigSchema() []ConfigField                              // ordered field list
    WebGetConfig(guildID string) (map[string]string, error)      // "" = global
    WebSetConfig(guildID, key, value string) error               // "" = global
}

type ConfigField struct {
    Key, Label, Help, Type, Placeholder string
    Options     []string   // for select/multi
    Min, Max, Step *float64 // for number/range
    Scope       string     // "global" (owner/elevated) | "guild" (guild managers)
    GuildScoped bool       // true => field editable per-guild
    // all values are strings over the wire; the module parses ints/bools itself
}

// Render types the dashboard understands (one per field; no module-side rendering):
//   toggle | text | textarea | number | range | select | multi | secret | channel | role | user
// Scope="global" => editable by owner/elevated only; GuildScoped=true => also by
// guild managers (staff). channel/role/user imply guild scope (populated from cache;
// user is capped). Options required for select/multi; Min/Max/Step for number/range.
```

A new module exposes a full settings panel by declaring a schema + implementing
`WebGetConfig/WebSetConfig` — **zero dashboard code changes needed**. The
dashboard module itself implements `WebConfigurable` to self-configure from the
web (dogfooding every field type).

Module context:
```go
type Context struct {
    BotName string
    OwnerID string
    DataDir string          // per-module data dir: <modules path>/<name> for builtins
                            // (dashboard pinned to modules/Go/dashboard/); Lua/Python = the module folder
    Logger  Logger
    Rest    rest.Rest
    Bot     commands.Interface
    Events  *EventHooks
    VoiceManager *VoiceManager
}
```

**19 event hooks** — Lua may register through `ctx.on_event(name, callback)` during `on_load` (the wrapper then wires them to the same hook table), Go only as a compiled-in builtin via `ctx.Events.Add*()` on the `modules.Context` during `OnLoad`, and Python declares them in `event_handlers()`. All dispatched through `safeDispatch()` with panic recovery.

Available hooks:
- `AddMessageCreate`, `AddMessageUpdate`, `AddMessageDelete`
- `AddGuildMessageCreate`, `AddGuildMessageUpdate`, `AddGuildMessageDelete`
- `AddGuildMemberJoin`, `AddGuildMemberLeave`
- `AddGuildBan`, `AddGuildUnban`
- `AddGuildJoin`, `AddGuildLeave`
- `AddPresenceUpdate`
- `AddMessageReactionAdd`, `AddMessageReactionRemove`
- `AddVoiceStateUpdate` — voice channel join/leave/move
- `AddGuildChannelDelete` — a guild channel was deleted (ticket channels finalize on this)
- `AddComponentInteraction` — button clicks, select menus
- `AddModalSubmit` — modal form submissions

**Manager** routes by detected type:
1. `DetectModuleType(path)` — "lua" / "python" / "" (unsupported)
2. Lua → `LuaLoader.Load(path)`; Python → `PythonLoader` (spawns the subprocess, waits for `ready`)
3. `mod.OnLoad(ctx)` — initializes; `bctx.DataDir` is `<modules path>/<name>` for builtins, the module folder for Lua/Python
4. Any other type → error (`only Lua and Python modules are loadable; feature modules are compiled-in`)

**Manager methods:**
- `Load(path, hooks)` — load module with event hooks (auto-detects type: lua/python; anything else is an error)
- `Unload(name)` — calls `OnUnload()`, always cleans up hooks even on error
- `UnloadAll()` — unloads all, collects errors
- `Get(name)` — get loaded module
- `List()` — list all as `[]ModuleInfo`
- `GetNames()` — list all names as `[]string`
- `AllCommands()` — collect all module commands (flat)
- `AllCommandsByModule()` — `[]commands.ModuleCommands{ Name, Commands }` in load order — groups each module's commands under its name; used by `[p]help`
- `AllSlashCommands()` — collect all module slash commands
- `SetLuaLoader(loader)` — registers the Lua loader
- `SetPythonLoader(loader)` — registers the Python loader

**Module type detection** (`DetectModuleType(path)`):
- `.lua` file → "lua" (via `IsLuaModule`)
- Directory with `main.py` → "python" (via `IsPythonModule`)
- Anything else → "" (unsupported — only Lua files and Python dirs stay dynamic; there is no Go plugin path)

**Path resolution** (`resolveModulePath(modulesDir, name)` in main.go):
- Probes `modules/Lua/<name>/<name>.lua` → `modules/Lua/<name>/main.lua` → `modules/Python/<name>/main.py` in order
- Used by `LoadModule`, `loadSingleModule`, `GetAvailableModuleNames`, and `loadCoreModules`

### Lua Modules

Single `.lua` files in `modules/Lua/<name>/`. Loaded by `LuaLoader` using gopher-lua.

**Lua module format:** Script defines a global table `M` with fields `name`, `version`, `description`, `author`, and functions `on_load(M, name)`, `on_unload(M)`, `commands(M)`, `slash_commands(M)`. Each command table has `name`, `description`, `usage`, `category`, `execute(M)`.

**Bridge:** `LuaBridge` registers a `ctx` global table with log functions (`log`, `log_debug`, `log_warn`, `log_error`) and bot info functions (`get_prefix`, `get_name`, `get_version`, `get_owner_id`, `is_owner`, `is_elevated`). Command execution adds `channel_id`, `guild_id`, `author_id`, `is_slash`, `args` table, `respond` fn, `reply_text` fn to the ctx table.

**Lua execute convention (verified against the loader):** the loader reads the **global** `M` table (`local M = {}` does NOT work) and calls callbacks with explicit args: `on_load(M, name)`, `commands(M)`, and each command's `execute(M)` — the single argument is always the module table. Read the command context from the **global** `ctx` table (`ctx.args`, `ctx.respond`, `ctx.reply_text`, `ctx.log`, …), which `RegisterCommandContext` refreshes per call. Do NOT name a callback parameter `ctx` — it would shadow the global. Use DOT syntax (`function M.on_load(M, name)`), not colon syntax (`M:on_load` shifts every parameter by one). Modules should also define `M.slash_commands()` (may return `{}`).

**Dashboard integration script (optional):** a Lua module's dashboard settings panel is declared in a SEPARATE script, `<module>.dashboard.lua` NEXT TO the module file (e.g. for `modules/Lua/<name>/<name>.lua` the script is `modules/Lua/<name>/<name>.dashboard.lua`). It runs in its own Lua state and defines a global table `D` with `D.schema` (array of field tables: `key`, `label`, `help`, `type` (one of the `FieldType*` strings), `scope`, `guild_scoped`, `placeholder`, `options`, `min`/`max`/`step`), `D.get(guild_id)` (returns `{key=value,…}` or `nil, error`), and `D.set(guild_id, key, value)` (returns `nil` or an error string). The script's `ctx` table also carries `ctx.data_dir` (the module config dir) for persisting values. Absence of the script ⇒ the module has NO dashboard integration (empty schema, no settings panel, no config API writes). `*.dashboard.lua` files are NOT modules: every scan site (`AutoLoad`, `[p]load all`, `GetAvailableModuleNames`, `DiscoverLuaModules`) skips them via `IsLuaDashboardScript`. The wrapper implements `WebConfigurable` in `modules/lua_webconfig.go` — zero dashboard code changes.

### Python Modules

Directories in `modules/Python/<name>/` containing `main.py` + optional `requirements.txt`. Loaded by `PythonLoader` via subprocess IPC.

**Python module format:** `main.py` imports from `misfit` (Module, Command, SlashCommand), defines a Module subclass, and assigns `module = MyModule()` as a global. The runner script (`sdk/python/misfit/runner.py`) imports the user's `main.py`, extracts the `module` global, sets up IPC, sends a `ready` message with module metadata + commands, and dispatches `init`/`command`/`event`/`shutdown` messages.

**IPC protocol** (JSON over stdin/stdout):
- Go → Python (stdin): `{type:init, context:{bot_name, owner_id, prefix, version, data_dir}}`, `{type:command, name, args, channel_id, guild_id, author_id, is_slash}`, `{type:event, name, data}`, `{type:web_get_config, guild_id, req_id}`, `{type:web_set_config, guild_id, key, value, req_id}`, `{type:shutdown}`
- Python → Go (stdout): `{type:ready, name, version, description, author, commands:[...], slash_commands:[...], event_handlers:[...], has_web_config, web_schema:[...]}`, `{type:respond, channel_id, title, description}`, `{type:reply_text, channel_id, text}`, `{type:web_config_response, req_id, values|ok|error}`, `{type:log, level, message}`, `{type:error, message}`

**Dashboard integration script (optional):** a Python module's dashboard settings panel is declared in a SEPARATE script, `dashboard.py`, inside the module directory next to `main.py`. The runner imports it in the same process, normalizes `web_schema` (list of field dicts: `key`, `label`, `help`, `type`, `scope`, `guild_scoped`, `placeholder`, `options`, `min`/`max`/`step`), ships it in `ready`, and answers `web_get_config`/`web_set_config` via `web_get_config(guild_id)` / `web_set_config(guild_id, key, value)` (exceptions become `error` replies). Absence of `dashboard.py` ⇒ the module has NO dashboard integration (`has_web_config: false`, no settings panel). The wrapper implements `WebConfigurable` in `python_module.go` (`SendWebGetConfig`/`SendWebSetConfig` in `python_ipc.go`, req_id-correlated with a 5s timeout) — zero dashboard code changes.

**Venv management:** Each Python module gets a per-module `.venv/` directory. `PythonVenv.Ensure()` creates the venv if missing and `pip install -r requirements.txt` if the requirements hash changed (tracked in `.venv/.requirements_hash`).

**Bridge:** `PythonBridge` holds `rest.Rest` for async Discord message sending. IPC callbacks: `onRespond` → creates embed + `Rest.CreateMessage`, `onReplyText` → `Rest.CreateMessage` with Content, `onLog` → routes to bot logger, `onError` → routes to bot logger as error.

**Command execution:** Python command `Execute` closures send the command to the Python process via IPC (`SendCommand`) and return nil immediately. The Python process sends `respond`/`reply_text` back asynchronously. The bridge's callbacks deliver the response to Discord. No auto-delete for Python module responses.

**Loading:**
- `botAdapter.LoadModule(name)` resolves path (Lua file / Python dir), creates fresh `EventHooks`, calls `ModMgr.Load(path, hooks)`, then `mod.OnLoad()`. On error, `ModMgr.Unload(name)` cleans up. Persists to `loaded_modules.json`.
- `botAdapter.UnloadModule(name)` → `ModMgr.Unload(name)` + persist.
- `botAdapter.ReloadModule(name)` → `Unload` + `LoadModule`. Reload cannot roll back (the module is gone from the manager once unloaded), so a failed reload logs `Reload of module '<name>' failed after unload — module lost until bot restart` and the module stays out until restart.

**Module loading on startup:**
1. `--no-modules` flag skips everything
2. Reads `loaded_modules.json` for previously loaded modules
3. If empty and `AutoLoad: true`, scans `modules/Lua/` + `modules/Python/`, loads them, persists to `loaded_modules.json`. A persisted name that no longer resolves logs `Previously loaded module <name> not found, skipping`
4. Module loading runs AFTER `Client.OpenGateway()` so `OnLoad` has gateway access
5. `registerSlashCommands` is called once after all modules are loaded
6. Runtime load/unload calls `reRegisterSlashCommands` in a goroutine (serialized by mutex)

### Permission System (`permissions/`)

**Three tiers:**
1. **Bot owner + elevated** — bypass everything including `OwnerOnly` and `RequiredPerm`
2. **Guild owner** — bypasses `RequiredPerm` but NOT `OwnerOnly`, NOT `SuperOwnerOnly`
3. **Everyone** — checked via Discord role permissions

**`SuperOwnerOnly`** — checked at dispatch level, not in `CanUse`. Only the actual bot owner (config `owner_id`) passes. Elevated users do NOT bypass this.

`CanUse(userID, userPerms, requiredPerm, ownerOnly, guildOwnerID)`:
- Owner or elevated → always allowed
- `OwnerOnly` → blocked (only owner + elevated passed above)
- Guild owner → allowed (bypasses `RequiredPerm`)
- `RequiredPerm != 0` → check `userPerms.Has(requiredPerm)`
- Otherwise → everyone can use

`ExtractID()` handles `@User`, `<@ID>`, `<@!ID>` mention formats.

### Embed System (`embed/`)

Helpers: `Success(title, desc)`, `Error(title, desc)`, `Info(title, desc)`, `Warning(title, desc)`, `New()`.

**CRITICAL:** `WithFields(fields...)` **replaces** `e.Fields`, does not append. Build a `[]discord.EmbedField` slice first, then call `WithFields(fields...)` once. Use `util.PtrBool(b)` for `Inline` field (`*bool`).

### Config System (`config/`)

```yaml
bot:
  token: "your-bot-token"
  prefix: "?"
  owner_id: "123456789"
  elevated_ids: []
  name: "Bot"
  status: "online"
  tos_url: ""
  privacy_url: ""
  bot_allowlist: []        # bot user IDs allowed to run prefix commands (QA observer bots); empty = all bots ignored
modules:
  auto_load: true
  path: "modules"
  disabled: []
  enabled_modules: {}      # gates the compiled-in builtins: {"cleanup": false} disables one; missing key = enabled
logging:
  # Discord-channel logging is not implemented in core — there is no channel_id key
  enabled: true
  file_path: "logs/bot.log"
  level: "info"
dashboard:                 # optional — pin dashboard bind/public URL from the main config
  listen: ""               # e.g. "127.0.0.1:9090" when default 8080 is taken; empty = 127.0.0.1:8080
  public_url: ""           # e.g. "https://dashboard.example.com"
oauth:                     # Discord application OAuth2 credentials the dashboard (and any OAuth-using module) read
  client_secret: ""        # from Dev Portal → OAuth2 → General; NOT the bot token. Set via the dashboard Admin page (or edit config.yml)
updater:                   # self-update integration with the bot's own GitHub repo (public since 2026-08-06)
  enabled: true            # master switch; false = updater does nothing
  repo: "Myrukora/misfit-bot"  # owner/name; empty = feature off
  branch: "main"           # branch to track
  token: ""                # GitHub PAT (or `gh auth token`); never committed (config.yml is gitignored)
  check_interval: 300      # seconds between polls (min 30)
  auto_pull: true          # automatically pull + rebuild + restart on new commits
  notify_channel: ""       # Discord channel ID for PR/commit embeds; empty = notifications skipped
mcp:                      # built-in MCP server mounted at /mcp on the dashboard listener
  enabled: true           # enabled by default; per-request live kill switch via mcp_enabled
  token: ""               # bearer token; empty = auto-generated on first start and saved here
```

`Config.Set(key, value)` with validation (the full accepted-key set; anything else is `unknown config key`):
- `prefix` (rejected if empty), `token`, `owner_id`, `tos_url`, `privacy_url`, `name`, `status` (must be `online`/`idle`/`dnd`/`invisible`, `""` allowed)
- `log_level` — must be `debug`, `info`, `warn`, or `error`; `log_enabled` — accepts `true`/`1`/`yes`; `log_file_path` — rejected if empty
- `modules_auto_load` (strict bool); `enabled_modules` — comma/space-separated `name` or `name=true|false` entries (bare name = enable). Enable DELETES the map entry (missing key = enabled), disable writes `name=false`. This is the key behind `[p]modules enable|disable`.
- `dashboard_listen`, `dashboard_public_url` — write the optional top-level `dashboard:` section (non-secret infra fields the dashboard module reads to pin its bind port / public URL from the main config — used when the default `127.0.0.1:8080` is taken and the web UI can't start). `dashboard_listen` is normalized to a bare `host:port` via `NormalizeListen`, `dashboard_public_url` must start with `http://` or `https://` (see `config.go`).
- `oauth_client_secret` — write the top-level `oauth:` section. The single shared Discord-app client secret the dashboard uses for login (and any future OAuth-using module can reuse). Takes priority over the dashboard's own 0600 config fallback.
- `updater_enabled`, `updater_repo`, `updater_branch`, `updater_token`, `updater_interval`, `updater_auto_pull`, `updater_notify_channel` — write the top-level `updater:` section. Booleans are strict (reject ambiguous values), `updater_repo` must be `owner/name`, `updater_interval` must be a number ≥ 30. `Load()` applies `DefaultConfig` first, so a missing `updater:` section on an existing install comes up enabled with `branch: main` / 300s interval / auto_pull on.
- `mcp_enabled`, `mcp_token` — write the top-level `mcp:` section. `mcp_enabled` booleans are strict (same value set as `log_enabled`), `mcp_token` is free-form trimmed.
- All changes auto-save to disk. `log_level` / `log_enabled` and `enabled_modules` require a restart (the logger's level is fixed at `logger.New`, builtins are registered once at startup).

### Self-Updater (`updater/`)

The bot is wired to its own GitHub repository (`Myrukora/misfit-bot`, public since 2026-08-06). The `updater.Manager` is constructed **once** in `main()` (never inside `run()`, so in-process restarts don't spawn duplicate poll loops) and runs a poll loop every `check_interval`:

1. **Notifications** — diffs GitHub state against `updater_state.json` (last commit SHA + seen PR numbers, atomic write, 0600):
   - New open PRs → embed: **author row on top** (avatar + username hyperlinked to the GitHub profile), bold title `Pull request opened: #<number> <title>` (linked to the PR), PR body as description (markdown renders), GitHub-green `0x2EA043`.
   - New commits on the tracked branch → one embed per commit: same author row, bold title `1 new commit #<sha7>`, full commit message as description, GitHub-blue `0x0969DA`. Merge commits (`Merge pull request` / `Merge branch`) are skipped.
   - **First poll seeds silently** (records HEAD + all open PRs, posts nothing); closed PRs are pruned from the seen set so a reopen re-notifies. Force-pushed history resyncs silently. Descriptions truncated to 4000 chars.
   - **At-least-once delivery**: a PR is only marked seen (and the last-seen commit SHA only advances) AFTER its embed was actually sent. Failed sends (e.g. the REST client not ready during the startup race) are retried on the next poll and survive restarts — the state file never records them as delivered. `Run()` also waits for the first `SetRest` (30s cap) so the first poll can't fire with a nil client.
2. **Auto-update** (if `auto_pull`) — `Check()` does `git fetch origin <branch>` with a per-invocation `-c http.extraheader="AUTHORIZATION: basic <base64(x-access-token:<token>)>"` (the token never lands in `.git/config`); if behind, `Apply()` first runs `git rev-parse --abbrev-ref HEAD` and REFUSES unless the working tree is on the tracked branch (an empty tracked branch means `main`): detached HEAD → `git HEAD is detached (expected branch <branch>); switch to the tracked branch first: git checkout <branch>`, a different branch → `git checkout is on <current> but the updater tracks <branch>; switch branches first: git checkout <branch>`. Only then does it run `git merge --ff-only FETCH_HEAD` (aborts with a clear error on local changes — bot keeps running untouched) → `go build -ldflags "-X main.Version=$(updater.ReadVersionFile(Dir))" -o bot.new ./cmd/bot/` — the Go twin of `scripts/version.sh`, see `ReadVersionFile` — without the stamp the new binary would report `dev` and could never be recognised as current → swaps `bot`→`bot.old`, `bot.new`→`bot` → sets the apply flag and fires `OnApplied` (wired to `restartCh` with a 2s delay so the success embed is delivered).
3. **True self-update** — in the restart loop, before calling `run()` again, if `updaterMgr.ApplyRequested()` the bot `syscall.Exec`s the new binary (`Dir/bot`) — an in-process restart would keep running the OLD code. On exec failure it logs loudly and falls back to the in-process restart. The updater never runs the bot's repo commands with user-controlled input.

**`[p]update` command** (owner-only):
- `update` / `update check` — fetch + report the release first when the repo is tagged (`v0.1.0 → v0.2.0 (3 new commits)`), otherwise "N new commit(s) available" / "Up to date".
- `update now` — force apply (pull → rebuild → swap → restart).
- `update status` — repo/branch/last seen SHA/last check/interval/auto_pull/last error, plus **Running Version** (this build) and **Latest Release** (newest tag seen, once a check has found one).
- When an auto-update is applied, `announceUpdate` posts one `Update available — vA → vB` embed (commit count + SHAs as detail) to `notify_channel`, deduplicated per target through `state.Announced` — a bounded (20-entry) list of `releaseKey` strings covering repo + branch + channel + version, so a live config change re-announces instead of being silenced by the old entry.
- `update test` — **temporary embed tester**: posts one sample PR + one sample commit embed to `notify_channel` (markdown-rich bodies — bold/italic/code block/link/list — so the owner can verify markdown renders; the author row uses the real authenticated GitHub user when a token is set).
- `update set <key> <value>` — config keys: `enabled, repo, branch, token, interval, auto_pull, notify_channel` (routes to `Config.Set`, takes effect without restart; token value is never echoed back).

**Security notes:** the GitHub token lives only in gitignored `config.yml`; `updater_state.json` is gitignored; merge commits are skipped in notifications; the first poll is silent. The repo is public (since 2026-08-06); `main` is protected by the `main-protection` ruleset (free for public repos). The updater token and the bot's runtime state must never be committed.

### Logger (`logger/`)

- Async via channel (non-blocking)
- JSON to stdout + file; the file writer is daily-rotating (`logs/bot-YYYY-MM-DD.log`, 30 files kept)
- Levels: `debug`, `info`, `warn`, `error`
- Implements `modules.Logger` interface
- `Close()` waits for drain via `done` channel before closing file
- Level and file-enabled state fixed at `New()` — config changes require restart
- `logger.New(dir, level, fileEnabled)` takes no path: the directory is always `<dir>/logs`, basename `bot`. `logging.file_path` (default `logs/bot.log`) is what `internal/logutil` resolves to the newest `bot-*.log` for the dashboard and MCP readers, so it must keep pointing at that directory/basename to be useful.

### Image Filter (`internal/builtin/imagefilter/`)

Compiled-in builtin (gated by `enabled_modules.imagefilter`), **dashboard-only** — it
registers no Discord commands. Images posted in enabled guilds are embedded with a
CLIP vision tower via **ONNX Runtime (CPU)** and compared by cosine similarity
against that guild's blacklisted reference images.

- **Model lifecycle (warm/cold):** enabling the filter on the first guild loads the
  model and keeps it warm; disabling everywhere closes the session and frees RAM
  (`manager.go` refcount). Variant switch (`b32` default, `b16`, `l14`, `l14-336`)
  closes the session and wipes cached embeddings — different vector space.
- **Port sheet (exact Python semantics):** `vision_model → pooler_output` (768-d
  CLS, L2-normalized in Go; NOT the 512-d projected features), max cosine over
  refs, threshold default 0.95, punishments none|mute(timeout)|kick|ban, message
  deleted unless (punishment=none AND delete_on_none=false), guild-owner/role-
  hierarchy immunity (uncached members resolved over REST; an unknown hierarchy
  is never immune), red log embeds to the guild's log channel only.
- **Safety (ported):** HTTPS + Discord-CDN host allowlist (`discordapp.com`,
  `discordapp.net`, `discord.com`, `discord.media`, checked against the full
  authority so `:port`/userinfo-shaped hosts are refused), redirects refused,
  private-IP dial guard (unparsable hosts refused; IPv4-mapped IPv6 unmapped
  before the private + reserved-range checks), 50 MiB fetch cap (post-read length
  check, never a silent truncation), 25 MP decode cap, bots exempt, one job per
  distinct image attachment (duplicates collapsed; non-image attachments skipped).
- **Reference-image bounds (per guild):** 200 images / 250 MiB, enforced on add;
  uploads stage through `<name>.tmp-*` and rename atomically, so a failed or
  concurrent add cannot leave a partial file in the gallery. Guild ids are
  validated as non-zero snowflakes before any filesystem path is built.
- **Mute needs a duration:** a mute punishment with `mute_duration <= 0` is
  rejected at validation (dashboard field is `min="1" step="1"`).
- **Dashboard contract:** `modules.ImageFilterAdmin` (resolved at request time
  like the ticket contracts; an optional contract, so omitting it just means the
  surface does not exist) — per-guild config, enable/disable, reference image
  add/remove/list (upload or Discord-CDN URL), model status, variant switch.
  Guild-scoped endpoints live at `/api/guilds/<gid>/imagefilter…`
  (raw gallery bytes at `…/raw?name=<file>`, delete at `…/images/<name>`); the
  guild id is validated before any admin/filesystem access, and a failed model
  load on enable surfaces as HTTP 500 instead of a silent success.
- **Setup (automatic; artifacts are gitignored):** `scripts/setup_imagefilter.sh`
  provisions both runtime artifacts — the ONNX Runtime C lib 1.30.0 →
  `lib/onnxruntime/` (via `scripts/setup_onnx.sh`) and the CLIP **b32** vision
  tower → `modules/imagefilter/models/clip-vision-b32.onnx` (via
  `scripts/export_clip_onnx.py export --variant b32`) — then runs that script's
  `verify` (torch vs onnxruntime cosine) as proof. It is idempotent and a no-op
  once they exist, so it is called automatically by `install.sh` (opt out with
  `--skip-imagefilter`) and by `updater.Apply()` after every binary swap. All
  heavy work (CPU-only torch wheel + the HF weights, both cached) happens at
  most once per machine; it picks a `python3` whose stack resolves and owns its
  venv at `modules/imagefilter/models/.export-venv/`. It is **non-fatal by
  design** — a failure is a warning, never an install or update failure.
  Non-default variants (b16/l14/l14-336) stay on demand: the script's `VARIANT`
  is pinned to `b32`, so a non-default variant means editing it or running
  `export_clip_onnx.py export --variant <v> --out-dir modules/imagefilter/models`
  directly (which needs torch+transformers+onnxruntime+onnxscript in whatever
  Python env you use). `--force` rebuilds an existing default-variant export.
  Missing lib or model = filter stays cold with the reason visible on the
  dashboard; the module itself still loads. Tests skip silently without them.
  (The Python image_spam_filter module was retired 2026-09; a full backup lives
  in `.hermes/backups/2026-09-09-pre-revamp/image_spam_filter`, gitignored.)

### MCP Server (`internal/mcpserver/`)

Compiled-in core subsystem (always on, like the dashboard) that exposes the bot
to an MCP client (Claude Code, etc.) over the **Model Context Protocol**
streamable-HTTP transport.

- **Transport:** mounted at `/mcp` on the dashboard listener (`dashboard.Deps.MCP
  http.Handler`). The dashboard **never imports** this package — the handler is
  injected — so the endpoint automatically follows `dashboard.listen` (default
  `127.0.0.1:8080`) and inherits the dashboard's panic-recovery / logging /
  security-header middleware. `Server` is stateless per request: one shared
  `*mcp.Server` (tools registered once) wrapped in the auth middleware; it dies
  with the process.
- **Auth:** static bearer token in core `config.yml` (`mcp.token`), sent as
  `Authorization: Bearer <token>`. An empty token is auto-generated on first
  start (32 random bytes hex) via `ba.SetConfig("mcp_token", …)` and logged
  **once** (`MCP: generated bearer token, stored in config.yml (mcp.token)`) —
  never logged again. Config is re-read **per request**, which makes
  `mcp_enabled` / `mcp_token` a live kill switch: `mcp_enabled=false` → **404**,
  empty token → **503**, wrong/missing token → **401**
  (`crypto/subtle.ConstantTimeCompare`).
- **Enabled by default:** `DefaultConfig` ships `MCP: {Enabled: true}` (a
  conscious choice, same precedent as `updater:`) — exposure is bounded by the
  auto-generated token plus the default loopback-only bind. A missing `mcp:`
  section on an existing install therefore comes up enabled.
- **Full owner-level access:** the token *is* the owner identity — everything
  these tools do is done as the bot owner (single-owner bot by design).
- **13 tools** (read + write):
  - Read: `bot_status` (version/latest version/update availability/uptime/
    latency/cache counts/module counts/goroutines), `list_guilds`,
    `list_channels` (a guild's channels + roles), `get_logs` (tail of the log
    file), `list_commands` (every registered command grouped by module),
    `get_config` (core config, **secrets redacted**), `module_get_config`
    (`guild_id` empty = global scope).
  - Write/act: `set_config` (owner-trusted core config write via
    `dashboard.ApplyCoreSetting` — validated by the bot's config writer),
    `module_set_config`, `run_command`, `send_message` (returns the new message
    ID), `module_action` (`load` | `unload` | `reload`), `update_action`
    (`status` | `check` | `apply` | `test`).
- **`run_command` semantics:** executes any registered command through the
  bot's internal dispatcher (`ExecuteCommand`) with a **virtual captured
  context** — `guild_id`/`channel_id` supply the context (cleanup subcommands
  need `channel_id`), **nothing is posted to Discord**, and the captured
  response is returned as text. Error-colored responses (red embeds) are
  prefixed with `error: `.
- **No exec allowlist:** unlike the dashboard's `POST /api/exec`, MCP does
  **not** consult an allowlist — the bearer token is the boundary
  (owner-equivalent by design).

### Onboarding (`onboarding/`)

Runs on first launch (no `config.yml`): token, owner ID, prefix, bot name, ToS URL, Privacy URL.

### Build & Run

```bash
go build -ldflags "-X main.Version=$(./scripts/version.sh)" -o bot ./cmd/bot/   # Build (version stamped from VERSION)
./bot                              # Run (onboarding if no config)
./bot --no-modules                 # Skip all module loading
./bot --version                    # Print the stamped version (CI verifies it post-build)
go vet ./...                       # Vet
./install.sh                       # Multi-distro installer (deps + build; --skip-go/--skip-imagefilter/--check)
```

Build needs a C toolchain, `pkg-config` and `libopus-dev` + `libopusfile-dev` (the voice binding is cgo). Runtime needs `git`, `python3` + `venv`/`pip` and `ffmpeg`. `shell.nix` provides the same set for Nix users.

### Versioning

`VERSION` at the repo root (committed) is the single source of truth: it is
injected into the binary via `-ldflags "-X main.Version=$(./scripts/version.sh)"` by
CI, `install.sh`, the release workflow and the updater's self-build. A binary
built without the stamp reports `dev` (unknown version).

`scripts/version.sh` is the one VERSION reader (first non-blank, non-comment
line, trimmed, `v` stripped) **and validator** — missing file, empty/comment-only
file, or anything that is not bare SemVer (so no `1.2.3-01`, no `-rc..1`, no
`+build`) exits non-zero. CI has a dedicated `Validate VERSION` step because a
failure inside `$(…)` would not fail the build step next to it, `install.sh` dies
on it, and `updater.ReadVersionFile` + `ParseVersion` are its Go twin.

SemVer, 0.x era: `v1.0.0` is THE release, not close. Until then `v0.Y.0`
(minor bumps) carry feature waves and `v0.Y.Z` (patch) carries fixes and small
features — the standard pre-1.0 convention. **Bumping is part of the PR**: the
author edits `VERSION` in the branch (CI warns, but does not fail, when a PR
changes code without touching `VERSION`). On merge to `main` the release
workflow tags `v<VERSION>` and publishes a GitHub Release
(`.github/workflows/release.yml`); it no-ops when that tag already exists.

The updater is version-aware: `Check()` lists origin's tags (`git ls-remote
--tags`, parsed by `parseTagRefs` into name + peeled-SHA `tagRef`s,
overridable in tests via the `listTags` hook), keeps only the version-shaped
ones **merged into the tracked branch** (`reachableTags` runs
`git merge-base --is-ancestor <sha> FETCH_HEAD` against the branch Check just
fetched — a release tagged on some hotfix branch is not an update for this
one), picks the highest with `LatestVersionTag`, compares it against the
running build with `updater/semver.go`, and reports `vA → vB` (`CheckResult.
VersionSummary`) with the commit count as secondary detail. "Up to date" stays
defined by commit SHAs, so an untagged repo or an unstamped `dev` build behaves
exactly as before — versions change the reporting, never the trigger. The
newest tag seen is cached in `updater_state.json` (`LatestVersion`) and drives
the once-per-release "Update available" announcement. The cache is scoped: it
stores `LatestScope` (`repo@branch`) and `LatestVersion()` / `Status()` return
empty for any other scope, so repointing the updater at another repo or branch
cannot transiently advertise the previous target's release.

SemVer lives in `updater/semver.go` (no `golang.org/x/mod/semver` — it is not
in the module graph); components are compared as **digit strings**, so ordering
stays exact past any integer width.

`updater_state.json`'s struct is shared: read it with `stateSnapshot()`, mutate
+ persist with `updateState()` (or `editState()`/`commit()` when Discord sends
sit between the reads — the notification pass never holds `Manager.mu` across
I/O). `TestConcurrentStateAccess` runs under `-race` to keep it that way.

### Gateway Intents (Discord Dev Portal)

The bot requests `Guilds`, `GuildMessages`, `GuildMembers`, `MessageContent`,
`DirectMessages`, `GuildPresences` and `GuildVoiceStates` (`main.go`). The
privileged ones must be enabled in the Dev Portal:
- `MESSAGE_CONTENT` — prefix command parsing
- `GUILD_MEMBERS` — member tracking / permission resolution
- `PRESENCES` — presence/status updates

### Current Status

**Done:**
- Project scaffolding, auto-directory creation
- Config YAML load/save/set with validation (prefix non-empty, log_level enum)
- Async logger (stdout + file, slog JSON)
- Embed helpers (Success/Error/Info/Warning/New)
- Three-tier permission system + SuperOwnerOnly
- Module interface + Manager (Lua/Python dynamic loading + builtin registration; load/unload/reload/unloadAll)
- Module persistence via `loaded_modules.json`
- `--no-modules` CLI flag
- AutoLoad (scans `modules/Lua/` + `modules/Python/`, persists)
- Lua module system (gopher-lua, single `.lua` files, Go-Lua bridge)
- Python module system (subprocess IPC, per-module venv, Python SDK, runner script)
- Module type auto-detection (Lua file / Python dir) via `DetectModuleType` + `resolveModulePath`; anything else is unsupported (no Go plugin path)
- 13 core commands with prefix + slash equivalents
- Permission-filtered `[p]help` (hides commands user can't use); module commands grouped under a category named after the owning module (e.g. cleanup's commands appear under "Cleanup"), regardless of the `Category` field each command sets
- Slash command batch registration with mutex serialization
- Auto-delete: ONLY errors (red `embed.Error`) vanish, after 7s; every other response (success/info/warning/usage/status/plain text) stays permanently. No preserved list, no opt-in hook — dispatcher deletes iff first embed is red
- Self-updater (`updater/` package): poll loop (default 300s), PR + commit notification embeds (author row → bold title → markdown description; merge commits skipped; first poll silent), auto pull (refused unless the working tree is on the tracked branch) → rebuild the single binary → binary swap → `syscall.Exec` self-restart, `[p]update check|now|status|test|set`, `updater_state.json` persistence, live config via `[p]update set` / the dashboard / MCP
- GitHub repo `Myrukora/misfit-bot` (public since 2026-08-06) + branch/PR workflow (owner review & approval for collaborator PRs; GitHub-side branch rules enforced via the `main-protection` ruleset)
- Cleanup module (9 subcommands, pagination via `fetchMessages`)
- Tickets module (v3, per-guild): per-server config files (`config.yml` module config + `guilds/<id>.yml`), open-time question modals, continuous local mirroring of ticket media, opened + closed log-channel posts, HTML transcripts, deleted-channel finalize (via `AddGuildChannelDelete`), and a retention sweep that runs every 24h (pruning closed tickets older than the configured window — 30 days by default, 0 = keep forever). The uploaded HTML transcript is a **cache** of that stored log, not a frozen close-time snapshot: close re-renders it from a fresh read and the close tail then re-checks the log (bounded, up to 3 passes) so an entry/edit/delete landing mid-render is reconciled before the upload, and `modules.TicketTranscript.RefreshTranscript` regenerates the file on demand — previously a message or edit arriving after the close snapshot stayed in the ticket JSON but was permanently missing from the `.html`, with no way to regenerate it.
- Cache methods on Interface (GetCachedMember/Guild/Role/Channel, GetMemberRoles)
- Event hook system (19 event types, safeDispatch panic recovery)
- Hooks always cleaned up on unload even on error
- Module commands now match Aliases in prefix dispatch
- Safe snowflake parsing (no MustParse panics)
- Config validation prevents empty prefix / invalid log_level
- Auto-delete driven purely by the first embed's color; usage/status/plain-text responses stay on screen indefinitely
- Voice module (`voice.go`) — VoiceManager built for modules to use (join/leave/play/pause/volume via FFmpeg), not core bot commands
- Rate limiting (`ratelimit/` package) — 10 commands per 5 seconds per user, owner bypasses, both prefix and slash
- `[p]ratelimit` command — owner-only command to check/reset rate limits for users
- Backup verification — `config.BackupService` (`Create`/`List`/`Verify`/`Restore` with YAML validation and `confirm=true` required for restore), surfaced owner-only at `GET/POST /api/backups`; the former `[p]backup` command was removed
- Module dependencies — `Dependencies() []string` method on Module interface, checked at load time, fails if dependency missing
- Python graceful shutdown — 5-second timeout for graceful shutdown, then force kill
- Web dashboard (`internal/dashboard/`, compiled-in core — always on, no plugin): black & white monochrome redesign (2026-09). Login → `/` server picker → `/g/<id>/…` per-server pages; `/config` = bot-wide core config (super-owner only).
  - **Pages** (all `rd_*` templates): `/`+`/login` → `rd_servers`/`rd_login`, `/overview`, `/config`, `/permissions`, `/logs`, `/modules` (owner module mgmt), `/g/<id>/{commands,tickets,modules,imagefilter}`, `/tickets/<g>/<t>` → standalone `rd_transcript` (renders the conversation live from the ticket's stored log, never the `.html`; its Download transcript link hits `GET /api/tickets/<g>/<t>/transcript` — guild-gated like `/api/ticketfiles`, a GET so no CSRF — which regenerates the file from the log and downloads it as `ticket-<id>.html`). First-run setup is NOT a route: `/login` renders `rd_setup` instead of `rd_login` while the OAuth client is unconfigured. Legacy redirects: `/guild/<id>` → `/g/<id>/commands`, `/settings` → `/configuration`, `/admin` → `/config`. JSON API under `/api/*`, tiered + CSRF.
  - **rd_* convention**: one template per page in `web/templates/rd_<name>.html`; shared chrome = `rd_header.html` (renders the hub OR guild sidebar from `d.Page`/`d.GuildID`) + `rd_footer.html`. Handler shape: `d := m.baseData(us); d.Page = "x"; d.Content = …; m.tmpl.render(w, "rd_x", d)`. Schema-driven settings render through the `rd_field` partial (fieldRender struct, unchanged data contract).
  - **CSS**: single `web/static/redesign/style.css` — custom-property design tokens at the top, components below; extend tokens, never duplicate rules; black & white only, flat backgrounds (login/setup keep the branded gradient card). CSP lives in `server.go` (Google Fonts allowed).
  - **JS**: single vanilla `web/static/redesign/app.js`, no framework, no inline `<script>`. Page-activation pattern: each block activates only when its elements exist (`if (!byId("x")) return;` — see the file's header comment); CSRF from `<meta name="csrf-token">`; fetch to `/api/*`.
  - Auth: Discord OAuth2 via disgo `oauth2` (no new deps), signed session cookies (HMAC-SHA256), in-memory sessions, mutual-guild restriction. 4 RBAC tiers (`owner` > `elevated` > `staff` > `regular`) computed per request; config hidden from non-staff. Command exec `POST /api/exec` runs only names on the `exec_allowlist` (an EMPTY allowlist means nothing is runnable) and applies the dispatcher's own rules — SuperOwnerOnly is never web-reachable. Core config editable on `/config` in sections (Bot/Logging/Dashboard/Updater/Secrets — secrets owner-only, locked for elevated).
  - Backup admin: `GET /api/backups` (list) + `POST /api/backups` (create/verify/restore) — owner-only, backed by `config.BackupService`; restore requires `confirm=true`.
- Built-in MCP server (`internal/mcpserver/`, compiled-in core — always on): MCP streamable-HTTP endpoint mounted at `/mcp` on the dashboard listener (handler injected via `dashboard.Deps.MCP`; the dashboard never imports it — follows `dashboard.listen`). Bearer-token auth (`mcp.token` in core `config.yml`, auto-generated on first start and saved, logged once); config re-read per request as a live kill switch (`mcp_enabled=false` → 404, empty token → 503, bad token → 401). Enabled by default. **13 tools**: `bot_status`, `list_guilds`, `list_channels`, `get_logs`, `list_commands`, `get_config` (secrets redacted), `module_get_config` (reads); `set_config`, `module_set_config`, `run_command` (internal dispatcher + virtual captured context — nothing posted to Discord), `send_message`, `module_action` (load/unload/reload), `update_action` (status/check/apply/test). Full owner-level access — the token is the owner identity.

**Not Yet Done:**
- [ ] Discord channel logging (separate module, not core feature)
- [ ] Voice commands (voice.go built for modules to use, not core bot)
- [ ] Runtime `log_level` / log_enabled changes (require restart — to be discussed)
- [ ] Auto-restart on crash (valid point, to be discussed)

**Questionable / Later:**
- DM handling for non-owner users (currently returns 0 permissions, no friendly message — may add later)

## Security Decisions (Intentional)

These are deliberate trade-offs for a **private, single-user bot** where the owner is the sole developer and operator:

1. **`config.yml` permissions (0644)** — The bot runs on a single-user Ubuntu server. The owner has full SSH access. 0644 is acceptable since no other users exist on the system. If the bot is ever deployed to a multi-user host, change to `0600`.
2. **Lua bridge unrestricted `ctx.api()` and `ctx.http()`** — The bot is private; only the owner writes Lua modules. Full Discord API and arbitrary HTTP access is intentional for development flexibility. Before public module distribution, add URL allowlisting and endpoint whitelisting.
3. **No shell command execution** — The former `[p]eval` command (ran `sh -c`, protected by `SuperOwnerOnly`) has been **removed entirely**; the bot no longer offers any way to run shell commands. The `SuperOwnerOnly` dispatch mechanism remains for future owner-only commands.
4. **Branch + PR workflow, no direct commits to `main`** — The repo (`Myrukora/misfit-bot`, public since 2026-08-06) uses the branch workflow: `git checkout -b <feature>` → commit → `gh pr create` → **owner review + approval → merge**. PRs from collaborators require the owner's manual approval; the owner's own PRs are exempt (GitHub forbids self-approval). The bot only ever pulls `main` (fast-forward) — PR-only merges keep every GitHub merge strategy fast-forward-compatible. Server-side enforcement is provided by the `main-protection` ruleset (free for public repos): only the owner can push to `main`; PRs require an approval from Lemma-Agent (code owner). The workflow is otherwise enforced by convention.
5. **Updater GitHub token in gitignored `config.yml`** — The bot authenticates to its GitHub repo (public since 2026-08-06) via `updater.token`, injected per git invocation via `http.extraheader` (never persisted to `.git/config`). The token never appears in any commit. `.gitignore` (non-exhaustive) covers: `config.yml` + `config_backup_*.yml` / `config_pre_restore_*.yml`; `logs` + `*.log`; `updater_state.json`, `loaded_modules.json`, `command_overrides.json`, `*.tmp`; the binaries (`/bot`, `bot.new`, `bot.old`); `*.so`; per-language module runtime data (`modules/{Go,Lua,Python}/*/config*.yml`, `modules/{Python,Lua}/*/config.json`, `…/data/`, `…/logs/`, `modules/Python/*/models/`, `.venv/`, `__pycache__/`, `*.sock`/`*.pid`); the builtins' own data dirs (`modules/tickets/`, `modules/imagefilter/` incl. `models/`); legacy plugin-era paths (`modules/Go/*/{guilds,tickets,spam_images,config.json}`, `modules/Python/image_spam_filter/`); plus Go test droppings, `module_configs/`, `node_modules/`, `.env`, `scratch/`, `*.local.yml|yaml`, `lib/`, `/redesign/`, `.claude/`, `.omp/`, `.hermes/`, `.pi/`, `.tmp/`, `.worktrees/` and editor/OS files. If the gh token is ever rotated, update `updater.token` (`[p]update set token <pat>`).

## Key Gotchas

1. **`Inline` field** in `discord.EmbedField` is `*bool`. Use `util.PtrBool(true/false)`.
2. **`snowflake.ID`** is `uint64`. Use `.String()` to convert — never `string(id)`.
3. **`discord.Permissions`** (plural) is the type, not `discord.Permission`.
4. **`WithFields(fields...)` REPLACES** `e.Fields`, does NOT append. Build the slice first, pass once.
5. **No Go plugin modules** — only Lua files and Python dirs stay dynamic; `DetectModuleType` returns `""` for anything else and `Manager.Load` errors out. The feature modules (cleanup/tickets/imagefilter) are compiled-in, gated by `enabled_modules` and toggled with `[p]modules enable|disable` (applies on restart).
6. **A failed reload loses the module** — `ReloadModule` is Unload + LoadModule with no rollback: once unloaded, the old instance is gone, so a failure logs `Reload of module '<name>' failed after unload — module lost until bot restart`. Prefer `load` for a first attempt and expect a restart to recover.
7. **Slash command re-registration** is mutex-serialized. Concurrent load/unload operations queue on `SetGlobalCommands`.
8. **Logger `Close()`** waits for the processLogs goroutine to drain via `done` channel. Never close file before drain completes.
9. **CoreCommands backing array** — `help` creates a separate backing array (`make` + `copy`) before appending module commands. Never append to `CoreCommands` directly (corrupts the original slice).
10. **`Client.Rest`** is a public field, not a method.
11. **Gateway latency** via `Client.Gateway.Latency()` — requires `Client.HasGateway()` check.
12. **Slash commands** use `OnApplicationCommandInteraction`, not `OnMessageCreate`. Must respond within 3 seconds.
13. **Auto-delete** — the bot deletes **only error-colored (red, `embed.Error`) embeds**, after 7s so the user can read them. Everything else — success, info, warning, usage/reference listings, status reports, plain text — **stays on screen permanently**. No `preservedCmds` list, no `isPreserved()`, no `RespondPreserved`/`RespondPersistent` hook (all removed): the single rule is "first embed red ⇒ delete at 7s, else never". The dispatcher (`isErrorResponse()` + `errorAutoDeleteDelay` in `main.go`) decides purely from the first embed's color. Plain-text `ctx.ReplyText` always stays.
14. **Three-tier permissions** — Owner/elevated bypass everything. Guild owner bypasses `RequiredPerm` but not `OwnerOnly`/`SuperOwnerOnly`. Normal users need Discord role perms.
15. **`SuperOwnerOnly`** is checked at dispatch level before `CanUse`. Even elevated users cannot bypass it. The mechanism remains for future owner-only commands, but no core command currently uses it (the former `eval` was removed).
16. **Cache flags required** — `FlagMembers` + `FlagRoles` must be enabled. Without them, `GetUserPermissions` always returns 0.
17. **Module persistence** — `loaded_modules.json` stores loaded module names. On startup, only these are loaded. `--no-modules` to skip. AutoLoad runs when no saved modules exist.
18. **Event hooks** — register in `OnLoad` only. Bot removes all hooks on unload via `RemoveModuleHooks`.
19. **Subcommand args** — `cmd/bot/main.go` builds `ctx.Args` via `commands.SlashArgs`, which walks the command's declared options and emits `[group, sub, …values in declared order]` (flat subcommand: `[sub, …values]`; absent optional options are skipped so an omitted argument shifts nothing; values are stringified by type — channel/role/user/mentionable become the snowflake string). The subcommand's position depends on whether it lives in a group; the vector is produced in core, not by each handler.
20. **Config changes** — `[p]update set <key> <value>` and the dashboard/MCP write through `Config.Set`, which persists immediately; `log_level`, `log_enabled` and `enabled_modules` take effect only after a restart. There is no core `set` or `logs` command. Discord-channel logging is not implemented in core at all.
21. **DM permission behavior** — In DMs, `GetUserPermissions` returns 0 and `GetGuildOwnerID` returns "". Only owner/elevated can use commands with `RequiredPerm` in DMs.
22. **Config security** — `config.yml` contains the bot token **and** the Discord OAuth `client_secret` (the `oauth:` section, used by the dashboard's user-login flow) in plaintext, plus the updater PAT and the MCP bearer token. Ensure it's in `.gitignore` and never committed to version control. Back it up owner-only from the dashboard (`POST /api/backups`, backed by `config.BackupService`) — the former `[p]backup` command is gone. (0644 is acceptable for the single-user Ubuntu host per security decision #1; tighten to 0600 if ever deployed to a multi-user host.)
23. **Python module venvs** — Each Python module gets a `.venv/` directory inside its module folder. These are gitignored (`modules/*/.venv/`). The venv is created on first load and `pip install` runs only when `requirements.txt` hash changes.
24. **Python runner script** — Go launches `python3 sdk/python/misfit/runner.py <module_main_path>`, NOT the user's `main.py` directly. The runner imports `main.py`, extracts the `module` global, and manages IPC. `PYTHONPATH` is set to `sdk/python` so `import misfit` works.
25. **Python command responses are async** — Python command `Execute` closures send the command via IPC and return nil immediately. The Python process sends `respond`/`reply_text` back asynchronously. No auto-delete for Python module responses (unlike core commands).
26. **Component interactions auto-defer** — `onComponentInteraction` calls `event.DeferUpdateMessage()` before dispatch, EXCEPT when a loaded module claims the interaction via `modules.RawComponentHandler` (`ModMgr.NeedsRawComponent(event)`); a claiming module MUST always respond, since an unacknowledged claimed interaction dies on Discord's 3s deadline (Discord rejects a modal after an acknowledgement — the tickets Open button relies on this opt-out).
27. **Lua event system** — Lua modules register event callbacks via `ctx.on_event(name, fn)` inside `on_load`. Callbacks receive a Lua table with the same event data as Python modules. LState is mutex-guarded so only one Lua callback runs at a time.
28. **Dashboard runs in-process** — it is compiled-in core (`internal/dashboard/`), NOT a separate process, so a panic in an HTTP handler would crash the whole bot. That's why `server.go` wraps every request in `recoverMiddleware` → 500 JSON. Long/async work (OAuth guild fetches, log tailing) must run off the gateway goroutines.
29. **`GetClient()` / `GetStartTime()`** — two additive `commands.Interface` accessors expose the raw `*bot.Client` (cache/gateway/rest) and the bot start time to in-process modules. The dashboard gets them via `ctx.Bot.GetClient().(*bot.Client)` and `ctx.Bot.GetStartTime()`. No other type implements `commands.Interface` except `botAdapter`.
30. **Dashboard OAuth reused disgo `oauth2`** — no new dependencies. `oauth2.New(id, secret, oauth2.WithStateController(oauth2.NewStateController()))`; scopes `identify`+`guilds`. Sessions are in-memory only (lost on restart — users just log in again). Default `listen` is `127.0.0.1:8080`; bind all interfaces for LAN access, and the redirect URI follows the browser's origin when `public_url` is unset. Expose remotely via a reverse proxy/tunnel (cloudflared/nginx) and set `public_url` accordingly. (Discord DOES accept `http://` redirect URIs for LAN/localhost — do not reintroduce the "HTTPS required" claim.)
31. **`WebConfigurable` is opt-in & additive** — modules that don't implement it are unaffected (dashboard shows no config UI for them). The dashboard type-asserts each loaded module via `modules.IsWebConfigurable(mod)` and renders settings purely from `WebConfigSchema()`. `secret` fields are redacted to `••••` on read unless the caller is the owner.
32. **Dashboard config hidden from non-staff** — `regular` users (in a mutual guild but managing none) get 403 on `/config`, `/permissions`, `/logs`, `/modules` and every mutating `POST /api/*` endpoint; the nav hides those links too. Staff see only their manageable guilds' guild-scoped module fields (`/g/<id>/modules`) + their usable commands.
33. **`client_secret` lives in core `config.yml`** — set via the dashboard Admin page (`/config`, or `SetConfig("oauth_client_secret", …)`); `listen`/`public_url` go to core `config.yml` (`dashboard:` section). All three can also be set by hand in `config.yml`. **A bind failure (e.g. 8080 in use) does NOT fail startup** — the dashboard stays up so the owner can rebind via the `dashboard.listen` key in core `config.yml` and restart. Effective listen = core `dashboard.listen` if set, else `127.0.0.1:8080`; a URL-shaped value (`http://host:port/`) is normalized to `host:port` at write and bind time (`NormalizeListen`).
34. **Dashboard integration is a separate file per module, and absence = no integration** — compiled-in modules implement `WebConfigurable` in their package (tickets `webconfig.go`, the dashboard itself in `main.go`); Lua modules add `modules/Lua/<name>/<name>.dashboard.lua` (global table `D` with `schema`/`get`/`set`, its own Lua state, `ctx.data_dir` available); Python modules add `dashboard.py` next to `main.py` (`web_schema` + `web_get_config`/`web_set_config`, imported by the runner, IPC `web_get_config`/`web_set_config` messages). No file ⇒ no settings panel and no config API writes. `*.dashboard.lua` files are NOT modules: AutoLoad, `[p]load all`, `GetAvailableModuleNames`, and `DiscoverLuaModules` all skip them (`IsLuaDashboardScript`), and `LuaLoader.Load` rejects them with a clear error. Lua `min`/`max`/`step` are presence-based (0 values survive); Python config values are coerced to strings in Go (bools → "true"/"false").
