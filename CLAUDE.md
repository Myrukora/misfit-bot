# CLAUDE.md — Custom Discord Bot

## Overview

A modular Discord bot in Go. The web dashboard and the cleanup/tickets feature modules are compiled into the single binary (core infrastructure, always on); Lua scripts (`.lua` files) and Python modules (directories with `main.py`) load dynamically via subprocess IPC. Inspired by Red-DiscordBot but fully standalone. Designed for Linux only.

## Tech Stack

| Component | Technology | Version |
|-----------|-----------|---------|
| Language | Go | 1.26.4 |
| Discord Library | [disgo](https://github.com/disgoorg/disgo) | v0.19.6 |
| Config | YAML (`gopkg.in/yaml.v3`) | v3.0.1 |
| Snowflake IDs | `github.com/disgoorg/snowflake/v2` | v2.0.3 |
| Module System | Compiled-in core (dashboard, cleanup, tickets) + dynamic Lua/Python modules | stdlib |
| Lua Modules | [gopher-lua](https://github.com/yuin/gopher-lua) | v1.1.2 |
| Python Modules | Subprocess IPC (per-module venv) | Python 3 |
| Logging | `log/slog` (stdlib) + file output | stdlib |
| Target Platform | Ubuntu Server (Linux amd64) | - |

## Project Structure

```
/home/sam/bot/
├── cmd/bot/main.go           # Entry point — Discord connection, event handling, command dispatch
├── commands/
│   ├── command.go            # Core types: Command, SlashCommand, Context, Interface
│   └── core.go               # 18 core commands + auto-generated slash equivalents
├── config/
│   └── config.go             # YAML config loading/saving, Config struct, Set() with validation
├── embed/
│   └── embed.go              # Discord embed helpers (Success, Error, Info, Warning, New)
├── logger/
│   └── logger.go             # Async slog JSON to stdout + file
├── modules/
│   ├── module.go             # Module interface, Manager (load/unload via plugin package)
│   ├── lua_loader.go         # Lua module loader
│   ├── lua_module.go         # Lua module wrapper (implements Module interface)
│   ├── lua_bridge.go         # Go-Lua bridge (ctx object, logging, bot info)
│   ├── python_loader.go      # Python module loader (spawns process, waits for ready)
│   ├── python_module.go      # Python module wrapper (implements Module interface)
│   ├── python_bridge.go      # Go-Python bridge (IPC callbacks → Discord Rest)
│   ├── python_ipc.go         # IPC protocol (stdin/stdout JSON messaging)
│   └── python_venv.go        # Per-module venv + pip install management
├── sdk/python/misfit/     # Python SDK for module authors
│   ├── module.py             # Module ABC (name, version, on_load, commands, etc.)
│   ├── commands.py           # Command/SlashCommand dataclasses
│   ├── context.py            # Context/BotContext/Logger (IPC-backed)
│   ├── ipc.py                # IPC class (JSON over stdin/stdout)
│   └── runner.py             # Runner script (launched by Go, imports user main.py)
├── onboarding/
│   └── onboarding.go         # First-run setup wizard
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
├── modules/                  # Module loader infrastructure (Go) + per-language module folders
│   ├── module.go             # Module interface, Manager (load/unload via plugin package)
│   ├── lua_loader.go         # Lua module loader
│   ├── lua_module.go         # Lua module wrapper (implements Module interface)
│   ├── lua_bridge.go         # Go-Lua bridge (ctx object, logging, bot info)
│   ├── lua_webconfig.go      # Lua dashboard integration (<name>.dashboard.lua → WebConfigurable)
│   ├── python_loader.go      # Python module loader (spawns process, waits for ready)
│   ├── python_module.go      # Python module wrapper (implements Module interface)
│   ├── python_bridge.go      # Go-Python bridge (IPC callbacks → Discord Rest)
│   ├── python_ipc.go         # IPC protocol (stdin/stdout JSON messaging)
│   ├── python_venv.go        # Per-module venv + pip install management
│   ├── Go/                   # Go plugin modules (each a package dir with main.go)
│   │   ├── dashboard/        # Web dashboard plugin (HTTP server; WebConfigurable dogfood)
│   │   │   ├── main.go       # Module + Start/Stop + WebConfigurable dogfood
│   │   │   ├── config.go     # DashboardConfig (config.yml next to the module, 0600)
│   │   │   ├── auth.go       # OAuth2 login, signed session cookies, mutual-guild enforcement
│   │   │   ├── acl.go        # 4 RBAC tiers (owner/elevated/staff/regular) + route guards
│   │   │   ├── commands.go   # Command catalog filtered via canUse (mirrors [p]help)
│   │   │   ├── metrics.go    # Live metrics snapshot from cache + runtime
│   │   │   ├── api.go / api2.go # Tiered JSON API (settings/modules/logs/perms presence…)
│   │   │   ├── pages.go      # Server-rendered MEE6-like HTML pages
│   │   │   ├── server.go     # HTTP server + middleware (panic-recovery mandatory) + router
│   │   │   ├── templates.go  # go:embed templates + render()/FuncMap
│   │   │   ├── static.go     # go:embed static assets
│   │   │   ├── templates_test.go # Template render coverage (no Discord needed)
│   │   │   └── web/{templates,static}/ # Embedded HTML/CSS/JS
│   │   └── cleanup/          # Message cleanup module (9 subcommands)
│   ├── Lua/                  # Lua modules (each a folder: <name>/<name>.lua, optional <name>.dashboard.lua)
│   │   └── hello/            # Lua example module
│   └── Python/               # Python modules (each a folder with main.py, optional dashboard.py)
│       └── hello_py/         # Python example module
├── loaded_modules.json       # Module persistence (auto-managed)
└── logs/
    └── bot.log               # JSON log output
```

## Core Architecture

### Entry Point (`cmd/bot/main.go`)

Startup sequence:
1. Auto-creates `modules/` (+ `Go/`, `Lua/`, `Python/` subfolders) and `logs/`
2. Checks for `config.yml` — runs onboarding if missing
3. Creates logger, permission manager, module manager
4. Connects to Discord via disgo with `FlagMembers` + `FlagRoles` cache
5. Registers 20 event listeners (all using `safeDispatch` panic recovery)
6. Loads core commands + registers slash commands with Discord
7. Loads modules (from `loaded_modules.json` persistence, or AutoLoad scans for `.so`, `.lua`, and Python dirs on first run)
8. Handles prefix command dispatch and slash command interactions
9. Graceful shutdown via SIGINT/SIGTERM, restart via `[p]restart`

### Command System (`commands/`)

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
    GetAvailableModuleNames() []string
    LoadModule(name string) error
    UnloadModule(name string) error
    ReloadModule(name string) error
    UnloadAllModules() error
    GetModuleManager() interface{}
    GetAllModuleCommands() []Command
    GetAllModuleCommandsByModule() []ModuleCommands // module name → its prefix commands (load order); used by [p]help to group each module's commands under a category named after the module
    GetPermissionManager() *permissions.Manager
    SetPresence(activityType string, text string) error
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
}
```

**`core.go`** — 18 core commands:
- `ping`, `uptime`, `info`, `help` — public, auto-delete preserved
- `modules` — `OwnerOnly: true`, auto-delete preserved — lives in the **Modules** category (grouped with `load`/`unload`/`reload` in `[p]help`)
- `status` — `RequiredPerm: discord.PermissionAdministrator`
- `load`, `unload`, `reload` — `OwnerOnly: true`, supports `all`
- `shutdown`, `restart` — `OwnerOnly: true`
- `set`, `permissions`, `debug`, `logs`, `backup` — `OwnerOnly: true`
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
    Key, Label, Help, Type, Scope, Placeholder string
    GuildScoped bool
    Options     []string // for select/multi
    Min, Max, Step *float64 // for number/range
    // all values are strings over the wire; the module parses ints/bools itself
}

// Render types the dashboard understands (one per field; no module-side rendering):
//   toggle | text | textarea | number | range | select | multi | secret | channel | role
// Scope="global" => editable by owner/elevated only; GuildScoped=true => also by
// guild managers (staff). channel/role imply guild scope (populated from cache).
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
    DataDir string          // the module's own folder (modules/Go|Lua|Python/<name>/)
    Logger  Logger
    Rest    rest.Rest
    Bot     commands.Interface
    Events  *EventHooks
}
```

**18 event hooks** — Go modules register via `ctx.Events.Add*()` during `OnLoad`. Python modules declare event handlers in `event_handlers()`. Lua modules register via `ctx.on_event(name, callback)` during `on_load`. All dispatched through `safeDispatch()` with panic recovery.

Available hooks:
- `AddMessageCreate`, `AddMessageUpdate`, `AddMessageDelete`
- `AddGuildMessageCreate`, `AddGuildMessageUpdate`, `AddGuildMessageDelete`
- `AddGuildMemberJoin`, `AddGuildMemberLeave`
- `AddGuildBan`, `AddGuildUnban`
- `AddGuildJoin`, `AddGuildLeave`
- `AddPresenceUpdate`
- `AddMessageReactionAdd`, `AddMessageReactionRemove`
- `AddVoiceStateUpdate` — voice channel join/leave/move
- `AddComponentInteraction` — button clicks, select menus
- `AddModalSubmit` — modal form submissions

**Manager** uses `plugin.Open`:
1. `plugin.Open(path)` — opens shared object (returns same handle for same path)
2. `p.Lookup("New")` — finds exported function
3. `sym.(func() Module)()` — instantiates module
4. `mod.OnLoad(ctx)` — initializes

**Manager methods:**
- `Load(path, hooks)` — load module with event hooks (auto-detects type: go/lua/python)
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
- Everything else → "go" (Go `.so` plugin)

**Path resolution** (`resolveModulePath(modulesDir, name)` in main.go):
- Probes `name.so` → `name.lua` → `name/main.py` in order
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
- `botAdapter.LoadModule(name)` resolves path (`.so`/`.lua`/dir), creates fresh `EventHooks`, calls `ModMgr.Load(path, hooks)`, then `mod.OnLoad()`. On error, `ModMgr.Unload(name)` cleans up. Persists to `loaded_modules.json`.
- `botAdapter.UnloadModule(name)` → `ModMgr.Unload(name)` + persist.
- `botAdapter.ReloadModule(name)` → `Unload` + `LoadModule`. If reload fails, logs warning and module is lost until bot restart (Go plugin limitation — cannot roll back).

**Module loading on startup:**
1. `--no-modules` flag skips everything
2. Reads `loaded_modules.json` for previously loaded modules
3. If empty and `AutoLoad: true`, scans all `.so`, `.lua`, and Python dirs, loads them, persists to `loaded_modules.json`
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
modules:
  auto_load: true
  path: "modules"
  disabled: []
logging:
  enabled: true
  channel_id: ""
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
```

`Config.Set(key, value)` with validation:
- `prefix` — rejected if empty
- `log_level` — must be `debug`, `info`, `warn`, or `error`
- `log_enabled` — accepts `true`/`1`/`yes`
- `dashboard_listen`, `dashboard_public_url` — write the optional top-level `dashboard:` section (non-secret infra fields the dashboard module reads to pin its bind port / public URL from the main config — used when the default `127.0.0.1:8080` is taken and the web UI can't start). `dashboard_listen` is normalized to a bare `host:port` via `NormalizeListen` (see `config.go`).
- `oauth_client_secret` — write the top-level `oauth:` section. The single shared Discord-app client secret the dashboard uses for login (and any future OAuth-using module can reuse). Takes priority over the dashboard's own 0600 config fallback.
- `updater_enabled`, `updater_repo`, `updater_branch`, `updater_token`, `updater_interval`, `updater_auto_pull`, `updater_notify_channel` — write the top-level `updater:` section. Booleans are strict (reject ambiguous values), `updater_repo` must be `owner/name`, `updater_interval` must be a number ≥ 30. `Load()` applies `DefaultConfig` first, so a missing `updater:` section on an existing install comes up enabled with `branch: main` / 300s interval / auto_pull on.
- All changes auto-save to disk. Some require restart (logger level, enabled state; file channel logging not yet implemented)

### Self-Updater (`updater/`)

The bot is wired to its own GitHub repository (`Myrukora/misfit-bot`, public since 2026-08-06). The `updater.Manager` is constructed **once** in `main()` (never inside `run()`, so in-process restarts don't spawn duplicate poll loops) and runs a poll loop every `check_interval`:

1. **Notifications** — diffs GitHub state against `updater_state.json` (last commit SHA + seen PR numbers, atomic write, 0600):
   - New open PRs → embed: **author row on top** (avatar + username hyperlinked to the GitHub profile), bold title `Pull request opened: #<number> <title>` (linked to the PR), PR body as description (markdown renders), GitHub-green `0x2EA043`.
   - New commits on the tracked branch → one embed per commit: same author row, bold title `1 new commit #<sha7>`, full commit message as description, GitHub-blue `0x0969DA`. Merge commits (`Merge pull request` / `Merge branch`) are skipped.
   - **First poll seeds silently** (records HEAD + all open PRs, posts nothing); closed PRs are pruned from the seen set so a reopen re-notifies. Force-pushed history resyncs silently. Descriptions truncated to 4000 chars.
   - **At-least-once delivery**: a PR is only marked seen (and the last-seen commit SHA only advances) AFTER its embed was actually sent. Failed sends (e.g. the REST client not ready during the startup race) are retried on the next poll and survive restarts — the state file never records them as delivered. `Run()` also waits for the first `SetRest` (30s cap) so the first poll can't fire with a nil client.
2. **Auto-update** (if `auto_pull`) — `Check()` does `git fetch origin <branch>` with a per-invocation `-c http.extraheader="AUTHORIZATION: basic <base64(x-access-token:<token>)>"` (the token never lands in `.git/config`); if behind, `Apply()` runs: `git merge --ff-only FETCH_HEAD` (aborts with a clear error on local changes — bot keeps running untouched) → `go build -o bot.new ./cmd/bot/` → builds the single binary (dashboard + feature modules included) → swaps `bot`→`bot.old`, `bot.new`→`bot` → sets the apply flag and fires `OnApplied` (wired to `restartCh` with a 2s delay so the success embed is delivered).
3. **True self-update** — in the restart loop, before calling `run()` again, if `updaterMgr.ApplyRequested()` the bot `syscall.Exec`s the new binary (`Dir/bot`) — an in-process restart would keep running the OLD code. On exec failure it logs loudly and falls back to the in-process restart. The updater never runs the bot's repo commands with user-controlled input.

**`[p]update` command** (owner-only):
- `update` / `update check` — fetch + report "N new commit(s) available" or "Up to date".
- `update now` — force apply (pull → rebuild → swap → restart).
- `update status` — repo/branch/last seen SHA/last check/interval/auto_pull/last error.
- `update test` — **temporary embed tester**: posts one sample PR + one sample commit embed to `notify_channel` (markdown-rich bodies — bold/italic/code block/link/list — so the owner can verify markdown renders; the author row uses the real authenticated GitHub user when a token is set).
- `update set <key> <value>` — config keys: `enabled, repo, branch, token, interval, auto_pull, notify_channel` (routes to `Config.Set`, takes effect without restart; token value is never echoed back).

**Security notes:** the GitHub token lives only in gitignored `config.yml`; `updater_state.json` is gitignored; merge commits are skipped in notifications; the first poll is silent. The repo is public (since 2026-08-06); `main` is protected by the `main-protection` ruleset (free for public repos). The updater token and the bot's runtime state must never be committed.

### Logger (`logger/`)

- Async via channel (non-blocking)
- JSON to stdout + file (`logs/bot.log`)
- Levels: `debug`, `info`, `warn`, `error`
- Implements `modules.Logger` interface
- `Close()` waits for drain via `done` channel before closing file
- Level and file-enabled state fixed at `New()` — config changes require restart

### Onboarding (`onboarding/`)

Runs on first launch (no `config.yml`): token, owner ID, prefix, bot name, ToS URL, Privacy URL.

### Build & Run

```bash
go build -o bot ./cmd/bot/         # Build bot
./bot                              # Run (onboarding if no config)
./bot --no-modules                 # Skip all module loading
go build -o bot ./cmd/bot/  # Build the single binary (dashboard + feature modules included)
go vet ./...                       # Vet
```

### Privileged Intents (Discord Dev Portal)

- `MESSAGE_CONTENT` — prefix command parsing
- `GUILD_MEMBERS` — member tracking
- `PRESENCES` — presence/status updates

### Current Status

**Done:**
- Project scaffolding, auto-directory creation
- Config YAML load/save/set with validation (prefix non-empty, log_level enum)
- Async logger (stdout + file, slog JSON)
- Embed helpers (Success/Error/Info/Warning/New)
- Three-tier permission system + SuperOwnerOnly
- Module interface + Manager (plugin.Open, load/unload/reload/unloadAll)
- Module persistence via `loaded_modules.json`
- `--no-modules` CLI flag
- AutoLoad (scans `.so`, `.lua`, and Python dirs, persists)
- Lua module system (gopher-lua, single `.lua` files, Go-Lua bridge)
- Python module system (subprocess IPC, per-module venv, Python SDK, runner script)
- Module type auto-detection (`.so`/`.lua`/Python dir) via `DetectModuleType` + `resolveModulePath`
- 18 core commands with prefix + slash equivalents
- Permission-filtered `[p]help` (hides commands user can't use); module commands grouped under a category named after the owning module (e.g. cleanup's commands appear under "Cleanup"), regardless of the `Category` field each command sets
- Slash command batch registration with mutex serialization
- Auto-delete: ONLY errors (red `embed.Error`) vanish, after 7s; every other response (success/info/warning/usage/status/plain text) stays permanently. No preserved list, no opt-in hook — dispatcher deletes iff first embed is red
- Self-updater (`updater/` package): poll loop (default 300s), PR + commit notification embeds (author row → bold title → markdown description; merge commits skipped; first poll silent), auto pull → rebuild (core + Go plugins) → binary swap → `syscall.Exec` self-restart, `[p]update check|now|status|test|set`, `updater_state.json` persistence, live config via `[p]update set`/`[p]set updater_*`
- GitHub repo `Myrukora/misfit-bot` (public since 2026-08-06) + branch/PR workflow (owner review & approval for collaborator PRs; GitHub-side branch rules enforced via the `main-protection` ruleset)
- Cleanup module (9 subcommands, pagination via `fetchMessages`)
- Cache methods on Interface (GetCachedMember/Guild/Role/Channel, GetMemberRoles)
- Event hook system (18 event types, safeDispatch panic recovery)
- Hooks always cleaned up on unload even on error
- Module commands now match Aliases in prefix dispatch
- Safe snowflake parsing (no MustParse panics)
- Config validation prevents empty prefix / invalid log_level
- `logs` commands accurately report restart required / not implemented
- Voice module (`voice.go`) — VoiceManager built for modules to use (join/leave/play/pause/volume via FFmpeg), not core bot commands
- Rate limiting (`ratelimit/` package) — 10 commands per 5 seconds per user, owner bypasses, both prefix and slash
- `[p]ratelimit` command — owner-only command to check/reset rate limits for users
- Backup verification — `[p]backup create|verify|restore|list` with YAML validation and confirmation required for restore
- Module dependencies — `Dependencies() []string` method on Module interface, checked at load time, fails if dependency missing
- Python graceful shutdown — 5-second timeout for graceful shutdown, then force kill
- Web dashboard module (`modules/Go/dashboard/`) — MEE6-style, role-tiered web dashboard as a hot-loadable `.so` plugin:
  - Discord OAuth2 login via disgo `oauth2` (reused, **no new deps**) with signed session cookies (HMAC-SHA256), in-memory sessions, and a **mutual-guild login restriction** (the OAuth user must share ≥1 server with the bot).
  - **4 RBAC tiers** computed per request: `owner` > `elevated` > `staff` (manages ≥1 mutual guild via ManageGuild/Admin/owner) > `regular`. **All config is hidden from non-staff** at both the nav and API-middleware layers.
  - **Live metrics** (guild/member/channel/role counts, gateway latency, uptime, module counts, runtime MemStats) auto-refresh every 5s.
  - **Command catalog** filtered exactly with the same `canUse` rule as `[p]help`, aggregated across the user's mutual guilds — every logged-in user sees only the commands they can actually run. Owner/elevated get a "raw" toggle.
  - **Tiered config**: owner/elevated edit core settings + global module config + load/unload/reload + permissions + presence + logs + shutdown/restart; staff additionally edit guild-scoped module config for their servers; regular users see only `/` and `/commands`.
  - **`WebConfigurable` opt-in contract** (`modules/module.go`) — each module declares exactly what's configurable and how (toggle/text/textarea/number/range/select/multi/secret/channel/role via `ConfigField`), and the dashboard renders it purely from the schema reading/writing through `WebGetConfig/WebSetConfig`. Zero dashboard code changes needed to support a new module's settings. **Dashboard integration lives in a separate per-module script by convention**: Go modules put the three methods in their own `dashboard.go`; Lua modules declare `modules/<name>.dashboard.lua` (table `D` with `schema`/`get`/`set`); Python modules declare `dashboard.py` next to `main.py` (`web_schema` + `web_get_config`/`web_set_config`). No script/file ⇒ no dashboard integration (no panel, no config API) — the Lua/Python wrappers implement `WebConfigurable` themselves (`lua_webconfig.go`, `python_module.go`), so the dashboard never changes.
  - **Universal web command execution** — the dashboard can execute **any** registered command (core prefix/slash + any loaded module — Go, Python and Lua alike) via `POST /api/exec`, running it with a **virtual Context** whose responses are captured instead of posted to Discord (`commands.Interface.ExecuteCommand` on `botAdapter`, pure gate `commands.CanExecuteWeb`). Permission mapping mirrors the Discord dispatcher exactly: **`SuperOwnerOnly` commands are NEVER web-reachable**; `OwnerOnly` requires the requesting user to be owner/elevated; `RequiredPerm` checks the user's cached perms (no guild context → only owner/elevated pass). CSRF-protected; the Discord rate limiter is intentionally not applied (auth + CSRF + per-command checks already gate the web). Every usable command row on `/commands` gets a **Run** button (space-separated args input; `Command.WebArgs` metadata can later render typed inputs). **Python modules need zero author-side changes**: web invocations carry `source:"dashboard"` + `req_id`, the runner echoes `req_id` in `respond`/`reply_text`/`error` replies, and Go routes them to the waiting HTTP caller (5s timeout) instead of Discord. **Lua modules need zero bridge changes**: `ctx.respond`/`ctx.reply_text` already route through `ctx.Respond`/`ctx.ReplyText`, so a virtual Context captures them.
  - **Full core settings page** — the entire bot config is editable from `/settings` in five permission-tiered sections: **Bot** (prefix, owner_id, tos_url, privacy_url), **Logging** (log_level, log_enabled, log_file_path), **Dashboard** (dashboard_listen → live rebind, dashboard_public_url), **Updater** (enabled, repo, branch, interval ≥30, auto_pull, notify_channel, token) and **Secrets** (bot token, oauth_client_secret). The three secrets are **owner-only**: elevated users see them locked (`data-owneronly` + `disabled`, skipped by the JS save) and the API refuses the write server-side. The Updater section has an owner-only status panel (`GET /api/updater/status`) with actions mirroring `[p]update`: check (`POST /api/updater/check` → CheckResult), apply (`POST /api/updater/apply` → rebuild + restart via OnApplied) and test embeds (`POST /api/updater/test`). New `Config.Set` keys: `log_file_path`, `modules_auto_load`.
  - Runs in-process with the gateway → mandatory panic-recovery middleware (a handler panic would otherwise crash the bot). `[p]reload dashboard` cleanly rebinds the listener (detects "address in use" up front).
  - Dashboard infra is configured from the core `config.yml` `dashboard:` section (or the web Admin page) — there is **no `[p]dashboard` command** (the dashboard is always-on core, not a module). Default `Listen` is `127.0.0.1:8080` (localhost only). For LAN access set `dashboard.listen` to `0.0.0.0:<port>` (all interfaces) or a single interface (`192.168.1.5:8080`). When `public_url` is unset the OAuth redirect URI is derived **per request** from the browser's origin (scheme + Host, honoring `X-Forwarded-Proto`), so direct LAN/localhost access works from any address; `public_url` remains the override for tunnel/reverse-proxy setups. The session cookie's `Secure` flag is derived per request from the actual scheme (a plain-http LAN origin must NOT get a Secure cookie or login loops). **A bind failure never fails startup** — the dashboard logs the error and stays up so the owner can change the port via the `dashboard.listen` key in core `config.yml` (or the Admin page) and restart. The listen address and `public_url` can be pinned from the core `config.yml` `dashboard:` section (priority: core config > module config > default). **The OAuth `client_secret` lives in core `config.yml` under `oauth:`** (the single shared Discord-app credential, set via the Admin page / `SetConfig("oauth_client_secret", …)`; priority: core config > the 0600 module config fallback). Only the per-installation `session_secret` and the `allowed_guilds` allowlist stay in the 0600 module config. A `listen` value that looks like a URL (`http://127.0.0.1:9090/`) is normalized to `host:port` at write and bind time.

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
5. **Updater GitHub token in gitignored `config.yml`** — The bot authenticates to its GitHub repo (public since 2026-08-06) via `updater.token`, injected per git invocation via `http.extraheader` (never persisted to `.git/config`). The token never appears in any commit; `config.yml`, module runtime data (`modules/{Go,Lua,Python}/*/{config*.yml,data,logs}` + `*.so`), `updater_state.json`, `loaded_modules.json`, binaries and venvs are all gitignored. If the gh token is ever rotated, update `updater.token` (`[p]update set token <pat>`).

## Key Gotchas

1. **`Inline` field** in `discord.EmbedField` is `*bool`. Use `util.PtrBool(true/false)`.
2. **`snowflake.ID`** is `uint64`. Use `.String()` to convert — never `string(id)`.
3. **`discord.Permissions`** (plural) is the type, not `discord.Permission`.
4. **`WithFields(fields...)` REPLACES** `e.Fields`, does NOT append. Build the slice first, pass once.
5. **Module `.so` files** must match the bot's exact Go version.
6. **`plugin.Open`** on the same path returns the same cached handle — cannot truly unload code from memory. `ReloadModule` warns if reload fails because rollback is impossible.
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
19. **Subcommand args** — slash subcommand name is `ctx.Args[0]`. Branch logic on it.
20. **Config changes** — `set` and `logs` commands persist to disk. Logger changes (`log_level`, `log_enabled`) require restart. Discord-channel logging is a separate module, not core.
21. **DM permission behavior** — In DMs, `GetUserPermissions` returns 0 and `GetGuildOwnerID` returns "". Only owner/elevated can use commands with `RequiredPerm` in DMs.
22. **Config security** — `config.yml` contains the bot token **and the Discord OAuth `client_secret`** (`oauth:` section, used by the dashboard's user-login flow) in plaintext. Ensure it's in `.gitignore` and never committed to version control. Use `[p]backup` to create timestamped backups. (0644 is acceptable for the single-user Ubuntu host per security decision #1; tighten to 0600 if ever deployed to a multi-user host.)
23. **Python module venvs** — Each Python module gets a `.venv/` directory inside its module folder. These are gitignored (`modules/*/.venv/`). The venv is created on first load and `pip install` runs only when `requirements.txt` hash changes.
24. **Python runner script** — Go launches `python3 sdk/python/misfit/runner.py <module_main_path>`, NOT the user's `main.py` directly. The runner imports `main.py`, extracts the `module` global, and manages IPC. `PYTHONPATH` is set to `sdk/python` so `import misfit` works.
25. **Python command responses are async** — Python command `Execute` closures send the command via IPC and return nil immediately. The Python process sends `respond`/`reply_text` back asynchronously. No auto-delete for Python module responses (unlike core commands).
26. **Component interactions auto-defer** — The bot calls `event.DeferUpdateMessage()` on all component interactions before dispatch. Modules receive the event after deferral.
27. **Lua event system** — Lua modules register event callbacks via `ctx.on_event(name, fn)` inside `on_load`. Callbacks receive a Lua table with the same event data as Python modules. LState is mutex-guarded so only one Lua callback runs at a time.
28. **Dashboard runs in-process** — it is a `.so` plugin, NOT a separate process, so a panic in an HTTP handler would crash the whole bot. That's why `server.go` wraps every request in `recoverMiddleware` → 500 JSON. Long/async work (OAuth guild fetches, log tailing) must run off the gateway goroutines.
29. **`GetClient()` / `GetStartTime()`** — two additive `commands.Interface` accessors expose the raw `*bot.Client` (cache/gateway/rest) and the bot start time to in-process modules. The dashboard gets them via `ctx.Bot.GetClient().(*bot.Client)` and `ctx.Bot.GetStartTime()`. No other type implements `commands.Interface` except `botAdapter`.
30. **Dashboard OAuth reused disgo `oauth2`** — no new dependencies. `oauth2.New(id, secret, oauth2.WithStateController(oauth2.NewStateController()))`; scopes `identify`+`guilds`. Sessions are in-memory only (lost on restart — users just log in again). Default `listen` is `127.0.0.1:8080`; bind all interfaces for LAN access, and the redirect URI follows the browser's origin when `public_url` is unset. Expose remotely via a reverse proxy/tunnel (cloudflared/nginx) and set `public_url` accordingly (Discord requires HTTPS redirect URIs outside localhost).
31. **`WebConfigurable` is opt-in & additive** — modules that don't implement it are unaffected (dashboard shows no config UI for them). The dashboard type-asserts each loaded module via `modules.IsWebConfigurable(mod)` and renders settings purely from `WebConfigSchema()`. `secret` fields are redacted to `••••` on read unless the caller is the owner.
32. **Dashboard config hidden from non-staff** — `regular` users (in a mutual guild but managing none) get 403 on `/settings`, `/modules`, `/permissions`, `/logs` and every mutating `POST /api/*` endpoint; the nav hides those links too. Staff see only their manageable guilds' guild-scoped module fields + their usable commands.
33. **`client_secret` lives in core `config.yml`** — set via the dashboard Admin page (or `SetConfig("oauth_client_secret", …)`); `listen`/`public_url` go to core `config.yml` (`dashboard:` section). All three can also be set by hand in `config.yml`. `session_secret` (the cookie-signing key, auto-generated) and `allowed_guilds` stay in `modules/Go/dashboard/config.yml` (mode 0600, the only remaining dashboard secret). **A bind failure (e.g. 8080 in use) does NOT fail startup** — the dashboard stays up so the owner can rebind via the `dashboard.listen` key in core `config.yml` (or the Admin page) and restart. Effective listen = core `dashboard.listen` if set, else the module config `listen`, else `127.0.0.1:8080`; a URL-shaped value (`http://host:port/`) is normalized to `host:port` at write and bind time (`NormalizeListen`).
34. **Dashboard integration is a separate file per module, and absence = no integration** — Go modules declare `WebConfigurable` in their own `dashboard.go`; Lua modules add `modules/Lua/<name>/<name>.dashboard.lua` (global table `D` with `schema`/`get`/`set`, its own Lua state, `ctx.data_dir` available); Python modules add `dashboard.py` next to `main.py` (`web_schema` + `web_get_config`/`web_set_config`, imported by the runner, IPC `web_get_config`/`web_set_config` messages). No file ⇒ no settings panel and no config API writes. `*.dashboard.lua` files are NOT modules: AutoLoad, `[p]load all`, `GetAvailableModuleNames`, and `DiscoverLuaModules` all skip them (`IsLuaDashboardScript`), and `LuaLoader.Load` rejects them with a clear error. Lua `min`/`max`/`step` are presence-based (0 values survive); Python config values are coerced to strings in Go (bools → "true"/"false").
