# Module Development Guide

A self-contained guide for writing **user modules** for this Discord bot. User
modules are **Python** (`modules/Python/<name>/main.py`) or **Lua**
(`modules/Lua/<name>/<name>.lua`). Go code is no longer a module-authoring
path — see [Compiled-in Go feature modules](#compiled-in-go-feature-modules).

## What Is a Module?

The bot has two kinds of add-ons, and only one of them is yours to write:

| Kind | Language | Where it lives | Managed with | Hot-loadable |
|------|----------|----------------|--------------|--------------|
| **Dynamic module** | Python or Lua | `modules/Python/<name>/`, `modules/Lua/<name>/` | `[p]load` / `[p]unload` / `[p]reload` | ✅ yes, no restart |
| **Compiled-in feature** | Go | the bot's own source, `internal/builtin/…` | `[p]modules enable\|disable` | ❌ restart to apply |

A dynamic module adds prefix commands, slash commands, event listeners,
background work of its own, or any combination — and is loaded, unloaded and
reloaded at runtime without restarting the bot. Python modules run in their own
subprocess (talking to the bot over JSON IPC, with a per-module virtualenv);
Lua modules run in an embedded gopher-lua state inside the bot.

Both kinds are first-class citizens of the same machinery: they declare
commands, register event hooks, and appear in `[p]help` the same way. What
differs is who runs them and how they are (re)loaded.

> **Crucial: every command shows up in `[p]help`.** Module commands are
> automatically listed (under a category named after the **module** — only its
> first character upper-cased, not per word) the moment the module loads.
> For that to look right, **every command must have a non-empty description** —
> it's the text shown after `—` (`?ping — Check how quickly the bot responds,
> with its gateway and API latency.`). A blank one renders as `?name —`.
> This applies to **Python and Lua** modules alike. See
> [How module commands appear in `[p]help`](#how-module-commands-appear-in-phelp).
> The dashboard can additionally render a module's settings panel with zero
> dashboard code if the module ships a dashboard integration script — see
> [Exposing settings on the dashboard](#exposing-settings-on-the-dashboard-moduleswebconfigurable).

## Prerequisites

**For a Python module:**

- Python 3 on the host, with the `venv` module available (`python3 -m venv`).
  The bot creates a per-module `.venv/` and installs `requirements.txt` into it
  on first load.
- Nothing else. No Go toolchain, no rebuild, no restart of the bot.

**For a Lua module:**

- Nothing extra. Lua is embedded in the bot binary (gopher-lua) — drop the
  `.lua` file in and load it.

**For the bot itself** (out of scope for module authors, mentioned once): it is
a Go program, built with `go build`. Go 1.26.4 is pinned in `go.mod`. You only
need that toolchain if you are changing the bot's own source — see
[Compiled-in Go feature modules](#compiled-in-go-feature-modules).

## Module Structure

**Python modules:**
```
modules/Python/
└── mymodule/
    ├── main.py          # Module entry point (must define a `module` global)
    ├── dashboard.py     # Dashboard integration (optional) — web_schema + web_get/set
    ├── requirements.txt # Optional dependencies (auto-installed to per-module .venv)
    └── .venv/           # Auto-created on first load (gitignored)
```

**Lua modules:**
```
modules/Lua/
└── mymodule/
    ├── mymodule.lua           # Module script (or main.lua)
    └── mymodule.dashboard.lua # Dashboard integration (optional) — table `D`
```

The module *folder* is the module: the Python directory (or the Lua file's
directory) is what `[p]load <name>` resolves, and it is also the module's data
directory — configs, saves and logs live next to the module script. See
[Module Configuration](#module-configuration).

Lua resolution order for `[p]load <name>`: `modules/Lua/<name>/<name>.lua`
first, then `modules/Lua/<name>/main.lua`. Python is
`modules/Python/<name>/` containing `main.py`. A name that matches nothing
fails with:

```
module 'mymodule' not found (tried Lua/<name>/<name>.lua, Python/<name>/main.py)
```

Only the quoted name is substituted — the `<name>` placeholders in the tail are
part of the literal message text.

**Canonical, runnable samples live in the repo:**
`examples/python/samplepy/main.py` and `examples/lua/sample.lua`. Copy one into
`modules/Python/<name>/` or `modules/Lua/<name>/` and edit.

### Dashboard integration script

**Dashboard integration is a separate file by convention.** The module's logic
lives in its own script(s); the dashboard settings panel lives in a dedicated
integration file — `dashboard.py` next to `main.py`, or `<name>.dashboard.lua`
next to `<name>.lua`. **No integration file ⇒ no dashboard integration:** the
Settings page shows no panel for the module and the config API refuses writes.
Everything is rendered purely from the schema the file declares — the dashboard
code never changes. See
[Dashboard integration script](#dashboard-integration-script) and
[Exposing settings on the dashboard](#exposing-settings-on-the-dashboard-moduleswebconfigurable).

`*.dashboard.lua` files are never treated as modules themselves: AutoLoad,
`[p]load all`, the available-modules listing and `[p]load <name>` all skip
them, and trying to load one directly is rejected with a clear error.

## Minimum Viable Module

### Python

```python
# modules/Python/hello_py/main.py
from misfit import Module, Command, SlashCommand


class HelloModule(Module):
    name = "hello_py"                  # REQUIRED — also the [p]help category name
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
                category="fun",   # single-command help + web catalog only; [p]help groups by module name
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
        return {}          # REQUIRED — return an empty dict if you handle no events

    def hello_command(self, ctx):
        ctx.respond("Hello!", "👋 Hello from Python!")


module = HelloModule()     # REQUIRED — the runner looks for this global
```

### Lua

```lua
-- modules/Lua/hello/hello.lua
M = {}                                      -- global, NOT `local M`
M.name = "hello"                            -- REQUIRED — also the [p]help category name
M.version = "1.0.0"
M.description = "A simple hello module (Lua)"
M.author = "YourName"

function M.on_load(M, name)                 -- dot syntax; the module table is arg 1
  ctx.log("Hello Lua module loaded! " .. tostring(name))
end

function M.on_unload()
end

function M.commands(M)
  return {
    {
      name = "hello",
      description = "Says hello from Lua",  -- REQUIRED — shown after `—` in [p]help
      usage = "hello [name]",
      category = "fun",
      execute = function(M)
        local who = ctx.args[1] or "world"  -- ctx is the global context table
        ctx.reply_text("Hello, " .. who .. "!")
      end,
    },
  }
end

function M.slash_commands(M)
  return {}
end
```

**Load:** `[p]load hello` / `[p]load hello_py` — then `?hello` and `/hello`
work immediately, and the commands appear under a **▸ Hello** section of
`[p]help`. No restart, no build.

## What Your Module Must Provide

### Python: the `Module` ABC

Subclass `misfit.Module`. Four class attributes describe the module; five
methods are **abstract** — all five must be implemented or instantiation raises
`TypeError`.

| Member | Kind | Meaning |
|--------|------|---------|
| `name` | class attr | Module name. Also the `[p]help` category and the load key. Falls back to the directory name if empty. |
| `version` | class attr | Free-form version string (default `"1.0.0"`). |
| `description` | class attr | One-line description of the module. |
| `author` | class attr | Author name. |
| `on_load(self, ctx)` | abstract | Called once after the process is up. `ctx` is a `BotContext`. Store it if you need it later. |
| `on_unload(self)` | abstract | Called on unload/shutdown. Clean up here. |
| `commands(self) -> list[Command]` | abstract | Prefix commands. Return `[]` for none. |
| `slash_commands(self) -> list[SlashCommand]` | abstract | Slash commands. Return `[]` for none. |
| `event_handlers(self) -> dict` | abstract | `{event_name: handler}`. Return `{}` for none. |

Finally, the module file must assign an instance to a module-level global:

```python
module = HelloModule()
```

The runner imports `main.py`, reads that global, and sends the bot a `ready`
message with the metadata + command lists before the module is considered
loaded (30s cap).

### Lua: the global `M` table

The script must define a **global** table `M`. `M.name` is mandatory — a
missing name fails the load (`M.name is required`). Everything else is
optional.

| Member | Called as | Meaning |
|--------|-----------|---------|
| `M.name` | read | Module name (required). Also the `[p]help` category and the load key. |
| `M.version`, `M.description`, `M.author` | read | Metadata strings. |
| `M.on_load(M, name)` | called | Runs after the script is evaluated. Register events here. |
| `M.on_unload()` | called | Runs on unload. |
| `M.commands(M)` | called, returns table | Array of command tables (see [Commands](#commands)). |
| `M.slash_commands(M)` | called, returns table | Array of slash command tables. Return `{}` if none. |

Two conventions matter:

- Use **dot syntax** — `function M.on_load(M, name)`. Colon syntax
  (`M:on_load`) shifts every parameter by one.
- Do **not** name a callback parameter `ctx`; that would shadow the global
  `ctx` context table.

## Commands

### Python prefix command

```python
Command(
    name="greet",                 # invoked as ?greet
    description="Greets a user",  # REQUIRED
    usage="greet <user>",
    category="fun",
    owner_only=False,             # True = bot owner + elevated only
    required_perm=0,              # see "Permissions" below
    aliases=["hi", "sayhi"],      # prefix-only alternative names
    execute=self.greet,
)
```

`execute` is required (a `Command` without it raises `ValueError` at
construction). It is called with a `Context` (see
[Responding](#responding)).

### Python slash command

```python
SlashCommand(
    name="greet",                 # registered with Discord as /greet
    description="Greets a user",  # REQUIRED; Discord caps this at 100 chars
    category="fun",
    owner_only=False,
    execute=self.greet,
)
```

`SlashCommand` has **no** `usage` and **no** `aliases` field. It also has **no
`options`** field: the current SDK does not expose typed slash options, so the
command is registered with Discord with no options at all and its args vector
is always **empty** — `ctx.args` is `[]` for a Python slash command. Put what
you need in the command's own wording (e.g. accept the argument as free text in
a follow-up flow), or use a prefix command for parameterised input.

### Lua command tables

A Lua command table is read for exactly four keys — `name`, `description`,
`usage`, `category` — plus the `execute` function:

```lua
{
  name = "greet",
  description = "Greets a user",  -- REQUIRED
  usage = "greet <user>",
  category = "fun",
  execute = function(M)
    -- read the invocation from the global ctx table
  end,
}
```

A Lua **slash** command table is read for `name`, `description`, `category`
and `execute` (no `usage`) — and, like the Python SDK, it declares no options,
so `ctx.args` is empty for a Lua slash command too.

Lua command tables have **no** permission fields at all: there is no
`owner_only`, no `required_perm`, no `aliases` equivalent. A Lua command is
usable by anyone who can invoke commands. Keep owner-only or moderation-grade
operations in Python (or in the bot's own Go code).

### How module commands appear in `[p]help`

Every module command is automatically grouped under a **category named after
the owning module**, with only its first character upper-cased, regardless of
the `Category` field you set on the command. So the `tickets` module's commands
appear under a **▸ Tickets** section and a module named `hello` under
**▸ Hello**. The user never sees your commands scattered across a shared
`modules` or `fun` bucket.


```text
📖 Bot Help
▸ Core
?help — List the commands you can use, grouped by category.
?shutdown — Shut the bot down completely. Owner and elevated users only.
?restart — Restart the bot so it comes back up with a fresh state.
?permissions — Grant or revoke elevated (owner-like) permissions for a user.
?ratelimit — Check a user's rate limit status or reset it so they can use commands again.
?update — Check for bot updates, apply them now, or show updater status.
▸ Cleanup
?cleanup — Delete messages in bulk — by count, user, text, or duplicates. Pick a subcommand.
▸ General
?ping — Check how quickly the bot responds, with its gateway and API latency.
?uptime — See how long the bot has been running.
?info — Show the bot's name, version, author, and where it runs.
▸ Modules
?modules — List modules, or enable/disable a compiled-in feature module (applies after restart).
?load — Load a module so its commands become available. Use `all` to load every module.
?unload — Unload a module so its commands stop working. Use `all` to unload every module.
?reload — Reload a module to pick up changes without restarting the bot. Use `all` for all.
▸ Tickets
?tickets — Ticket system configuration for this server
?close — Close this ticket (inside the ticket channel only)
?claim — Claim this ticket (helpers only)
?add — Add a member to this ticket
?remove — Remove a member from this ticket
```

Two rules produce that layout. First, **core** commands are grouped by their own
`Category` field (`"core"` when empty) — which is why `ping`/`uptime`/`info`
(`Category: "general"`) sit under **▸ General**, not under Core, while
`help`/`shutdown`/`restart`/`permissions`/`ratelimit`/`update` declare `core`
and `modules`/`load`/`unload`/`reload` declare `modules`. Second, each module's
commands are appended under a category named after the module, so cleanup's one
command and the tickets module's five all land in their own sections. Lines
inside a section keep registration order; the sections themselves are the
category names sorted alphabetically with `core` then moved to the front —
**Core, Cleanup, General, Modules, Tickets**, with any other loaded module
slotted into that alphabetical run by its own name. Only commands the caller
may use are listed: this is the owner's view. An ordinary user sees a much
smaller list — `help` itself is not owner-gated, so **▸ Core** survives showing
just `?help`, while `shutdown`/`restart`/`permissions`/`ratelimit`/`update` and
the whole **▸ Modules** section disappear. Finally, the label is `title()`d —
first character of the whole string upper-cased, not per word (`my_module` →
**▸ My_module**).

Rules:

- The category name comes from the module's name (`name` in Python, `M.name`
  in Lua) — `cleanup` → **▸ Cleanup**, `my_module` → **▸ My_module**.
- Commands are filtered by the same `canUse` check as core commands — a user
  who can't use a command doesn't see it.
- A module that exposes **no commands** doesn't get an empty section — it's
  skipped.
- The per-command `category` is **only** used by single-command help
  (`[p]help <name>` shows it as “Category: <value>”) and by the dashboard's web
  command catalog (which groups by module first, then category). It no longer
  affects how module commands are bucketed in `[p]help`.
- **Always set a description** — it's the text that appears after `—`. A blank
  one shows as `?name —`, which is what the user sees for poorly-written
  modules. Write it as a plain, user-friendly sentence. For commands that exist
  both as prefix and slash commands, the two descriptions are separate fields —
  set both.

### Permissions

The bot's three permission tiers still apply — a module command is subject to
exactly the same dispatch checks as a core command:

| Tier | Who | Access |
|------|-----|--------|
| Bot owner + elevated | Bot owner (config) + elevated users (`[p]permissions add`) | Everything — bypasses all checks |
| Guild owner | Owner of the Discord server | Bypasses `RequiredPerm`, NOT `OwnerOnly` |
| Normal users | Everyone else | Checked via Discord role permissions |

Priority:

```
If user is bot owner or elevated → always allowed
If OwnerOnly = true → only bot owner + elevated
If guild owner → allowed (bypasses RequiredPerm)
If RequiredPerm != 0 → user must have that Discord permission
Otherwise → everyone can use
```

What a module author can actually declare:

- **Python** — `owner_only` is honoured (the SDK field is forwarded to the
  bot and maps to `OwnerOnly`). `required_perm` is declared on the SDK
  dataclasses but is **not** transmitted by the current runner, so a
  `required_perm` you set has **no effect**: gate the command in your own code
  (or use `owner_only`) instead of relying on it.
- **Lua** — no permission fields exist; only the implicit checks apply.

There is no `SuperOwnerOnly` equivalent for modules (no SDK field at all), and
module commands cannot set a Discord permission requirement of their own.

Important behaviours:

- Denied users see the core "🚫 Permission Denied" error embed (red → that
  message auto-deletes after 7s; see [Auto-Delete](#auto-delete)). That embed
  comes from the dispatcher, not from your module.
- The **bot** must also have the corresponding Discord permission for any API
  call you make.

### Responding

**Python** — the command context exposes exactly two reply methods:

```python
ctx.respond("✅ Done", "Operation completed.")   # an embed
ctx.reply_text("Hello, world!")                  # plain message content
```

- `ctx.respond(title, description="")` posts a **blurple info embed**
  (`0x5865F2`). There is no success/error/warning variant in the SDK — the
  bridge always builds `embed.Info`.
- `ctx.reply_text(text)` posts plain content.
- Both are sent to `ctx.channel_id` through the bot's REST client, so they go
  out even though your code runs in a separate process. You can call them as
  often as you like during one invocation.
- For anything richer (fields, a different colour, multiple embeds), build the
  payload yourself and post it: `ctx.rest.create_message(channel_id, embed={...})`.

**Lua** — the same two, read off the global `ctx` table:

```lua
ctx.respond("✅ Done", "Operation completed.")  -- blurple embed
ctx.reply_text("Hello, world!")                 -- plain message content
```

Lua's `ctx.respond` also always builds a blurple embed. For a custom embed,
call the raw API: `ctx.api("POST", "/channels/" .. ctx.channel_id .. "/messages",
{embeds = {{title = "…", description = "…", color = 5763719}}})`.

### Auto-Delete

The rule is deliberately simple: **only error messages disappear; everything
else stays.**

| Response kind | Behavior |
|---------------|----------|
| Core-dispatched **error** — anything built with `embed.Error(…)` (red, `0xED4245`: permission denied, unknown command, rate-limited, command failed) | **Auto-deletes after 7 seconds** so the user can read it, then vanishes. |
| Core success / info / warning | **Stays permanently.** |
| Core usage / reference listings, help-style output | **Stays permanently** — they aren't red. |
| Plain text from a core command | **Stays permanently.** |
| **Anything a Python or Lua module posts** | **Stays permanently.** |

The dispatcher inspects the **first embed's colour** of every core-dispatched
response: red ⇒ schedule a delete after 7s; otherwise the message is left
alone. Module replies are posted directly through the bot's REST client by the
Python bridge / Lua bridge, so they never pass through that check — and since
both bridges always build a blurple embed or plain content, a module's response
is never deleted. There is no per-command "preserve" list and no opt-in hook:
if you don't want a response to vanish, just don't make it red. A module
*can't* make it red through `respond()` at all.

### Arguments

`ctx.args` (Python: a list; Lua: a **1-based** table) holds the arguments
after the command name.

For a prefix invocation, the content is tokenized with quote support:

```
[p]cleanup text "free nitro" 50
    ctx.args[0] = "text"          # Lua: ctx.args[1]
    ctx.args[1] = "free nitro"    # one arg, because it was quoted
    ctx.args[2] = "50"            # Lua: ctx.args[3]
```

For a slash invocation the bot builds the vector from the command's declared
options, in **declared order**: a flat subcommand contributes its own name
followed by its option values; a command in a subcommand group contributes the
group name, then the subcommand name, then its values. Optional options that
were not supplied are skipped entirely (so nothing shifts), and
channel/role/user options arrive as raw snowflake strings. This mapping only
matters for commands that *declare* options — the core and built-in commands
(such as `/cleanup`) do; commands declared by a **module** do not, so for those
`ctx.args` is always empty:

```
/cleanup messages count:100
    ctx.args[0] = "messages"    # the subcommand name
    ctx.args[1] = "100"

/cleanup user user:@someone count:10
    ctx.args[0] = "user"
    ctx.args[1] = "123456789012345678"   # the user option, as a snowflake
    ctx.args[2] = "10"
```

### Bot Info

**Python — `ctx` is a `BotContext` inside `on_load`, a `Context` inside a
command.** `BotContext` carries the bot facts:

| Member | Type | Meaning |
|--------|------|---------|
| `bot_name` | property → str | The bot's configured name. |
| `owner_id` | property → str | The bot owner's Discord user ID. |
| `prefix` | property → str | The command prefix. |
| `version` | property → str | The bot's version string. |
| `data_dir` | property → str | The module's own folder — see [Module Configuration](#module-configuration). |
| `logger` | `Logger` | `debug`/`info`/`warn`/`error` — see [Logger](#logger). |
| `rest` | `RestAPI` | Discord REST proxy — see [Discord Access](#discord-access). |
| `voice` | `VoiceContext` | Voice join/leave/play helpers. |
| `http_request(method, url, headers=None, body="", timeout=30.0)` | method | External HTTP through the bot (no CORS, no proxy needed). Returns `{"status": int, "body": str}`. |

Keep the `BotContext` you receive in `on_load` (e.g. `self.ctx = ctx`) if a
command or event handler needs it later; command handlers receive a `Context`
with only `channel_id`, `guild_id`, `author_id`, `args`, `is_slash`, `req_id`.

**Lua — the global `ctx` table** carries both bot facts and the per-invocation
fields:

| Function | Returns | Meaning |
|----------|---------|---------|
| `ctx.get_prefix()` | string | Command prefix. |
| `ctx.get_name()` | string | Bot name. |
| `ctx.get_version()` | string | Bot version. |
| `ctx.get_owner_id()` | string | Owner's Discord user ID. |
| `ctx.get_self_user_id()` | string | The bot's own user ID. |
| `ctx.is_owner(id)` | boolean | Is this user the bot owner? |
| `ctx.is_elevated(id)` | boolean | Is this user elevated? |
| `ctx.get_latency()` | string | Gateway latency (`"45ms"`). |
| `ctx.get_config_dir()` | string | The bot's working directory. |
| `ctx.sleep(ms)` | — | Sleep inside a callback (milliseconds). |

Per-invocation fields, refreshed before every command runs: `ctx.channel_id`,
`ctx.guild_id`, `ctx.author_id` (all strings), `ctx.is_slash` (boolean) and
`ctx.args` (1-based table).

### Module Management from Commands

Module commands cannot load or unload other modules — that's an owner
operation exposed through the bot's own `[p]load` / `[p]unload` / `[p]reload`
commands (`all` is accepted by each):

```
[p]load <name> | load all
[p]unload <name> | unload all
[p]reload <name> | reload all
```

`reload` is unload-then-load. If the reload's load step fails, the module is
**lost until the bot restarts** (the bot logs a warning — the previous instance
cannot be rolled back).

For a compiled-in feature module, `[p]modules` lists what is loaded and
`[p]modules enable <name>` / `[p]modules disable <name>` writes the
`enabled_modules` config entry. **It applies after the next restart.**

## Discord Access

Neither Python nor Lua modules get the bot's in-process cache. They talk to
Discord through a proxy that holds the token, which means one extra IPC hop but
no way to leak credentials into your script.

**Python — `ctx.rest` (`RestAPI`)** covers the common operations directly:
`request(method, endpoint, json=…)` for anything else, plus
`create_message`, `edit_message`, `delete_message`, `get_message`,
`get_channel_messages`, `add_reaction`, `remove_reaction`, `pin_message`,
`unpin_message`, `get_channel`, `create_channel`, `delete_channel`,
`get_member`, `kick_member`, `ban_member`, `unban_member`, `timeout_member`,
`add_member_role`, `remove_member_role`, `get_guild`, `get_guild_channels`,
`get_guild_roles`, `get_guild_bans`, `get_guild_members`, `get_user`,
`create_role`, `delete_role`, `get_guild_emojis`, `create_webhook`.
`request()` returns the parsed response body or `None`.

```python
ctx.rest.create_message(channel_id, "Done.")
ctx.rest.ban_member(guild_id, user_id, reason="spam")
members = ctx.rest.get_guild_members(guild_id, limit=100)
```

**Lua — `ctx.api(method, endpoint[, body_table])`** returns the response body
as a **JSON string**, or `nil, error_message` on failure;
`ctx.http(method, url[, headers[, body]])` reaches arbitrary external APIs and
returns a `{status, body}` table, or `nil, error_message` on failure. Common
calls have thin wrappers: `ctx.delete_message(channel_id, message_id)`,
`ctx.ban(guild_id, user_id[, reason])`, `ctx.kick(guild_id, user_id)`,
`ctx.add_role(guild_id, user_id, role_id)`,
`ctx.remove_role(guild_id, user_id, role_id)` — each returns a single JSON
result string (on failure that string is the error message, so check the
status you expect). The getters — `ctx.get_message`, `ctx.get_channel`,
`ctx.get_guild`, `ctx.get_member` — return a JSON string, or `nil, error`.

```lua
ctx.delete_message(ctx.channel_id, msg_id)
local body = ctx.api("GET", "/guilds/" .. ctx.guild_id .. "/members?limit=100")
local res  = ctx.http("GET", "https://example.com/status")
```

**Cache vs REST:** the bot's own core keeps a guild/member/role cache
(`FlagMembers` + `FlagRoles`) and uses it for its permission checks. Modules do
not see that cache — resolve what you need over REST (`ctx.rest` in Python,
`ctx.api` in Lua) and treat it as network I/O: keep what you read often in your
own module state rather than re-fetching on every event.

## Module Configuration

`ctx.data_dir` (Python) / `ctx.data_dir` inside the dashboard script (Lua) is
the module's own folder:
`modules/Python/<name>/`, `modules/Lua/<name>/`.

**It is not created for you** — call your language's mkdir before writing:

```python
# modules/Python/mymodule/main.py
import os

def on_load(self, ctx):
    self.dir = ctx.data_dir
    os.makedirs(self.dir, exist_ok=True)
    self.cfg_path = os.path.join(self.dir, "config.json")
    if os.path.exists(self.cfg_path):
        with open(self.cfg_path) as f:
            self.cfg = json.load(f)
```

```lua
-- Lua has no ctx.data_dir in module state (only a dashboard script gets one),
-- so build the folder path from the bot's working dir. The standard Lua `io`
-- library is available, and the module's own folder already exists (it holds
-- the script).
local dir = ctx.get_config_dir() .. "/modules/Lua/mymodule"

local function save_channel(id)          -- one key, one line — no JSON needed
  local f = assert(io.open(dir .. "/channel.txt", "w"))
  f:write(id)
  f:close()
end

local function load_channel()
  local f = io.open(dir .. "/channel.txt", "r")
  if not f then return "" end
  local v = f:read("*a")
  f:close()
  return v
end
```

Note the asymmetry: a Lua **module** gets no `ctx.data_dir` — only a
**dashboard** script is handed one (its own folder, injected by the loader). A
plain Lua module therefore builds its folder path from `ctx.get_config_dir()`
(the bot's working directory) plus `modules/Lua/<name>/`, and writing state
means doing the file I/O yourself. If you need real state management, a Python
module is the easier fit.

Notes:

- The module folder is the right home for state. The repo's `.gitignore` already
  ignores runtime data there (`modules/<lang>/*/config*.yml`, `config.json`,
  `data/`, `logs/`) while tracking the source.
- Use file mode `0600` for anything that may hold secrets.
- Python dependencies: list them in `requirements.txt` next to `main.py`. The
  bot creates `modules/Python/<name>/.venv/` and runs `pip install -r` whenever
  the file's hash changes. Never vendor or assume a global install.

## Logger

**Python:**

```python
ctx.logger.info("Module loaded")
ctx.logger.debug("payload: %s" % data)
ctx.logger.warn("config missing, using defaults")
ctx.logger.error("failed to reach API: %s" % err)
```

Messages are routed to the bot's logger: JSON lines on stdout **and** to the
day-rotated file `logs/bot-YYYY-MM-DD.log` (30-day retention — `logger.New` +
`DailyRotatingWriter`). `logs/bot.log` itself is not written any more; it
survives only as the default `logging.file_path` string that the readers
(`internal/logutil.ResolvePath`) use as a basename when picking the newest
`bot-*.log`. `ctx.logger.*` calls arrive with a `Python: ` prefix
(`python_bridge.go`, `handleLog`), and an exception escaping a command or event
handler is logged as `Python error: …` (`handleError`) plus the traceback on
the subprocess's stderr — the caller sees nothing (see [Gotchas](#gotchas)).

**Lua:**

```lua
ctx.log("module loaded")          -- info
ctx.log_debug("payload")
ctx.log_warn("config missing")
ctx.log_error("failed: " .. tostring(err))
```

## Events

Event names are **identical across Python and Lua** — the bot maps each name to
the corresponding gateway event and hands your handler a data payload. Both
languages support these 17 names:

```
message_create            message_update            message_delete
guild_message_create      guild_message_update      guild_message_delete
guild_member_join         guild_member_leave
guild_ban                 guild_unban
guild_join                guild_leave
voice_state_update
message_reaction_add      message_reaction_remove
component_interaction     modal_submit
```

Python additionally supports **`presence_update`** (member status changes).
Lua does not — `presence_update` is not one of the names the Lua loader
handles, so registering `ctx.on_event("presence_update", …)` in Lua never fires
(the callback is simply never wired up).

Every handler is dispatched with panic/traceback recovery, so a crashing
handler won't take down the bot (or the other modules).

### Python: `event_handlers()`

Return a dict of event name → handler. Only the names you declare are
subscribed:

```python
def event_handlers(self):
    return {
        "guild_message_create": self.on_message,
        "guild_member_join":    self.on_join,
        "voice_state_update":   self.on_voice,
        "component_interaction": self.on_button,
    }

def on_message(self, data):
    if data["author"]["bot"]:
        return
    print(data["content"])
```

Handlers receive a plain `dict`. They are called **without** a command
context — if you need bot facts, keep the `BotContext` from `on_load`.

### Lua: `ctx.on_event()`

Register inside `on_load`; callbacks are wired after `on_load` returns:

```lua
function M.on_load(M, name)
  ctx.on_event("guild_message_create", function(data)
    -- data.guild_id, data.channel_id, data.content
  end)
  ctx.on_event("voice_state_update", function(data)
    -- data.guild_id, data.user_id, data.channel_id (nil when disconnected)
  end)
  ctx.on_event("component_interaction", function(data)
    -- data.custom_id, data.channel_id, data.user_id
  end)
end
```

Callbacks receive a Lua table. The Lua state is mutex-guarded — only one Lua
callback (or command) runs at a time, so a slow callback blocks every other Lua
command and event. Do slow work in the background of your own process instead
if you can.

### Event payloads

Payload fields per event. A `user`/`author` object is Python
`{id, username, bot[, global_name]}`; Lua flattens it to `user_id` +
`username` where noted.

| Event | Python fields | Lua fields |
|-------|---------------|------------|
| `message_create` | `message_id, channel_id, author, content, attachments, timestamp` | `message_id, channel_id, content` (+ `guild_id` when in a guild) |
| `message_update` | `message_id, channel_id, content`[, `author`][, `attachments`] | as Python, but `author` is `{id}` only |
| `message_delete` | `channel_id, message_id` | same as Python |
| `guild_message_create` | `message_id, guild_id, channel_id, author, content, attachments, timestamp` | `message_id, guild_id, channel_id, content` |
| `guild_message_update` | `message_id, guild_id, channel_id, content`[, `author`][, `attachments`] | as Python, but `author` is `{id}` only |
| `guild_message_delete` | `guild_id, channel_id, message_id` | same as Python |
| `guild_member_join` | `guild_id, user, roles` (list of role ID strings) | `guild_id, user_id, username, roles` |
| `guild_member_leave` | `guild_id, user` (`{id, username}`) | `guild_id, user_id` |
| `guild_ban` / `guild_unban` | `guild_id, user` | `guild_id, user_id` |
| `guild_join` (bot joins) | `guild_id, guild_name, owner_id, member_count` | `guild_id, name` |
| `guild_leave` (bot leaves) | `guild_id, guild_name` | `guild_id, name` |
| `presence_update` | `user_id, guild_id, status`[, `activities` = `[{name, type}]`] | **not supported** |
| `voice_state_update` | `guild_id, user_id, session_id`[, `channel_id`][, `member` = `{user_id, username, bot}`] | `guild_id, user_id`[, `channel_id`] |
| `message_reaction_add` / `_remove` | `channel_id, message_id, user_id, emoji` = `{name[, id][, animated]}`[, `guild_id`] | same as Python |
| `component_interaction` | `custom_id, channel_id, user_id`[, `guild_id`] | same as Python |
| `modal_submit` | `custom_id, channel_id, user_id`[, `guild_id`], `components` = `[{custom_id, value}]` | `custom_id, channel_id, user_id`[, `guild_id`] — **no `components`** |

`attachments` entries carry `id`, `url`, `proxy_url` (and more — inspect one to
see).

### Component Interactions (buttons & select menus)

Components arrive as `component_interaction` events:

| Field | Type | Description |
|-------|------|-------------|
| `custom_id` | string | The button/select custom ID you set |
| `channel_id` | string | Channel the interaction happened in |
| `user_id` | string | User who clicked |
| `guild_id` | string *(optional)* | Guild ID if in a guild |

**The bot acknowledges every component interaction for you** — it calls
`DeferUpdateMessage()` before dispatching, *unless* a loaded module claims the
interaction outright (the tickets module's Open button does this, because
Discord rejects a modal after an acknowledgement). For an ordinary module
handler the interaction is already deferred, so you cannot use the interaction
token to reply; respond by creating a message instead —
`ctx.rest.create_message(...)` in Python, `ctx.api("POST", …)` in Lua — or by
editing the original message.

### Modal Submits

Modal submissions arrive as `modal_submit` events with the same four fields as
components (`custom_id`, `channel_id`, `user_id`, optional `guild_id`).

**Python** additionally receives `components`: the submitted values as
`[{"custom_id": …, "value": …}]`.

```python
def on_modal(self, data):
    for comp in data.get("components", []):
        if comp["custom_id"] == "feedback_text":
            self.ctx.logger.info("Feedback: %s" % comp["value"])
```

**Lua does not receive `components`** — the modal's values are not forwarded to
Lua handlers, only the identifying fields. If you need submitted values, handle
the modal in a Python module.

## Welcome Module Example

A complete, realistic Python module: a config file in its own folder, a command
to set the welcome channel, and a `guild_member_join` handler that greets new
members.

```python
# modules/Python/welcome/main.py
import json
import os

from misfit import Command, Module, SlashCommand


class WelcomeModule(Module):
    name = "welcome"
    version = "1.0.0"
    description = "Greets new members in a configured channel"
    author = "YourName"

    def on_load(self, ctx):
        self.ctx = ctx
        os.makedirs(ctx.data_dir, exist_ok=True)
        self.cfg_path = os.path.join(ctx.data_dir, "config.json")
        self.cfg = {"channel_id": "", "message": "Welcome, {mention}!"}
        if os.path.exists(self.cfg_path):
            with open(self.cfg_path) as f:
                self.cfg.update(json.load(f))
        ctx.logger.info("welcome module ready")

    def on_unload(self):
        pass

    def _save(self):
        with open(self.cfg_path, "w") as f:
            json.dump(self.cfg, f)

    def commands(self):
        return [
            Command(
                name="welcome",
                description="Configure the welcome message channel",
                usage="welcome [#channel] [message]",
                owner_only=True,
                execute=self.cmd_welcome,
            ),
        ]

    def slash_commands(self):
        return [
            SlashCommand(
                name="welcome",
                description="Configure the welcome message channel",
                owner_only=True,
                execute=self.cmd_welcome,
            ),
        ]

    def event_handlers(self):
        return {"guild_member_join": self.on_join}

    def cmd_welcome(self, ctx):
        args = ctx.args
        if args:
            # Channel args are raw snowflake strings on slash; mentions on prefix.
            cid = args[0].strip("<#>")
            if cid.isdigit():
                self.cfg["channel_id"] = cid
        if len(args) > 1:
            self.cfg["message"] = " ".join(args[1:])
        self._save()
        ctx.respond("Welcome", "channel=%s message=%s"
                    % (self.cfg["channel_id"] or "(unset)", self.cfg["message"]))

    def on_join(self, data):
        channel = self.cfg["channel_id"]
        if not channel:
            return
        text = self.cfg["message"].format(
            mention="<@%s>" % data["user"]["id"],
            name=data["user"]["username"],
        )
        self.ctx.rest.create_message(channel, text)


module = WelcomeModule()
```

The Lua shape of the same idea (in-memory config, event callback, one command):

```lua
-- modules/Lua/welcome/welcome.lua
M = {}
M.name = "welcome"
M.version = "1.0.0"
M.description = "Greets new members"
M.author = "YourName"

local channel_id = ""

function M.on_load(M, name)
  ctx.on_event("guild_member_join", function(data)
    if channel_id == "" then return end
    ctx.api("POST", "/channels/" .. channel_id .. "/messages",
      {content = "Welcome, <@" .. data.user_id .. ">!"})
  end)
end

function M.on_unload()
end

function M.commands(M)
  return {
    {
      name = "welcome",
      description = "Set the welcome channel",
      usage = "welcome <channel_id>",
      execute = function(M)
        channel_id = ctx.args[1] or ""
        ctx.reply_text("Welcome channel set to " .. (channel_id ~= "" and channel_id or "(unset)"))
      end,
    },
  }
end

function M.slash_commands(M)
  return {}
end
```

## Building & Loading

There is nothing to build. A Python or Lua module is loaded from source.

```text
# Load (resolves Lua/<name>/<name>.lua, Lua/<name>/main.lua, Python/<name>/main.py)
[p]load <name>

# Unload
[p]unload <name>

# Reload (unload + load; if the load step fails the module is lost until restart)
[p]reload <name>

# All at once
[p]load all
[p]unload all
[p]reload all
```

Loading and unloading re-registers slash commands with Discord automatically —
module slash commands appear in the command picker without a bot restart.

`[p]load all` walks every Lua/Python module it can find;
`[p]reload all` walks the currently loaded set. Note that `[p]unload all` is
literal — it also drops the compiled-in builtins for the rest of the process
run (they come back on restart).

On startup the bot reads `loaded_modules.json` and loads exactly those modules
(that's also how `[p]reload all` picks its targets). If that file has no
entries and `modules.auto_load` is true, the bot scans `modules/Lua/` and
`modules/Python/` and loads everything it finds, then writes the file. A name
recorded in `loaded_modules.json` whose file no longer exists is skipped with a
warning (`Previously loaded module <name> not found, skipping`) — it does not
block startup. `--no-modules` skips module loading entirely.

## Compiled-in Go feature modules

The bot's own feature modules — `cleanup` and `tickets` (Discord commands) and
`imagefilter` (dashboard-only) — are **compiled into the binary**. They are not
user-loadable, they cannot be added without changing the bot's source and
building it, and there is no `.so` plugin path at all: the module manager
rejects anything that isn't Lua or Python with

```
unsupported module type for <path> — only Lua and Python modules are loadable;
feature modules (cleanup/tickets) are compiled-in and managed with
[p]modules enable|disable
```

**If you want to add Go code to this bot, this is the path:**

1. Write the module under `internal/builtin/<name>/` implementing the
   `modules.Module` interface (name/version/description/author, `OnLoad`,
   `OnUnload`, `Commands`, `SlashCommands`, `Dependencies`) plus an exported
   `New() modules.Module`.
2. Register the constructor in `cmd/bot/main.go`'s
   `ModMgr.RegisterBuiltinsWithFilter(...)` call next to
   `cleanup.New, tickets.New, imagefilter.New`.
3. Build and restart. `[p]modules enable|disable <name>` then gates it through
   the `enabled_modules` config map (a missing key means enabled).

Two useful references in-tree:

- `internal/builtin/cleanup/` — the smallest builtin (commands only, no state).
- `internal/builtin/tickets/` — a large builtin: commands, event hooks, its own
  storage, and a `WebConfigurable` settings surface
  (`internal/builtin/tickets/webconfig.go`) you can copy for a new dashboard
  panel.

A builtin's data directory is `modules/<name>/` (its `OnLoad` gets `DataDir` =
the configured modules path + the module name). `imagefilter` is registered the
same way but is not accepted by `[p]modules enable|disable`, which only knows
`cleanup` and `tickets`. The dashboard — an always-on subsystem rather than a
builtin — is pinned to the historical `modules/Go/dashboard/` path so its
module-local state survives.

Background services (an HTTP server, a poll loop, a long-running worker) belong
in this compiled-in layer, not in a dynamic module: the dashboard and the MCP
endpoint are in-process subsystems constructed in `cmd/bot/main.go`, and
because a panic there would take the whole bot down, they wrap their handlers
in panic-recovery middleware and run their I/O off the gateway goroutines. For
the bot's internals, read `CLAUDE.md`.

## Gotchas

1. **Python: implement all five abstract methods** — `on_load`, `on_unload`,
   `commands`, `slash_commands`, `event_handlers`. A missing one raises
   `TypeError` at instantiation and the module fails to load.
2. **Python: assign the `module` global** — the runner refuses a `main.py` with
   no `module` instance (the error is "main.py must define a `module` global
   variable (instance of Module)").
3. **Lua: the module table must be global `M`** — `local M = {}` is invisible
   to the loader, and `M.name` is mandatory.
4. **Lua: dot syntax, never colon syntax** — `function M.on_load(M, name)`, not
   `M:on_load`. Colon syntax shifts every parameter by one. Never name a
   callback parameter `ctx` — it shadows the global context table.
5. **Module names** — lowercase, no spaces. The name is the `[p]load` key, the
   `[p]help` category, and (in Python) the fallback to the directory name.
6. **Descriptions are mandatory in practice** — a blank description renders as
   `?name —` in `[p]help`. Set both the prefix and the slash description when a
   command exists as both.
7. **`ctx.args[0]` is the first argument after the command name**, not the
   command itself. In Lua the args table is 1-based, so it's `ctx.args[1]`.
8. **Slash args are declared-order positional values** — the subcommand (and
   group) name comes first, absent optional options are skipped, and
   channel/role/user options arrive as snowflake strings, not mentions.
9. **Python `required_perm` is inert** — declared on the SDK dataclasses but not
   transmitted by the runner. Use `owner_only`, or check permissions yourself.
10. **Lua commands have no permission fields** — no `owner_only`, no
    `required_perm`, no aliases. Move anything privileged to Python.
11. **Python responses are always blurple info embeds** — `ctx.respond(title,
    description)` gives you one embed with one colour. Post via `ctx.rest` for
    anything richer.
12. **Module responses never auto-delete** — the 7s delete applies only to
    core-dispatched red `embed.Error` embeds, and module replies don't go
    through that path. See [Auto-Delete](#auto-delete).
13. **`ctx.data_dir` is NOT auto-created** — `os.makedirs(ctx.data_dir,
    exist_ok=True)` before writing. The Python `.venv/` is created for you, the
    module folder is not.
14. **Python dependencies go in `requirements.txt`** — they are installed into
    the module's own `.venv/` when the file's hash changes, not into a global
    Python.
15. **Keep the `BotContext` from `on_load`** — command handlers get a
    command-scoped `Context` (channel/guild/author/args), not the `BotContext`
    with `rest`/`logger`/`data_dir`.
16. **Register events where the language expects it** — Python in
    `event_handlers()` (returned dict), Lua via `ctx.on_event()` **inside
    `on_load`**. Nothing registered later is wired up.
17. **Lua has no `presence_update`** — the registration is stored but never
    wired to a hook, so the callback never fires; use Python if you need it.
18. **Lua modal handlers get no `components`** — only `custom_id`,
    `channel_id`, `user_id`, and optionally `guild_id`. Python gets the
    submitted values.
19. **Lua is single-threaded** — one Lua callback or command at a time. A slow
    handler blocks every other Lua command and event for that module.
20. **Component interactions are auto-acknowledged** — you cannot reply through
    the interaction; create or edit a message instead.
21. **Exceptions are logged, not shown to the user** — a Python command that
    raises produces a `Python error: …` log line (and a traceback on the
    subprocess's stderr) but no Discord response. Wrap your own error handling
    and reply with a message.
22. **Mention parsing** — user input may be `<@ID>`, `<@!ID>` or a bare ID; strip
    the wrapper before using an ID.
23. **Module persistence** — `loaded_modules.json` is authoritative on startup.
    With no saved entries, `auto_load` scans `modules/Lua/` and
    `modules/Python/` and persists what it finds. `--no-modules` skips all of
    it.
24. **`reload` cannot roll back** — it unloads first, then loads; a failing load
    leaves the module unloaded until the bot restarts.
25. **Compiled-in features need a restart** — `[p]modules enable|disable`
    writes config only; the reply says so.
26. **DMs** — in DMs the bot resolves zero guild permissions and no guild
    owner, so permission-gated commands only work for the owner/elevated there.

---

## Exposing settings on the dashboard (`modules.WebConfigurable`)

The dashboard renders a per-module **Settings** panel automatically the moment a
module ships a dashboard integration file — **zero dashboard code changes** are
needed to support a new module's settings. A module is the **single source of
truth** for what settings exist and exactly how each one renders: the dashboard
never introspects module internals, it just renders whatever schema your file
declares and reads/writes through your get/set functions.

This is **opt-in and additive** — modules without an integration file are
simply unaffected (no panel shown).

### Dashboard integration script

**Where the integration lives, per module type:**

| Module type | Integration file | What it declares |
|-------------|------------------|------------------|
| Python | `modules/Python/<name>/dashboard.py` | `web_schema` list + `web_get_config(guild_id)` + `web_set_config(guild_id, key, value)` |
| Lua | `modules/Lua/<name>/<name>.dashboard.lua` (next to `<name>.lua`) | Global table `D` with `D.schema` + `D.get(guild_id)` + `D.set(guild_id, key, value)` |
| Compiled-in Go | e.g. `internal/builtin/tickets/webconfig.go` | The `WebConfigurable` methods on the module type |

**No integration file ⇒ no dashboard integration.** The Settings page shows
nothing for the module and the config API refuses writes. Python and Lua
modules need no Go code — the bot's wrappers implement `WebConfigurable`
themselves (`modules/python_module.go`, `modules/lua_webconfig.go`) and forward
reads/writes to your script over IPC.

- The Python file is imported by the runner next to `main.py`; it must define
  `web_schema`, `web_get_config` and `web_set_config`. A malformed schema
  (entry without `key` or `type`, `scope`/`guild_scoped` disagreement) fails
  the module load so you notice immediately.
- The Lua script runs in its **own** Lua state (separate from the module's
  command/event state) and gets the standard `ctx` table plus `ctx.data_dir`
  for persistence. It must define a global table `D` with `D.schema`, `D.get`
  and `D.set`. A broken script fails the module load.

### The schema vocabulary (`modules.ConfigField`)

Every integration file mirrors the same field shape, whatever the language:

| Field | Type | Meaning |
|-------|------|---------|
| `Key` / `key` | string | Stable identifier passed to get/set. Internal — not shown. |
| `Label` / `label` | string | Human label shown in the panel (the row title). |
| `Help` / `help` | string | Optional one-line helper text under the field. Empty = none. |
| `Type` / `type` | string | Render type — one of the constants in the next table. |
| `Scope` / `scope` | string | `"global"` (one value for the whole bot) or `"guild"` (per-guild). |
| `GuildScoped` / `guild_scoped` | bool | If true, staff who manage that guild may also edit it (not just owner/elevated). `channel`/`role`/`user` fields imply guild scope. |
| `Placeholder` / `placeholder` | string | Input placeholder shown when empty (text/textarea/number/secret). |
| `Options` / `options` | list | Choices for `select` and `multi`. Ignored otherwise. |
| `Min`, `Max`, `Step` / `min`, `max`, `step` | number | Bounds for `number`/`range`. |

Current values come from your get function, not from the schema — the schema
only describes the UI.

### Render types (`modules.FieldType*`)

| Constant | Constant string | Renders as | Notes |
|----------|---------------|------------|-------|
| `FieldTypeToggle` | `toggle` | ON/OFF switch | Stored as `"true"`/`"false"`. |
| `FieldTypeText` | `text` | Single-line input | |
| `FieldTypeTextarea` | `textarea` | Multi-line input | Good for templates/messages. |
| `FieldTypeNumber` | `number` | Number input | Honour `Min`/`Max`/`Step`. |
| `FieldTypeRange` | `range` | Slider | `Min`/`Max`/`Step` required. |
| `FieldTypeSelect` | `select` | Dropdown | One of `Options`. |
| `FieldTypeMulti` | `multi` | Multi-select | Value is newline-joined by the UI (so commas stay legal inside option values). |
| `FieldTypeSecret` | `secret` | Password input | Redacted to `••••••••` on read unless the caller is the bot owner. Never echoed back to non-owners. |
| `FieldTypeChannel` | `channel` | Channel picker | Implies guild scope; populated from cache. |
| `FieldTypeRole` | `role` | Role picker | Implies guild scope; populated from cache. |
| `FieldTypeUser` | `user` | Member picker | Implies guild scope; populated from cache (capped). |

### Who can see/edit what (permission tiers)

Everything config-related is **hidden from regular users** at both the nav and
API layers. The dashboard resolves one of four tiers per request —
`owner > elevated > staff > regular`:

| Tier | Sees | Can edit |
|------|------|---------|
| **owner** / **elevated** | All module sections + global scope | All `global` fields + every guild-scoped field in every guild |
| **staff** (manages ≥1 mutual guild) | Their managed guilds only | Guild-scoped fields **for guilds they manage**. Cannot touch global scope. |
| **regular** (member of a mutual guild, manages none) | No config at all (403) | Nothing — the nav hides Settings/Modules/Permissions/Logs. |

So to let a guild's moderators tune your module for **their** server, mark the
relevant fields `scope: guild, guild_scoped: true`. To keep a setting globally
owner-controlled, leave it `scope: global, guild_scoped: false`.

Writes are authorized against the field's declared scope: a guild-scoped field
demands a non-empty guild id and that the caller manages that guild; a global
field demands an empty guild id and an owner/elevated caller.

### Worked example — Python

```python
# modules/Python/starboard_py/dashboard.py   (next to main.py)

web_schema = [
    {"key": "enabled",    "label": "Enabled", "type": "toggle",
     "scope": "guild", "guild_scoped": True, "help": "Turn the starboard on"},
    {"key": "channel_id", "label": "Starboard channel", "type": "channel",
     "scope": "guild", "guild_scoped": True},
    {"key": "threshold",  "label": "Reaction threshold", "type": "range",
     "min": 1, "max": 50, "step": 1, "scope": "guild", "guild_scoped": True},
    {"key": "emoji",      "label": "Reaction emoji", "type": "text",
     "placeholder": "⭐", "scope": "guild", "guild_scoped": True},
    {"key": "tone",       "label": "Embed style", "type": "select",
     "options": ["info", "fancy"], "scope": "guild", "guild_scoped": True},
    {"key": "api_key",    "label": "External API key", "type": "secret",
     "help": "Optional webhook signer", "scope": "global"},   # guild_scoped defaults to False
]

VALUES = {"enabled": "false", "threshold": "3", "tone": "info", "emoji": "⭐",
          "channel_id": "", "api_key": ""}


def web_get_config(guild_id):
    # Return whatever applies for this scope; the dashboard filters and
    # redacts according to the schema, so returning everything is fine.
    return dict(VALUES)


def web_set_config(guild_id, key, value):
    if key == "tone" and value not in ("info", "fancy"):
        raise ValueError("unknown style")          # → shown in the browser
    if key == "threshold":
        n = int(value)
        if n < 1:
            raise ValueError("threshold must be ≥ 1")
        value = str(n)
    if key not in VALUES:
        raise ValueError("unknown field %r" % key)
    VALUES[key] = value
    # persist wherever you like — e.g. a JSON file in the module's data dir
```

An exception from `web_set_config` becomes an HTTP 422 with your message
displayed to the user.

### Worked example — Lua

```lua
-- modules/Lua/starboard/starboard.dashboard.lua   (next to starboard.lua)
D = {}
D.schema = {
  {key = "enabled",   label = "Enabled",  type = "toggle", scope = "guild", guild_scoped = true},
  {key = "threshold", label = "Reaction threshold", type = "range",
   min = 1, max = 50, step = 1, scope = "guild", guild_scoped = true},
  {key = "emoji",     label = "Reaction emoji", type = "text",
   placeholder = "⭐", scope = "guild", guild_scoped = true},
  {key = "tone",      label = "Embed style", type = "select",
   options = {"info", "fancy"}, scope = "guild", guild_scoped = true},
  {key = "api_key",   label = "External API key", type = "secret", scope = "global"},
}

local vals = {enabled = "false", threshold = "3", tone = "info", emoji = "⭐"}

D.get = function(guild_id)
  return vals
end

D.set = function(guild_id, key, value)
  if key == "tone" and value ~= "info" and value ~= "fancy" then
    return "unknown style"        -- non-nil return = error shown in the browser
  end
  vals[key] = value
  -- persist with ctx.data_dir (available in this script's ctx table)
  return nil
end
```

The script's `ctx` table also carries the usual `ctx.log` / `ctx.log_error`
functions and `ctx.data_dir` (the module's folder) for persistence.

### Practical notes

- **Values are strings over the wire.** Booleans arrive as `"true"`/`"false"`,
  numbers as `"3"` — parse them yourself in your set function. `multi` values
  are joined with newlines by the UI (commas inside option values stay legal).
- **`secret` fields are redacted on read** to `••••••••` for non-owner callers
  (module fields are redacted in the module-config read path). The `secret`
  input in the template has **no `value` attribute** — only a placeholder — and
  the frontend skips blank secret fields on save
  (`if (type === "secret" && value === "") return;`). So the placeholder never
  reaches your `web_set_config`: leaving a secret field empty leaves the stored
  value untouched, and typing a new value replaces it. **No `••••` guard is
  needed** in your set function.
- **`channel` / `role` / `user` values are raw snowflake strings** (no `<#>` or
  `<@>` wrapping), and they imply guild scope — expect a non-empty `guild_id`
  with them.
- **You own your storage.** The convention is the module's own folder with mode
  `0600` for anything secret; the dashboard itself keeps only its 0600 module
  config (see below). Persist whenever your set function runs.
- **Don't block.** Get/set functions run on HTTP goroutines inside the bot
  process (in Python's case, round-tripped through IPC with a 5s timeout), so
  keep them fast.
- **An optional extra: sidebar tabs.** A compiled-in module can also implement
  `modules.WebTabser` (`WebTabs() []modules.WebTab`) to contribute extra
  dashboard pages to the sidebar — `internal/builtin/tickets/` does this with
  `{Name: "Tickets", Slug: "/tickets"}`. This is a compiled-in-only feature;
  Python and Lua modules get the settings panel and nothing else.
- **Dogfooding reference.** The dashboard configures *itself* through the same
  contract: its `WebConfigSchema` declares `client_id` (text), `session_secret`
  (secret), `allowed_guilds` (multi) and `exec_mode` (select) — all global. It
  intentionally does **not** expose `listen`, `public_url` or `client_secret`
  there any more; those live on the core settings page (the `dashboard:` and
  `oauth:` sections of the main `config.yml`) as the single obvious place to
  configure them. Its module config file
  (`modules/Go/dashboard/config.yml`, mode 0600, pinned historical path) holds
  the rest, and a legacy `client_secret` fallback is still read. Read
  `internal/dashboard/main.go` (`WebConfigSchema`) and
  `internal/builtin/tickets/webconfig.go` for real, shipping implementations of
  every field type.

See `internal/dashboard/README.md` for the dashboard-specific setup flow
(OAuth, the `dashboard:` **and** `oauth:` sections of the main `config.yml`)
and the RBAC table for who sees which config sections.
