# Module Development Guide

A self-contained guide for creating `.so` plugin modules for this Discord bot.

## What Is a Module?

A Go shared library (`.so`) loaded at runtime that adds commands, slash commands, event listeners, background tasks, or any other functionality. Hot-loadable via `[p]load`/`[p]unload`/`[p]reload`.

> **Crucial: every command shows up in `[p]help`.** Module commands are
> automatically listed (under a category named after the **module** —
> title-cased) the moment the module loads. For that to look right, **every
> command must have a non-empty `Description`** — it's the text shown after `—`
> (`?cleanup — Message cleanup commands`). A blank one renders as `?name —`.
> This applies to **Go, Python, and Lua** modules alike. See
> [How module commands appear in `[p]help`](#how-module-commands-appear-in-phelp).
> The dashboard can additionally render a module's settings panel with zero
> dashboard code if the module implements `WebConfigurable` — see
> [Exposing settings on the dashboard](#exposing-settings-on-the-dashboard-moduleswebconfigurable).

## Prerequisites

- Go 1.26.4 (must match the bot's Go version exactly)
- Linux (`.so` plugins only work on Linux)
- The bot's source code (for import paths)

## Module Structure

**Go modules:**
```
modules/Go/
└── mymodule/
    ├── main.go          # Module entry point
    ├── commands.go      # Commands (optional)
    ├── handlers.go      # Event handlers (optional)
    ├── config.go        # Config (optional)
    └── dashboard.go     # Dashboard integration (optional) — WebConfigurable
```

**Python modules:**
```
modules/Python/
└── mymodule/
    ├── main.py          # Module entry point (must define `module` global)
    ├── dashboard.py     # Dashboard integration (optional) — web_schema + web_get/set
    └── requirements.txt # Optional dependencies (auto-installed to per-module venv)
```

**Lua modules:**
```
modules/Lua/
└── mymodule/
    ├── mymodule.lua      # Module script
    └── mymodule.dashboard.lua # Dashboard integration (optional) — table `D`
```

> **Dashboard integration is a separate file by convention.** The module's
> logic lives in its own script(s); the dashboard settings panel lives in a
> dedicated integration file (`dashboard.go` / `dashboard.py` /
> `<name>.dashboard.lua`). **No integration file ⇒ no dashboard integration**:
> the Settings page shows no panel for the module and the config API refuses
> writes. Everything is rendered purely from the file's schema — the
> dashboard code never changes. See
> [Dashboard integration script](#dashboard-integration-script).

## Minimum Viable Module

```go
// modules/Go/hello/main.go
package main

import (
    "github.com/misfit/bot/commands"
    "github.com/misfit/bot/embed"
    "github.com/misfit/bot/modules"
)

type HelloModule struct{}

func (m *HelloModule) Name() string        { return "hello" }
func (m *HelloModule) Version() string     { return "1.0.0" }
func (m *HelloModule) Description() string { return "A simple hello module" }
func (m *HelloModule) Author() string      { return "YourName" }

func (m *HelloModule) OnLoad(ctx *modules.Context) error {
    ctx.Logger.Info("Hello module loaded!")
    return nil
}

func (m *HelloModule) OnUnload() error { return nil }

func (m *HelloModule) Commands() []commands.Command {
    return []commands.Command{
        {
            Name:        "hello",
            Description: "Says hello",
            Usage:       "hello",
            Execute: func(ctx *commands.Context) error {
                return ctx.Respond(embed.Info("Hello!", "👋"))
            },
        },
    }
}

func (m *HelloModule) SlashCommands() []commands.SlashCommand { return nil }
func (m *HelloModule) Dependencies() []string          { return nil } // no deps — method still required by the interface

func New() modules.Module { return &HelloModule{} }
```

**Build:**
```bash
go build -ldflags "-X main.Version=$(./scripts/version.sh)" -o bot ./cmd/bot/   # feature modules are compiled into the single binary
```

**Load:**
```bash
[p]load hello
```

### Python Module Example

```python
# modules/Python/hello_py/main.py
from misfit import Module, Command, SlashCommand


class HelloModule(Module):
    name = "hello_py"
    version = "1.0.0"
    description = "A simple hello module (Python)"
    author = "YourName"

    def on_load(self, ctx):
        ctx.logger.info("Hello Python module loaded!")

    def on_unload(self):
        pass

    def commands(self):
        return [
            Command(
                name="hello",
                description="Says hello from Python",  # REQUIRED — shown after `—` in [p]help
                usage="hello",
                category="fun",  # only used in single-command help + the web catalog; [p]help groups by module name regardless
                execute=self.hello_command,
            ),
        ]

    def slash_commands(self):
        return [
            SlashCommand(
                name="hello",
                description="Says hello from Python",
                execute=self.hello_command,
            ),
        ]

    def event_handlers(self):
        # REQUIRED: Return dict of event name -> handler function
        # Even if no events, return empty dict {}
        return {}

    def hello_command(self, ctx):
        ctx.respond("Hello!", "👋 Hello from Python!")


module = HelloModule()  # REQUIRED: must assign to `module` global
```

**Load:**
```
[p]load hello_py
```

**Note:** `event_handlers()` is a required abstract method. Even if your module doesn't handle events, return an empty dict `{}`. Missing this will cause a `TypeError` at module instantiation.

## Module Interface

```go
type Module interface {
    Name() string                   // Unique, no spaces
    Version() string                // Semver
    Description() string            // Short description
    Author() string                 // Author name
    OnLoad(ctx *Context) error      // Called on load
    OnUnload() error                // Called on unload
    Commands() []commands.Command   // Prefix commands
    SlashCommands() []commands.SlashCommand  // Slash commands
    Dependencies() []string         // Other module names this one needs; bot refuses to load if a dep is missing. Return nil for none.
}
```

An empty `[]string` from `Dependencies()` means "no dependencies" — you still 
must implement the method (the interface requires it); that's why even the
trivial example below returns `nil`.

### `OnLoad(ctx *modules.Context)`

Initialize state, load config, register event hooks:

```go
func (m *MyModule) OnLoad(ctx *modules.Context) error {
    ctx.Logger.Info("Module %s v%s loaded", m.Name(), m.Version())
    return nil
}
```

**Context fields:**

| Field | Type | Description |
|-------|------|-------------|
| `BotName` | `string` | Bot's configured name |
| `OwnerID` | `string` | Bot owner's Discord user ID |
| `DataDir` | `string` | The module's own folder (`modules/Go/<name>/`, `modules/Python/<name>/`, `modules/Lua/<name>/`) — configs/saves/logs live next to the module |
| `Logger` | `modules.Logger` | Logger (Debug/Info/Warn/Error) |
| `Rest` | `rest.Rest` | Discord REST API client |
| `Bot` | `commands.Interface` | Bot function access |
| `Events` | `*EventHooks` | Register event listeners |

### `OnUnload()`

Clean up goroutines, close connections:

```go
func (m *MyModule) OnUnload() error {
    // Stop goroutines, close connections
    return nil
}
```

### `Name()`

Unique, no spaces. Used as filename (`name.so`) and for `[p]load name`.

## Commands

### Prefix Command

```go
commands.Command{
    Name:        "greet",
    Description: "Greets a user",
    Usage:       "greet <user>",
    RequiredPerm: discord.PermissionBanMembers,
    OwnerOnly:   false,
    Aliases:     []string{"hi", "sayhi"},
    Execute: func(ctx *commands.Context) error {
        // ctx.Args[0] is first arg after command name
        return nil
    },
}
```

Module command aliases are checked in prefix dispatch (fixed for module commands).

### How module commands appear in `[p]help`

Every module command is automatically grouped under a **category named after the owning module** (title-cased), regardless of the `Category` field you set on the command. So the `cleanup` module's commands appear under a ** ▸ Cleanup ** section, and the `dashboard` module's command appears under ** ▸ Dashboard **. The user never sees your commands scattered across a shared `modules` or `fun` bucket.

```text
📖 Bot Help
▸ Core
?help — Show available commands
…
▸ Cleanup
?cleanup — Message cleanup commands
▸ Dashboard
?dashboard — Manage the web dashboard …
▸ General
?ping — Check bot latency
…
```

Rules:
- The category name comes from the module's `Name()` method (title-cased to “Cleanup”, “Dashboard”, etc.).
- Commands are still filtered by `canUse` exactly like core commands — a user who lacks `RequiredPerm` won't see the command at all.
- A module that exposes **no commands** doesn't get an empty section — it's skipped.
- The `Category` field per command is **only** used by single-command help (`[p]help <name>` shows it as “Category: <value>”) and by the dashboard's web command catalog (which groups by module first, then category). It no longer affects how module commands are bucketed in `[p]help`.
- **Always set `Description`** — it's the text that appears after `—` in `[p]help`. A blank `Description` shows as `?name —` (blank), which is what the user sees for poorly-written modules. The dashboard module's command had this bug and was fixed. Write it as a plain, user-friendly sentence of what the command does. The prefix `Command.Description` and the slash `SlashCommand.Description` are **separate fields** — set both; Discord hard-limits the slash description to 100 chars.

This grouping is powered by `ctx.Bot.GetAllModuleCommandsByModule()` (adds `commands.ModuleCommands{ Name, Commands }`), which replaces the old flat `GetAllModuleCommands()` for help rendering. Module authors don't need to do anything — just give each command a `Name`, a `Description`, and optionally `RequiredPerm`/`OwnerOnly`.

### Slash Command

```go
commands.SlashCommand{
    Name:        "greet",
    Description: "Greets a user",
    Options: []discord.ApplicationCommandOption{
        discord.ApplicationCommandOptionString{
            Name:        "user",
            Description: "The user to greet",
            Required:    true,
        },
    },
    RequiredPerm: discord.PermissionBanMembers,
    OwnerOnly:    false,
    Execute: func(ctx *commands.Context) error {
        return nil
    },
}
```

### Field Reference

| Field | Type | Description |
|-------|------|-------------|
| `Name` | `string` | Command name (lowercase, no spaces). Used for `[p]name` and `/name` |
| `Description` | `string` | **Crucial — shown after the command in `[p]help`** (`?name — description`). Every command **must** have a meaningful one or it shows blank. |
| `Usage` | `string` | Shown in `[p]help name` single-command view |
| `Category` | `string` | Single-command help only (`[p]help <name>` shows it). Module commands are **auto-grouped** under the module name — see below. |
| `RequiredPerm` | `discord.Permissions` | Discord permission required. `0` = everyone |
| `OwnerOnly` | `bool` | `true` = bot owner + elevated only |
| `Aliases` | `[]string` | Alternative names (prefix only) |
| `Execute` | `func(ctx *Context) error` | Command logic |

### Permission System

**Three tiers:**

| Tier | Who | Access |
|------|-----|--------|
| Bot owner + elevated | Bot owner (config) + elevated users (`[p]permissions add`) | Everything — bypasses all checks |
| Guild owner | Owner of the Discord server | Bypasses `RequiredPerm`, NOT `OwnerOnly` |
| Normal users | Everyone else | Checked via Discord role permissions (`RequiredPerm`) |

**SuperOwnerOnly** — Only the bot owner (config `owner_id`) can use. Not bypassed by elevated users or guild owners. No core command currently uses it (the former `eval` command was removed); the mechanism is retained for future owner-only commands.

**Priority:**
```
If user is bot owner or elevated → always allowed
If SuperOwnerOnly (checked at dispatch) → only bot owner
If OwnerOnly = true → only bot owner + elevated
If guild owner → allowed (bypasses RequiredPerm)
If RequiredPerm != 0 → user must have that Discord permission
Otherwise → everyone can use
```

**Important behaviors:**
- Commands with `RequiredPerm` set are **hidden from `[p]help`** for users who lack that permission.
- Denied users see "Permission Denied" embed (red → auto-deletes after 7s; see [Auto-Delete](#auto-delete)).
- The **bot** must also have the corresponding Discord permission to execute API actions.

**Examples:**

```go
// Owner + elevated only
commands.Command{
    Name:      "shutdown",
    OwnerOnly: true,
}

// Bot owner only (not elevated, not guild owner)
commands.Command{
    Name: "super-secret",
    OwnerOnly: true,
    SuperOwnerOnly: true,
    // Checked at dispatch level before CanUse
}

// Anyone with BAN_MEMBERS permission
commands.Command{
    Name:         "ban",
    RequiredPerm: discord.PermissionBanMembers,
}

// Guild owner bypasses RequiredPerm — any guild owner can ban
```

**Common permission constants:**

`discord.PermissionKickMembers`, `discord.PermissionBanMembers`, `discord.PermissionManageMessages`, `discord.PermissionManageChannels`, `discord.PermissionManageGuild`, `discord.PermissionAdministrator`, `discord.PermissionManageRoles`, `discord.PermissionModerateMembers`, and many more. Use `0` for everyone.

### Responding

```go
// With embed
return ctx.Respond(embed.Success("✅ Done", "Operation completed."))   // stays on screen (non-error)
return ctx.Respond(embed.Error("❌ Error", "Something failed."))          // auto-deletes after 7s (it's the only kind that does)
return ctx.Respond(embed.Info("ℹ️ Info", "Here's some info."))           // stays on screen
return ctx.Respond(embed.Warning("⚠️ Warning", "Be careful!"))          // stays on screen

// Multiple embeds
return ctx.Respond(embed1, embed2)

// Plain text — also stays on screen
return ctx.ReplyText("Hello, world!")
```

There's just one response method for embeds — `ctx.Respond`. See
**[Auto-Delete](#auto-delete)**: the bot deletes **only** error-colored (red)
responses (after 7s); everything else (success / info / warning / usage
listings / status reports / plain text) stays on screen permanently. You don't
need to do anything special to keep a usage listing or status report visible —
just don't make it red (`embed.Error`).

### Auto-Delete

The rule is deliberately simple: **only error messages disappear; everything else stays.**

| Response kind | Behavior |
|---------------|----------|
| **Error** — anything built with `embed.Error(…)` (red, `embed.ColorError`/`0xED4245`: permission denied, command not found, command failed, rate-limited, etc.) | **Auto-deletes after 7 seconds** so the user can read it, then vanishes. |
| Success / info / warning (`embed.Success`, `embed.Info`, `embed.Warning`) | **Stays permanently** — never auto-deleted. |
| Usage / reference listings, subcommand menus, `⚠️ Usage` hints, status reports, help-style output | **Stays permanently** — they aren't red. |
| Plain text (`ctx.ReplyText`) | **Stays permanently.** |

The dispatcher (`isErrorResponse()` + `errorAutoDeleteDelay` in `cmd/bot/main.go`)
inspects the **first embed's color**: red ⇒ schedule a delete after 7s;
otherwise the message is left alone. There is no per-command "preserve" list and
no opt-in hook to remember — if you don't want a response to vanish, just don't
build it with `embed.Error(…)`.

```go
// A result that should stay on screen — any non-red embed:
return ctx.Respond(embed.Success("✅ Done", "Finished."))    // green → stays
return ctx.Respond(embed.Info("📊 Dashboard Status", "…"))  // blurple → stays
return ctx.Respond(embed.Warning("⚠️ Usage", "mymodule <sub>")) // yellow → stays

// A genuine error — vanishes after 7s:
return ctx.Respond(embed.Error("❌ Error", "invalid argument"))  // red → deletes at 7s
```

So `[p]cleanup` (subcommand menu), `[p]help`, and any
one-shot success result all stay on screen because they aren't red. Only the
red error embeds auto-delete.

### Arguments

`ctx.Args` is `[]string` of space-separated arguments after the command name:

```
[p]ban @user spamming
    ctx.Args[0] = "@user"
    ctx.Args[1] = "spamming"
```

For slash subcommands, `ctx.Args[0]` is the subcommand name:
```
/purge messages limit:100
    ctx.Args[0] = "messages"
    ctx.Args[1] = "100"
```

### Bot Info

```go
ctx.Bot.GetPrefix()             // "?"
ctx.Bot.GetName()               // Bot name
ctx.Bot.GetVersion()            // Version string
ctx.Bot.GetOwnerID()            // Owner's Discord user ID
ctx.Bot.IsOwner(id)             // Check if user is owner
ctx.Bot.IsElevated(id)          // Check if elevated
ctx.Bot.GetSelfUserID()         // Bot's own user ID
ctx.Bot.GetGuildOwnerID(id)     // Guild owner ID
ctx.Bot.GetToS()                // Terms of Service URL
ctx.Bot.GetPrivacy()            // Privacy URL
ctx.Bot.GetConfigDir()          // Bot's working directory
ctx.Bot.GetLatency()            // Gateway latency ("45ms")
ctx.Bot.SetConfig("key", "val") // Persist config change
ctx.Bot.SetPresence("playing", "text")  // Set activity
```

### Module Management from Commands

```go
ctx.Bot.GetLoadedModuleNames()   // []string of loaded names
ctx.Bot.LoadModule("mymodule")   // Load by name
ctx.Bot.UnloadModule("mymodule") // Unload
ctx.Bot.ReloadModule("mymodule") // Reload (may lose module if OnLoad fails — warns in log)
ctx.Bot.UnloadAllModules()       // Unload all
ctx.Bot.GetAvailableModuleNames() // []string of .so files found
```

## Cache Access

Cache reads are instant (no HTTP). All return `nil`/nil on miss:

```go
// Full member object
member := ctx.Bot.GetCachedMember(guildID, userID)
if member != nil {
    name := member.User.Username
    nick := member.Nick
    roleIDs := member.RoleIDs
}

// Guild info
guild := ctx.Bot.GetCachedGuild(guildID)
if guild != nil {
    ownerID := guild.OwnerID.String()
    name := guild.Name
}

// Single role
role := ctx.Bot.GetCachedRole(guildID, roleID)
if role != nil {
    perm := role.Permissions
    position := role.Position
}

// Any guild channel
channel := ctx.Bot.GetCachedChannel(channelID)
if channel != nil {
    name := channel.Name()
    chanType := channel.Type()
}

// All roles a member has
roles := ctx.Bot.GetMemberRoles(guildID, userID)
for _, role := range roles {
    if role.Permissions.Has(discord.PermissionAdministrator) {
        // user is admin
    }
}
```

**Cache vs REST guidance:**

| Use Case | Recommended |
|----------|-------------|
| Permission checks (every command) | Cache (handled by core) |
| XP/activity tracking (high frequency) | Cache |
| Moderation role hierarchy | Cache |
| Welcome messages | Cache (event fires after cache update) |
| Ban/kick/mute execution | REST (`ctx.Rest`) |
| Fetching message by ID | REST (not cached) |
| Channel permission checks | Cache or REST |

## Embed Helpers

```go
embed.Success("✅ Title", "Description")   // Green
embed.Error("❌ Title", "Description")     // Red
embed.Info("ℹ️ Title", "Description")      // Blurple
embed.Warning("⚠️ Title", "Description")   // Yellow
```

**Custom embed:**
```go
e := embed.New().
    WithTitle("My Embed").
    WithDescription("Custom description").
    WithColor(0xFF0000).
    WithFields(
        discord.EmbedField{Name: "Field1", Value: "Value1", Inline: ptrBool(true)},
    ).
    WithTimestamp(time.Now())
return ctx.Respond(e)
```

**CRITICAL:** `WithFields(fields...)` replaces, does NOT append. Build the full slice first, then pass once.

**`ptrBool` helper:**
```go
func ptrBool(b bool) *bool { return &b }
```

## Module Configuration

Modules can have their own config next to the module — `ctx.DataDir` is the
module's own folder (`modules/Go/<name>/`, `modules/Python/<name>/`,
`modules/Lua/<name>/`):

```go
func (m *MyModule) OnLoad(ctx *modules.Context) error {
    configPath := filepath.Join(ctx.DataDir, "config.yml")
    data, err := os.ReadFile(configPath)
    if err != nil {
        // Create default
        defaultCfg := MyConfig{...}
        data, _ := yaml.Marshal(defaultCfg)
        os.MkdirAll(ctx.DataDir, 0755)
        os.WriteFile(configPath, data, 0644)
        m.config = defaultCfg
        return nil
    }
    yaml.Unmarshal(data, &m.config)
    return nil
}
```

## Logger

```go
ctx.Logger.Debug("Debug info: %v", data)
ctx.Logger.Info("Module loaded: %s", name)
ctx.Logger.Warn("Something might be wrong: %s", reason)
ctx.Logger.Error("Failed to initialize: %v", err)
```

## Event Hooks

Register in `OnLoad`. Bot removes all hooks automatically on unload:

| Method | Fires when |
|--------|-----------|
| `AddMessageCreate(func(e *events.MessageCreate))` | Any message (including DMs) |
| `AddMessageUpdate(func(e *events.MessageUpdate))` | Message edited |
| `AddMessageDelete(func(e *events.MessageDelete))` | Message deleted |
| `AddGuildMessageCreate(func(e *events.GuildMessageCreate))` | Guild message only |
| `AddGuildMessageUpdate(func(e *events.GuildMessageUpdate))` | Guild message edited |
| `AddGuildMessageDelete(func(e *events.GuildMessageDelete))` | Guild message deleted |
| `AddGuildMemberJoin(func(e *events.GuildMemberJoin))` | Member joins guild |
| `AddGuildMemberLeave(func(e *events.GuildMemberLeave))` | Member leaves/kicked/banned |
| `AddGuildBan(func(e *events.GuildBan))` | Member banned |
| `AddGuildUnban(func(e *events.GuildUnban))` | Member unbanned |
| `AddGuildJoin(func(e *events.GuildJoin))` | Bot joins guild |
| `AddGuildLeave(func(e *events.GuildLeave))` | Bot leaves guild |
| `AddPresenceUpdate(func(e *events.PresenceUpdate))` | Member status changes |
| `AddMessageReactionAdd(func(e *events.MessageReactionAdd))` | Reaction added |
| `AddMessageReactionRemove(func(e *events.MessageReactionRemove))` | Reaction removed |
| `AddVoiceStateUpdate(func(e *events.GuildVoiceStateUpdate))` | Member joins/leaves/moves voice channels |
| `AddComponentInteraction(func(e *events.ComponentInteractionCreate))` | Button click or select menu interaction |
| `AddModalSubmit(func(e *events.ModalSubmitInteractionCreate))` | Modal form submission |

All hooks wrapped in `safeDispatch` with panic recovery — a module panic won't crash the bot.

### Python & Lua Event Handlers

Python modules declare event handlers via `event_handlers()` returning a dict:

```python
def event_handlers(self):
    return {
        "guild_message_create": self.on_message,
        "voice_state_update": self.on_voice_state,
        "component_interaction": self.on_button,
    }
```

Lua modules register event handlers via `ctx.on_event()` inside `on_load`:

```lua
function M:on_load(ctx)
    ctx.on_event("guild_message_create", function(data)
        -- data.guild_id, data.channel_id, data.author, data.content
    end)
    ctx.on_event("voice_state_update", function(data)
        -- data.guild_id, data.user_id, data.channel_id (nil if disconnected)
    end)
    ctx.on_event("component_interaction", function(data)
        -- data.custom_id, data.channel_id, data.user_id
    end)
end
```

### Component Interactions (Python & Lua)

Components (buttons, select menus) arrive as `component_interaction` events. The event data includes:

| Field | Type | Description |
|-------|------|-------------|
| `custom_id` | string | The button/select custom ID you set |
| `channel_id` | string | Channel the interaction happened in |
| `user_id` | string | User who clicked |
| `guild_id` | string *(optional)* | Guild ID if in a guild |

To respond to a component interaction (e.g., show a modal), use `bot.rest` from Python or `ctx.api()` from Lua to call the Discord API directly.

### Modal Submits (Python & Lua)

Modal form submissions arrive as `modal_submit` events. The event data includes:

| Field | Type | Description |
|-------|------|-------------|
| `custom_id` | string | The modal's custom ID |
| `channel_id` | string | Channel the modal was submitted from |
| `user_id` | string | User who submitted |
| `guild_id` | string *(optional)* | Guild ID |
| `components` | array | Submitted values: `{custom_id, value}` |

```python
# Python example
def on_modal(self, data):
    for comp in data.get("components", []):
        if comp["custom_id"] == "feedback_text":
            text = comp["value"]
            self.bot.logger.info(f"Feedback: {text}")
```

### Welcome Module Example

```go
type WelcomeConfig struct {
    ChannelID string `yaml:"channel_id"`
    Message   string `yaml:"message"`
}

type WelcomeModule struct {
    config WelcomeConfig
}

func (m *WelcomeModule) OnLoad(ctx *modules.Context) error {
    configPath := filepath.Join(ctx.DataDir, "config.yml")
    if _, err := os.Stat(configPath); os.IsNotExist(err) {
        m.config = WelcomeConfig{ChannelID: "", Message: "Welcome, %s!"}
        os.MkdirAll(ctx.DataDir, 0755)
        data, _ := yaml.Marshal(m.config)
        os.WriteFile(configPath, data, 0644)
    } else {
        data, _ := os.ReadFile(configPath)
        yaml.Unmarshal(data, &m.config)
    }

    if m.config.ChannelID != "" {
        ctx.Events.AddGuildMemberJoin(func(e *events.GuildMemberJoin) {
            msg := fmt.Sprintf(m.config.Message, e.User.Mention())
            ctx.Rest.CreateMessage(m.config.ChannelID,
                discord.NewMessageCreateBuilder().SetContent(msg).Build())
        })
    }

    ctx.Logger.Info("Welcome module loaded!")
    return nil
}
```

## Building & Loading

```bash
# Build module (Go plugin; output lives next to the source, gitignored)
go build -ldflags "-X main.Version=$(./scripts/version.sh)" -o bot ./cmd/bot/   # feature modules are compiled into the single binary

# Load in Discord
[p]load <name>

# Unload
[p]unload <name>

# Reload (unload + load; may lose module if OnLoad fails — warning logged, restart needed)
[p]reload <name>

# All at once
[p]load all
[p]unload all
[p]reload all
```

## Gotchas

1. **Same Go version** — Module `.so` must match the bot's Go version exactly (1.26.4).
2. **Linux only** — Go plugins don't work on macOS or Windows.
3. **Can't truly unload** — `plugin.Open` caches the handle. `Unload` calls `OnUnload()` but code stays in memory. `ReloadModule` logs a warning if `OnLoad` fails because rollback is impossible.
4. **No spaces in module name** — Becomes the `.so` filename. Use lowercase + underscores.
5. **Embed `Inline` is `*bool`** — Use `ptrBool(true/false)`.
6. **`WithFields` replaces** — Build the full `[]discord.EmbedField` slice first, then call `WithFields(fields...)` once.
7. **`ctx.Args[0]`** is the first argument after the command name, not the command itself. For slash subcommands, the subcommand name is `ctx.Args[0]`.
8. **`OwnerOnly` overrides `RequiredPerm`** — If `OwnerOnly: true`, `RequiredPerm` is never checked.
9. **`SuperOwnerOnly`** — Only the bot owner passes. Even elevated users are blocked. Checked at dispatch before `CanUse`.
10. **Slash commands register with Discord** — Module slash commands appear in the command picker via `SetGlobalCommands`. Registration is mutex-serialized.
11. **Module config directory** — `ctx.DataDir` is auto-created. Use it for per-module config.
12. **Mention parsing** — User input may be `<@ID>` or `<@!ID>`. Use `extractID()` or handle both formats.
13. **Set `RequiredPerm` for moderation** — Commands that ban, kick, delete messages should set `RequiredPerm`. Guild owner bypasses this. The bot also needs the corresponding permission.
14. **Auto-delete** — the bot deletes **only** error-colored (red, `embed.Error`) embeds, after 7s so they're readable. Everything else — success / info / warning / usage-listings / status reports / plain text — **stays on screen permanently**. No preserved list, no opt-in hook: don't build a response with `embed.Error(…)` and it won't vanish. `[p]cleanup` menus and `[p]help` all stay because they aren't red.
15. **Module persistence** — Only previously loaded modules (from `loaded_modules.json`) load on startup. `--no-modules` to skip. `AutoLoad` scans `.so` files when no saved modules exist.
16. **Event hooks in `OnLoad`** only — Register listeners in `OnLoad`, not in commands or goroutines. The bot removes all hooks on unload.
17. **Subcommand args** — For slash subcommands, `ctx.Args[0]` is the subcommand name. Branch your logic on it.
18. **DMs** — In DMs, `GetUserPermissions` returns 0 and `GetGuildOwnerID` returns "". Commands with `RequiredPerm` only work for owner/elevated in DMs.
19. **Cache methods** — All return nil/nil slice on cache miss. Always nil-check before use.
20. **Config changes** — `SetConfig` persists to disk immediately. Logger level/enabled changes require restart. Discord-channel logging is not yet implemented.
21. **Component interactions auto-defer** — The bot calls `DeferUpdateMessage()` on all component interactions before dispatching to modules. You cannot respond via the interaction after deferral; use `bot.rest.create_message()` (Python) or `ctx.api()` (Lua) instead.
22. **Lua events use ctx.on_event** — Call `ctx.on_event("event_name", callback)` inside `on_load`. Callbacks receive a Lua table with the same fields as Python event data. LState is mutex-guarded — only one Lua callback runs at a time, so slow callbacks block other Lua commands and events.
23. **Python events return data dicts** — Event handler functions receive a plain dict. See the event data tables above for field details.

---

## Example: A long-running background service — the Web Dashboard module

The dashboard (`modules/Go/dashboard/`) is the reference for two patterns at once:
running a **non-command background service inside a module**, and enabling
**web-editable settings** via the optional `WebConfigurable` contract.

### Running a background HTTP server

`OnLoad` builds and starts an `http.Server` in its own goroutine; `OnUnload`
shuts it down with a 5s timeout. Because the dashboard is a `.so` plugin running
**in-process with the gateway**, every HTTP handler is wrapped in a
panic-recovery middleware — an unrecovered panic would otherwise crash the whole
bot. Long/async work (OAuth guild fetches, log tailing) runs on its own
goroutine, never the gateway's.

```go
func (m *DashboardModule) OnLoad(ctx *modules.Context) error {
    m.ctx = ctx
    if c, ok := ctx.Bot.GetClient().(*bot.Client); ok { m.client = c }
    // load config, build oauth client, build templates …
    return m.startServer() // starts http.Server in a goroutine
}
func (m *DashboardModule) OnUnload() error { m.stopServer(); return nil }
```

Build a multi-file module with the **package path** (not a single `main.go`):

```bash
go build -ldflags "-X main.Version=$(./scripts/version.sh)" -o bot ./cmd/bot/   # the dashboard is compiled into the single binary
```

### Exposing settings on the dashboard (`modules.WebConfigurable`)

The dashboard renders a per-module **Settings** panel automatically the moment a
module opts into the `WebConfigurable` interface — **zero dashboard code changes**
are needed to support a new module's settings. A module is the **single source of
truth** for what settings exist and exactly how each one renders: the dashboard
never introspects module internals, it just renders whatever `WebConfigSchema()`
returns and reads/writes through `WebGetConfig`/`WebSetConfig`.

This is **opt-in and additive** — modules that don't implement it are simply
unaffected (no panel shown).

**Where the integration lives (per module type):**

| Module type | Integration file | What it declares |
|-------------|------------------|------------------|
| Go | `modules/Go/<name>/dashboard.go` (any file in the package) | The three methods below, on the module type |
| Python | `modules/Python/<name>/dashboard.py` | `web_schema` list + `web_get_config(guild_id)` + `web_set_config(guild_id, key, value)` |
| Lua | `modules/Lua/<name>/<name>.dashboard.lua` | Global table `D` with `D.schema` + `D.get(guild_id)` + `D.set(guild_id, key, value)` |

**No integration file ⇒ no dashboard integration.** The Settings page shows
nothing for the module and the config API rejects writes. Lua/Python modules
don't need any Go code — the bot's wrappers implement `WebConfigurable`
themselves and forward reads/writes to your script.

#### The contract (3 methods)

```go
// Implemented on the same type that already implements modules.Module:

// 1. Declare the ordered field list — this is the entire UI definition.
func (m *MyModule) WebConfigSchema() []modules.ConfigField

// 2. Read current values. guildID == "" means the global/default scope.
//    All values are strings over the wire — the module parses ints/bools itself.
func (m *MyModule) WebGetConfig(guildID string) (map[string]string, error)

// 3. Write one field. guildID == "" means global. Validate + persist here.
//    Return an error to reject the value (shown to the user in the browser).
func (m *MyModule) WebSetConfig(guildID, key, value string) error
```

#### `ConfigField` reference

| Field | Type | Meaning |
|-------|------|---------|
| `Key` | `string` | Stable identifier passed to `WebGet`/`WebSet`. Internal — not shown. |
| `Label` | `string` | Human label shown in the panel (the row title). |
| `Help` | `string` | Optional one-line helper text under the field. Empty = none. |
| `Type` | `string` | Render type — one of `FieldType*` constants (table below). |
| `Scope` | `string` | `"global"` (one value for the whole bot) or `"guild"` (per-guild). |
| `GuildScoped` | `bool` | If `true`, staff who manage that guild may also edit it (not just owner/elevated). `channel`/`role` fields imply guild scope. |
| `Placeholder` | `string` | Input placeholder shown when empty (text/textarea/number/secret). |
| `Options` | `[]string` | Choices for `select` and `multi`. Ignored otherwise. |
| `Min`, `Max`, `Step` | `*float64` | Bounds for `number`/`range`. Use a `ptrF(v)` helper. |

Current values are returned by `WebGetConfig`, not stored in the schema — the
schema only describes the UI. So don't set `Value`; return values from `WebGetConfig`.

#### Render `Type`s (`modules.FieldType*`)

| Constant | Constant string | Renders as | Notes |
|----------|---------------|------------|-------|
| `FieldTypeToggle` | `toggle` | ON/OFF switch | Stored as `"true"`/`"false"`. |
| `FieldTypeText` | `text` | Single-line input | |
| `FieldTypeTextarea` | `textarea` | Multi-line input | Good for templates/messages. |
| `FieldTypeNumber` | `number` | Number input | Honor `Min`/`Max`/`Step`. |
| `FieldTypeRange` | `range` | Slider | `Min`/`Max`/`Step` required. |
| `FieldTypeSelect` | `select` | Dropdown | One of `Options`. |
| `FieldTypeMulti` | `multi` | Multi-select | Newline- or comma-joined (the web UI joins with `\n`; commas stay legal inside option values). |
| `FieldTypeSecret` | `secret` | Password input | **Redacted to `••••`** on read unless the caller is the bot owner. Never echoed back to non-owners. |
| `FieldTypeChannel` | `channel` | Channel picker | Implies guild scope; populated from cache. |
| `FieldTypeRole` | `role` | Role picker | Implies guild scope; populated from cache. |

#### Who can see/edit what (permission tiers)

Everything config-related is **hidden from regular users** at both the nav and
API layers. A logged-in user sees only the panels they can act on:

| Tier | Sees | Can edit |
|------|------|---------|
| **owner** / **elevated** | All module sections + raw/global scope | All `Scope:"global"` fields + every `GuildScoped` field in every guild |
| **staff** (manages ≥1 mutual guild) | Their managed guilds only | `GuildScoped:true` fields **for guilds they manage**. Cannot touch `Scope:"global"`. |
| **regular** (member of a mutual guild, manages none) | No config at all (403) | Nothing — the nav hides Settings/Modules/Permissions/Logs. |

So to let a guild's moderators tune your module for **their** server, mark the
relevant fields `Scope:"guild", GuildScoped: true`. To keep a setting
globally owner-controlled, leave it `Scope:"global"`, `GuildScoped: false`.

#### Complete worked example — `starboard` module

A realistic module that exposes several field types and persists them to a
per-guild YAML file. This mirrors how the built-in dashboard module configures
itself (dogfooding every field type).

```go
package main

import (
    "fmt"
    "os"
    "path/filepath"
    "strconv"
    "sync"

    "github.com/misfit/bot/modules"
    "gopkg.in/yaml.v3"
)

func ptrF(v float64) *float64 { return &v }

type starboardCfg struct {
    Enabled     bool   `yaml:"enabled"`
    ChannelID   string `yaml:"channel_id"`
    Threshold   int    `yaml:"threshold"`
    Emoji       string `yaml:"emoji"`
    Tone        string `yaml:"tone"` // "info" | "fancy"
    ApiKey      string `yaml:"api_key"`
}

type StarboardModule struct {
    ctx *modules.Context
    // guildID -> cfg ("" == global default)
    cfgs map[string]starboardCfg
    mu   sync.Mutex
}

// 1) The full UI for the dashboard Settings panel — declared here, rendered there:
func (m *StarboardModule) WebConfigSchema() []modules.ConfigField {
    return []modules.ConfigField{
        {Key: "enabled",     Label: "Enabled",      Type: modules.FieldTypeToggle,  Help: "Turn the starboard on", Scope: "guild", GuildScoped: true},
        {Key: "channel_id",  Label: "Starboard channel", Type: modules.FieldTypeChannel, Scope: "guild", GuildScoped: true},
        {Key: "threshold",   Label: "Reaction threshold", Type: modules.FieldTypeRange, Min: ptrF(1), Max: ptrF(50), Step: ptrF(1), Scope: "guild", GuildScoped: true},
        {Key: "emoji",       Label: "Reaction emoji", Type: modules.FieldTypeText, Placeholder: "⭐", Scope: "guild", GuildScoped: true},
        {Key: "tone",        Label: "Embed style", Type: modules.FieldTypeSelect, Options: []string{"info", "fancy"}, Scope: "guild", GuildScoped: true},
        {Key: "api_key",     Label: "External API key", Type: modules.FieldTypeSecret, Help: "Optional webhook signer", Scope: "global", GuildScoped: false},
    }
}

// 2) Read — return strings, one entry per field you want shown with a value.
func (m *StarboardModule) WebGetConfig(guildID string) (map[string]string, error) {
    m.mu.Lock()
    defer m.mu.Unlock()
    c := m.cfgs[guildID] // zero-value if absent
    return map[string]string{
        "enabled":    strconv.FormatBool(c.Enabled),
        "channel_id": c.ChannelID,
        "threshold":  strconv.Itoa(c.Threshold),
        "emoji":      c.Emoji,
        "tone":       c.Tone,
        // api_key intentionally omitted on read for non-owners: the dashboard
        // auto-redacts secret fields unless the caller is the owner, so you
        // can safely return it here. (The dashboard redacts secret fields
        // on read for non-owners; you don't need to pre-redact yourself.)
    }, nil
}

// 3) Write — validate, store, persist to disk.
func (m *StarboardModule) WebSetConfig(guildID, key, value string) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    c := m.cfgs[guildID]
    switch key {
    case "enabled":
        b, err := strconv.ParseBool(value); if err != nil { return fmt.Errorf("must be true/false") }
        c.Enabled = b
    case "channel_id":
        c.ChannelID = value // a raw channel snowflake string from the picker
    case "threshold":
        n, err := strconv.Atoi(value); if err != nil || n < 1 { return fmt.Errorf("must be ≥1") }
        c.Threshold = n
    case "emoji":
        c.Emoji = value
    case "tone":
        if value != "info" && value != "fancy" { return fmt.Errorf("unknown style") }
        c.Tone = value
    case "api_key":
        if value == "••••" { return nil } // owner re-submitted the redacted pseudo-value; ignore
        c.ApiKey = value
    default:
        return fmt.Errorf("unknown field %q", key)
    }
    m.cfgs[guildID] = c
    return m.persist(guildID) // write to <DataDir>/<guildID-or-global>.yml (the module's own folder)
}

func (m *StarboardModule) persist(guildID string) error {
    name := guildID; if name == "" { name = "global" }
    p := filepath.Join(m.ctx.DataDir, name+".yml")
    data, err := yaml.Marshal(m.cfgs[guildID]); if err != nil { return err }
    return os.WriteFile(p, data, 0600) // 0600 — configs may hold secrets
}
```

Rebuild and the dashboard's Settings page now shows a label, threshold slider,
emoji text box, style dropdown, channel picker, and a redacted API-key field for
the starboard module — with the owner/elevated able to edit all of them and a
guild's staff able to edit the `GuildScoped` ones for their guild.

```bash
go build -ldflags "-X main.Version=$(./scripts/version.sh)" -o bot ./cmd/bot/   # feature modules are compiled into the single binary
[p]load starboard
```

#### Practical notes

- **Values are strings over the wire.** Booleans arrive as `"true"`/`"false"`,
  numbers as `"3"` — parse them yourself in `WebSetConfig`. Same for `multi`:
  you get a newline- or comma-joined string of the selected `Options` (split
  on either; newlines keep commas legal inside option values).
- **`secret` fields are auto-redacted** to `••••` on read by the dashboard
  (see `redactedIfSet` in `main.go`). The `secret` `<input>` is rendered with
  **no `value` attribute**, and the frontend skips blank secret fields on save
  (`app.js`: `if (type === 'secret' && value === '') return;`), so the
  placeholder never reaches your `WebSetConfig`. Net effect: leaving a secret
  field empty leaves the stored value untouched; typing a new value replaces
  it. No backend `••••` guard is required.
- **`channel` / `role` values** are raw snowflake strings (no `<#>` wrapping).
  They imply guild scope — you'll receive them with a non-empty `guildID`.
- **Persists whenever you want.** You own your storage. The convention is
  your module's own folder (`ctx.DataDir` = `modules/Go/<name>/`,
  `modules/Python/<name>/`, `modules/Lua/<name>/`), file mode `0600` if it
  may hold secrets. The dashboard keeps only its cookie-signing
  `session_secret` and the `allowed_guilds` allowlist in
  `modules/Go/dashboard/config.yml` (`0600`); its OAuth **`client_secret`**
  lives in core `config.yml` under the shared `oauth:` section (see below).
- **Don't block.** `WebGetConfig`/`WebSetConfig` run on HTTP goroutines inside the
  bot process, so keep them fast. They're wrapped in the dashboard's panic-recovery
  middleware (a panic → 500 JSON) but a slow call still ties up the request.
- **Dogfooding reference.** The dashboard module implements `WebConfigurable`
  itself to configure its own OAuth `client_secret`, `client_id`, `listen`, and
  `public_url` — read `modules/Go/dashboard/config.go` and `main.go` (look for
  `WebConfigSchema`) for a real, shipping implementation of every field type.
  Note where each persists: `listen`/`public_url`/`client_secret` route to
  the **core `config.yml`** (`dashboard:` and `oauth:` sections, via
  `m.ctx.Bot.SetConfig("dashboard_…"/"oauth_client_secret", …)`); only
  `client_id`/`session_secret`/`allowed_guilds` stay in the 0600 module
  config. Use `effectiveListen()`/`effectiveClientSecret()` (core config →
  module config → default/fallback) as the pattern when a setting should be
  pinnable from the main config.

See `internal/dashboard/README.md` for the dashboard-specific setup flow
(OAuth, the `dashboard:` **and** `oauth:` sections of
the main `config.yml`) and the full RBAC table for who sees which config
sections.

#### Python example — `modules/Python/starboard_py/dashboard.py`

Same starboard settings, declared in Python. The runner imports this file
next to `main.py`; the field dict keys mirror `ConfigField` exactly. Raise an
exception from `web_set_config` to reject a value (shown in the browser).

```python
# modules/Python/starboard_py/dashboard.py
VALUES = {"enabled": "true", "threshold": "3", "tone": "info", "emoji": "⭐"}

web_schema = [
    {"key": "enabled", "label": "Enabled", "type": "toggle", "scope": "global"},
    {"key": "threshold", "label": "Reaction threshold", "type": "range",
     "min": 1, "max": 50, "step": 1, "scope": "guild", "guild_scoped": True},
    {"key": "emoji", "label": "Reaction emoji", "type": "text",
     "placeholder": "⭐", "scope": "guild", "guild_scoped": True},
    {"key": "tone", "label": "Embed style", "type": "select",
     "options": ["info", "fancy"], "scope": "guild", "guild_scoped": True},
    {"key": "api_key", "label": "External API key", "type": "secret", "scope": "global"},
]

def web_get_config(guild_id):
    return dict(VALUES)

def web_set_config(guild_id, key, value):
    if key == "tone" and value not in ("info", "fancy"):
        raise ValueError("unknown style")
    if key == "threshold":
        n = int(value)
        if n < 1:
            raise ValueError("threshold must be ≥ 1")
    VALUES[key] = value
    # persist wherever you like — e.g. a JSON/YAML file in the module dir
```

No Go code, no rebuild — dropping `dashboard.py` next to `main.py` and
reloading the module is all it takes. Remove it and the panel disappears.

#### Lua example — `modules/Lua/starboard/starboard.dashboard.lua`

```lua
-- modules/Lua/starboard/starboard.dashboard.lua  (next to starboard.lua)
D = {}
D.schema = {
  {key = "enabled",   label = "Enabled",  type = "toggle", scope = "global"},
  {key = "threshold", label = "Reaction threshold", type = "range",
   min = 1, max = 50, step = 1, scope = "guild", guild_scoped = true},
  {key = "emoji",     label = "Reaction emoji", type = "text",
   placeholder = "⭐", scope = "guild", guild_scoped = true},
  {key = "tone",      label = "Embed style", type = "select",
   options = {"info", "fancy"}, scope = "guild", guild_scoped = true},
}

local vals = {enabled = "true", threshold = "3", tone = "info", emoji = "⭐"}

D.get = function(guild_id)
  return vals
end

D.set = function(guild_id, key, value)
  if key == "tone" and value ~= "info" and value ~= "fancy" then
    return "unknown style"   -- non-nil return = error shown in the browser
  end
  vals[key] = value
  return nil
end
```

The script runs in its own Lua state; `ctx.log`/`ctx.log_error` and
`ctx.data_dir` (the module config dir, for persistence) are available.
`*.dashboard.lua` files are never treated as modules themselves (AutoLoad,
`[p]load all`, and the available-modules list all skip them).
