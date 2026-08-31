# Dashboard

The web dashboard for the bot — a MEE6-style, role-tiered UI. It is **compiled
into the single binary** (always-on core infrastructure, not a module): Discord
OAuth2 login, live metrics, a permission-filtered command catalog, and tiered
configuration for both the core bot and opt-in modules. Runs **in-process** with
the Discord gateway.

There is **no `[p]dashboard` command** — the dashboard has always been a config
surface and now the command is gone entirely. Bind address, public URL, and the
OAuth client secret are set in the core `config.yml` (`dashboard:` / `oauth:`
sections) or from the **Admin** page of the web UI itself.

No new dependencies — it reuses disgo's `oauth2`, `cache`, and `bot` packages
already in `go.mod`.

## Build & start

```bash
# from the repo root
go build -ldflags "-X main.Version=$(./scripts/version.sh)" -o bot ./cmd/bot/   # dashboard + feature modules are compiled into the single binary
./bot                        # dashboard starts automatically (always on)
```

The dashboard starts even when not yet configured, so the `/login` setup screen
is reachable.

## Choosing the port (and recovering from a port already in use)

The default bind is `127.0.0.1:8080` (localhost only). **A bind failure never
fails startup** — if the port is already taken, the dashboard logs a warning and
stays up so you can change the port without being locked out. The dashboard
status view shows a `❌ Last Error` field in that case.

The listen address comes from, in this priority:

1. **Core `config.yml`** — the `dashboard:` section (set this when you can't
   reach the web UI, e.g. the default 8080 is taken):
   ```yaml
   dashboard:
     listen: "127.0.0.1:9090"
     public_url: "https://dashboard.example.com"
   ```
   Then restart the bot (or change it from the Admin page) to pick it up.
2. **Module config** — `modules/Go/dashboard/config.yml`'s `listen`/
   `public_url` (used only when the core `config.yml` `dashboard:` section is
   empty).

Only the non-secret, infrastructure fields (`listen`, `public_url`) can live in
the world-readable core `config.yml`. The OAuth `client_secret` lives in core
`config.yml` under `oauth:` (the single shared Discord-app credential). The
per-installation `session_secret` and `allowed_guilds` stay in the dashboard's
own 0600 config file. When both define a value, the core config wins.

## LAN access

By default the dashboard binds `127.0.0.1` (localhost only). To reach it from
other devices on your network, set `dashboard.listen` to `0.0.0.0:<port>` in
`config.yml` (all interfaces) or from the Admin page, and bind a single
interface by replacing `0.0.0.0` with the machine's LAN IP, e.g. `192.168.1.5:8080`.

When `public_url` is **not** set, the OAuth redirect URI is derived per request
from the address the browser opened (scheme + Host), so the same server works
from `http://127.0.0.1:8080`, `http://<lan-ip>:<port>`, or any hostname —
register each redirect URI you actually use under **OAuth2 → Redirects** in the
Discord Developer Portal.

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

## First-time setup (web login)

1. (Optional) Set `dashboard.public_url` to the base URL where the dashboard is
   reachable (a tunnel/domain for remote access; **skip for direct LAN access**
   — the LAN URL is auto-derived from the address you open).
2. Open the dashboard → **Login with Discord**.
3. Register the redirect URI (the `/callback` URL, printed on the login/setup
   page) under **Discord Developer Portal → your application → OAuth2 →
   Redirects**.
4. Create a **Client Secret** on that same page, then set it via the **Admin**
   page (or `oauth.client_secret` in core `config.yml`).

`client_id` is auto-derived from the bot application when left empty (it equals
the bot's application ID, available in-process as `ctx.Bot.GetClient().(*bot.Client).ApplicationID`) — nothing to fill in. `session_secret` is
auto-generated (32 random bytes) on first run and stored in the dashboard's own
0600 config file (`modules/Go/dashboard/config.yml`), alongside the
`allowed_guilds` allowlist and a backwards-compatible fallback for
`client_id`/`client_secret`. `allowed_guilds` takes a comma- or
whitespace-separated list of guild IDs; if set, only those guilds are
shown/managed. Empty = allow all servers the bot is in.

## Access tiers (RBAC)

The level is recomputed per request from the logged-in Discord user:

| level | who | can see / do |
|---|---|---|
| **owner** | `config.owner_id` | everything: metrics, full command catalog (with a "raw" toggle), core settings, the Admin page, all module config (global + guild), module enable/disable, elevated-user management, presence, logs, shutdown/restart |
| **elevated** | a bot elevated user | same global access as owner for dashboard purposes (Admin page is owner-only) |
| **staff** | manages ≥1 mutual guild (guild owner or `ManageGuild`/`Administrator`) | their manageable guilds' detail pages, **guild-scoped** module config fields, and their usable commands. No core/global settings, no module enable/disable, no elevated management, no logs |
| **regular** | shares a mutual guild but manages none | only `/` (metrics + public info) and `/commands` (only their usable commands). **All config is hidden** — `/admin`, `/modules`, `/permissions`, `/logs` and every mutating `POST /api/*` return 403 |

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

The dashboard itself implements `WebConfigurable` (see `main.go`) so it
self-configures from the web — and dogfoods every field type end-to-end.

## API surface (REST, all behind auth + tier + CSRF)

`GET /api/me` · `GET /api/metrics` · `GET /api/commands[?guild=&raw=]` ·
`GET /api/guilds` · `GET /api/guild/{id}` ·
`GET /api/modules` · `POST /api/modules/{name}/{enable|disable}` ·
`GET /api/settings` · `POST /api/settings/core` ·
`GET /api/settings/module/{name}[?guild=]` · `POST /api/settings/module/{name}` ·
`POST /api/presence` · `GET /api/permissions/elevated` ·
`POST /api/permissions/elevated/{add|remove}` · `GET /api/logs?tail=N` ·
`POST /api/shutdown` · `POST /api/restart`.

Mutating endpoints require header `X-CSRF-Token` matching the token in `/api/me`
and the `<meta name="csrf-token">` tag.

## Testing

```bash
go test ./internal/dashboard/   # template-render coverage + field types (no Discord needed)
```

## Notes & limitations

- **In-process**: a handler panic is recovered and returned as 500 JSON (never
  crashes the bot). Long/async work runs off the gateway goroutines.
- **Sessions are in-memory only** — restart the bot = re-login. Acceptable for a
  private dashboard.
- **Default `listen` is `127.0.0.1:8080`** (localhost only). Bind all interfaces
  for LAN access; for remote access use a reverse proxy / tunnel and set
  `public_url` (https) accordingly — the `Secure` cookie flag is derived per
  request from the scheme the browser actually uses, so plain-http LAN origins
  work too.
- Live OAuth end-to-end verification (real Discord token + dev-portal redirect)
  is an owner-run step after building. Offline checks: `go vet ./...`, the
  template tests, and the package test suite.
