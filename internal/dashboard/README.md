# Dashboard module

A MEE6-style, role-tiered web dashboard for the bot, shipped as a hot-loadable
Go plugin (`dashboard.so`). Discord OAuth2 login, live metrics, a permission-
filtered command catalog, and tiered configuration for both the core bot and
opt-in modules. Runs **in-process** with the Discord gateway.

No new dependencies — it reuses disgo's `oauth2`, `cache`, and `bot` packages
already in `go.mod`.

## Build & load

```bash
# from the repo root
go build -buildmode=plugin -o modules/Go/dashboard/dashboard.so ./modules/Go/dashboard/
./bot                       # then, in Discord:
[p]load dashboard
[p]dashboard status
```

The dashboard starts even when not yet configured (so the owner's `[p]dashboard`
command and the `/login` setup screen are reachable).

## Choosing the port (and recovering from a port already in use)

The default bind is `127.0.0.1:8080` (localhost only). **A bind failure never
fails the module load** — if the port is already taken, the dashboard logs a
warning and stays loaded with its `[p]dashboard` command available, so you can
change the port and restart without ever being locked out. `[p]dashboard status`
shows a `❌ Last Error` field in that case.

There are two places the listen address can come from, in this priority:

1. **Core `config.yml`** — the `dashboard:` section (set this when you can't
   reach the web UI, e.g. the default 8080 is taken):
   ```yaml
   dashboard:
     listen: "127.0.0.1:9090"
     public_url: "https://dashboard.example.com"
   ```
   Then `[p]reload dashboard` (or restart the bot) picks it up. You can also set
   it from Discord with `[p]dashboard set listen 127.0.0.1:9090`, which writes
   this same `config.yml` key.
2. **Module config** — `modules/Go/dashboard/config.yml`'s `listen`/
   `public_url` (used only when the core `config.yml` `dashboard:` section is
   empty).

Only the non-secret, infrastructure fields (`listen`, `public_url`) can live in
the world-readable core `config.yml`. The OAuth secrets stay in the module's
0600 config file. When both define a value, the core config wins.

## LAN access (`[p]dashboard lan`)

By default the dashboard binds `127.0.0.1` (localhost only). To reach it from
other devices on your network:

1. `[p]dashboard lan` — one command that sets `dashboard.listen` to
   `0.0.0.0:<port>` in `config.yml` (all interfaces), drops a stale
   localhost-only `public_url` if one was set, rebinds the listener, and prints
   the LAN URL. (`[p]dashboard set listen 0.0.0.0:8080` does the bind manually;
   you can also bind a single interface by replacing `0.0.0.0` with the
   machine's LAN IP, e.g. `192.168.1.5:8080`.)
2. `[p]dashboard url` — now prints the auto-detected **LAN URL**
   (`http://<lan-ip>:<port>`, detected from the host's interfaces) and the
   exact redirect URI to register in the Developer Portal.
3. Register `<lan-url>/callback` under **OAuth2 → Redirects**, create a client
   secret, `[p]dashboard set client_secret <secret>`, `[p]dashboard restart`.

When `public_url` is **not** set, the OAuth redirect URI is derived per request
from the address the browser opened (scheme + Host), so the same server works
from `http://127.0.0.1:8080`, `http://<lan-ip>:<port>`, or any hostname —
register each redirect URI you actually use.

> **Discord caveat:** Discord accepts `http://` redirect URIs for localhost and
> LAN setups — the LAN `http://` redirect URI registers fine in the Developer
> Portal (local/LAN instances of Red-Dashboard work the same way). Use HTTPS via
> a tunnel (cloudflared) or reverse proxy with `public_url` only if you want to
> reach the dashboard from the internet or via a dedicated domain.
>
> **Security note:** binding `0.0.0.0` exposes the dashboard to your whole LAN.
> Login still requires Discord OAuth with the mutual-guild check, but on a
> plain-http LAN origin session cookies are not `Secure` — keep `0.0.0.0`
> binding to trusted networks only.

### Quick recovery when 8080 is taken

```text
[p]load dashboard                 # loads despite the bind failure
[p]dashboard status               # shows ❌ Last Error: listen 127.0.0.1:8080: ...
[p]dashboard set listen 127.0.0.1:9090   # writes config.yml `dashboard.listen`
[p]dashboard restart              # binds the new address
```

…or just edit `config.yml` directly and `[p]reload dashboard`.

## First-time setup (web login)

1. `[p]dashboard set public_url https://dashboard.example.com`
   (the public base URL where the dashboard is reachable — a tunnel/domain for
   remote access, or **skip this step entirely for direct LAN access**, the LAN
   URL is auto-derived from the address you open).
2. `[p]dashboard url` — prints the OAuth **redirect URI** to register:
   `<public_url>/callback`. Add it under
   **Discord Developer Portal → your application → OAuth2 → Redirects**.
3. Create a **Client Secret** on that same page.
4. `[p]dashboard set client_secret <secret>` — this writes the secret to the
   **core bot `config.yml`** under the `oauth:` section (the single shared
   Discord-app credential — any future OAuth-using module reads it from there).
5. `[p]dashboard restart`
6. Visit the dashboard URL → **Login with Discord**.

`client_id` is auto-derived from the bot application when left empty (it equals
the bot's application ID, available in-process as `ctx.Bot.GetClient().(*bot.Client).ApplicationID`)` — nothing to fill in.
`session_secret` is auto-generated (32 random bytes) on first run and stored in
the dashboard's own config file. The dashboard's module config file
`modules/Go/dashboard/config.yml` is written with mode `0600`; these days it
holds only the `session_secret`, the `allowed_guilds` allowlist, and a
backwards-compatible fallback for `client_id`/`client_secret`. The OAuth
`client_secret` itself now lives in core `config.yml:oauth:` so other modules
can reuse it.

### `[p]dashboard` subcommands (owner-only)

| subcommand | args | effect |
|---|---|---|
| `status` | — | show listen addr, public URL, LAN URL, configured?, running? |
| `url` | — | print the login URL + the OAuth redirect URI to register (falls back to the auto-detected LAN URL when no `public_url` is set) |
| `lan` | — | bind all interfaces (`0.0.0.0:<port>`) for LAN access, rebind, print the LAN URL |
| `set` | `<key> <value>` | set a config key. `listen`/`public_url` → core `config.yml` (`dashboard:` section) + listener rebind. `client_secret` → core `config.yml` (`oauth:` section, the shared Discord-app credential) + OAuth client rebuild + session invalidation. `client_id`/`session_secret`/`allowed_guilds` → the dashboard's 0600 module config file. |
| `restart` | — | stop & restart the HTTP server with the latest config |

`allowed_guilds` takes a comma- or whitespace-separated list of guild IDs; if
set, only those guilds are shown/managed. Empty = allow all servers the bot is
in.

## Access tiers (RBAC)

The level is recomputed per request from the logged-in Discord user:

| level | who | can see / do |
|---|---|---|
| **owner** | `config.owner_id` | everything: metrics, full command catalog (with a "raw" toggle), core settings, all module config (global + guild), module load/unload/reload, elevated-user management, presence, logs, shutdown/restart |
| **elevated** | a bot elevated user | same global access as owner for dashboard purposes |
| **staff** | manages ≥1 mutual guild (guild owner or `ManageGuild`/`Administrator`) | their manageable guilds' detail pages, **guild-scoped** module config fields, and their usable commands. No core/global settings, no module load/unload, no elevated management, no logs |
| **regular** | shares a mutual guild but manages none | only `/` (metrics + public info) and `/commands` (only their usable commands). **All config is hidden** — `/settings`, `/modules`, `/permissions`, `/logs` and every mutating `POST /api/*` return 403 |

Every page enforces its tier at the middleware level (not just the UI), and the
command catalog uses the **exact** `canUse` filter the bot applies at dispatch
(aggregated across the user's mutual guilds).

**Login restriction:** a Discord account that shares no server with the bot is
refused at the `/callback` step with a friendly message.

## `WebConfigurable` — exposing module settings on the dashboard

Modules are the **single source of truth** for what settings exist and how each
one renders. The dashboard never introspects a module — it renders only what
`WebConfigSchema()` returns. Opt in by implementing this optional interface
(in `modules/module.go`):

```go
type WebConfigurable interface {
    WebConfigSchema() []ConfigField
    WebGetConfig(guildID string) (map[string]string, error) // "" = global
    WebSetConfig(guildID, key, value string) error          // "" = global
}
```

Declare one `ConfigField` per setting and pick its render `Type`. All values
are strings over the wire — your module parses ints/bools itself (same
convention as the bot's `SetConfig(key, value string)`).

| `Type` | renders as | notes |
|---|---|---|
| `toggle` | on/off switch | value `"true"` / `"false"` |
| `text` | single-line input | |
| `textarea` | multi-line input | e.g. allowlists |
| `number` | +/- spinner | `Min`/`Max` enforced |
| `range` | slider | `Min`/`Max`/`Step` required |
| `select` | single-select dropdown | `Options` required |
| `multi` | checkbox group | value = comma-joined selections |
| `secret` | masked password | redacted `"••••"` on read (owner sees real value) |
| `channel` | Discord channel picker | guild-scoped; populated from cache |
| `role` | Discord role picker | guild-scoped; populated from cache |

`Scope: "global"` fields are editable only by owner/elevated. `GuildScoped: true`
fields are also editable by staff who manage that guild (a guild picker is shown).

### Minimal example

```go
type MyModule struct{ ... }

func (m *MyModule) WebConfigSchema() []modules.ConfigField {
    return []modules.ConfigField{
        {Key: "welcome_enabled", Label: "Welcome messages", Help: "Greet new members.", Type: modules.FieldTypeToggle, Scope: "guild", GuildScoped: true},
        {Key: "volume", Label: "Volume", Help: "Default playback volume.", Type: modules.FieldTypeRange, Min: ptrF(0), Max: ptrF(100), Step: ptrF(5), Value: "100"},
        {Key: "welcome_channel", Label: "Welcome channel", Help: "Where to post welcomes.", Type: modules.FieldTypeChannel, Scope: "guild", GuildScoped: true},
    }
}

func (m *MyModule) WebGetConfig(guildID string) (map[string]string, error) {
    // read your persisted config for guildID ("" = global) and return strings
    return map[string]string{"welcome_enabled": "true", "volume": "100", "welcome_channel": ""}, nil
}

func (m *MyModule) WebSetConfig(guildID, key, value string) error {
    // validate + persist; return an error string on bad input
    return nil
}
```

That's all — the dashboard renders a toggle, a slider, and a channel picker and
reads/writes them through those three methods. Adding a new module's settings
needs **zero dashboard code changes**.

The dashboard module itself implements `WebConfigurable` (see `main.go`) so it
self-configures from the web — and dogfoods every field type end-to-end.

## API surface (REST, all behind auth + tier + CSRF)

`GET /api/me` · `GET /api/metrics` · `GET /api/commands[?guild=&raw=]` ·
`GET /api/guilds` · `GET /api/guild/{id}` ·
`GET /api/modules` · `POST /api/modules/{name}/{load|unload|reload}` ·
`GET /api/settings` · `POST /api/settings/core` ·
`GET /api/settings/module/{name}[?guild=]` · `POST /api/settings/module/{name}` ·
`POST /api/presence` · `GET /api/permissions/elevated` ·
`POST /api/permissions/elevated/{add|remove}` · `GET /api/logs?tail=N` ·
`POST /api/shutdown` · `POST /api/restart`.

Mutating endpoints require header `X-CSRF-Token` matching the token in `/api/me`
and the `<meta name="csrf-token">` tag.

## Testing

```bash
go test ./modules/Go/dashboard/   # template-render coverage + field types (no Discord needed)
```

## Notes & limitations

- **In-process**: a handler panic is recovered and returned as 500 JSON (never
  crashes the bot). Long/async work runs off the gateway goroutines.
- **Sessions are in-memory only** — restart the bot = re-login. Acceptable for a
  private dashboard.
- **Default `listen` is `127.0.0.1:8080`** (localhost only). `[p]dashboard lan`
  binds all interfaces for LAN access; for remote access use a reverse proxy /
  tunnel and set `public_url` (https) accordingly — the `Secure` cookie flag is
  derived per request from the scheme the browser actually uses, so plain-http
  LAN origins work too.
- Live OAuth end-to-end verification (real Discord token + dev-portal redirect)
  is an owner-run step after building. Offline checks: `go vet ./...`, the
  template tests, and a plugin-load sanity check that confirms `New()` returns a
  `modules.Module` implementing `WebConfigurable` with all 6 self-config fields.