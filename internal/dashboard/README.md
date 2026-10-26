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
   Then restart the bot to pick it up.
2. **Module config** — `modules/Go/dashboard/config.yml`'s `listen`/
   `public_url` (used only when the core `config.yml` `dashboard:` section is
   empty).

`listen` and `public_url` are file-only; the dashboard no longer exposes them.
The OAuth `client_secret` lives in `.env` as `DISCORD_OAUTH_CLIENT_SECRET`.
The per-installation `session_secret` is auto-generated and the `client_id` is
derived from the bot application; neither is exposed in dashboard settings.
`allowed_guilds` remains editable in the dashboard's module settings (and in
the 0600 module config file). When both config files define a value, core wins.

## LAN access

By default the dashboard binds `127.0.0.1` (localhost only). To reach it from
other devices on your network, set `dashboard.listen` to `0.0.0.0:<port>` in
`config.yml` (all interfaces) and restart, or bind a single interface by
replacing `0.0.0.0` with the machine's LAN IP, e.g. `192.168.1.5:8080`.

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

1. (Optional) Set `dashboard.public_url` in `config.yml` if you want a fixed
   external base URL. It is not required: by default the OAuth redirect is
   derived from the browser origin.
2. Open the dashboard → **Login with Discord**.
3. Register the redirect URI (the `/callback` URL, printed on the login/setup
   page) under **Discord Developer Portal → your application → OAuth2 →
   Redirects**.
4. Create a **Client Secret** on that same page and write it to `.env` on the
   host as `DISCORD_OAUTH_CLIENT_SECRET=...` (mode 0600).

`client_id` is always derived from the bot application on restart (it equals
the bot's application ID, available in-process as `ctx.Bot.GetClient().(*bot.Client).ApplicationID`) — there is nothing to fill in and no dashboard field for
it. `session_secret` is auto-generated (32 random bytes) on first run and stored
in the dashboard's own 0600 config file. Neither value is exposed in the
dashboard. `allowed_guilds` remains editable on the dashboard's module settings
page and in the 0600 module config file; it takes a comma- or whitespace-
separated list of guild IDs. Empty = allow all servers the bot is in.

## Access tiers (RBAC)

The level is recomputed per request from the logged-in Discord user:

| level | who | can see / do |
|---|---|---|
| **owner** | `config.owner_id` | everything: metrics, full command catalog (with a "raw" toggle), core settings, the Config page, all module config (global + guild), module enable/disable, elevated-user management, presence, logs, shutdown/restart |
| **elevated** | a bot elevated user | same global access as owner for dashboard purposes (the owner-only action cards on the Config page are hidden) |
| **staff** | manages ≥1 mutual guild (guild owner or `ManageGuild`/`Administrator`) | their manageable guilds' detail pages, **guild-scoped** module config fields, and their usable commands. No core/global settings, no module enable/disable, no elevated management, no logs |
| **regular** | shares a mutual guild but manages none | only `/` (metrics + public info) and `/commands` (only their usable commands). **All config is hidden** — `/admin`, `/modules`, `/permissions`, `/logs` and every mutating `POST /api/*` return 403 |

Every page enforces its tier at the middleware level (not just the UI), and the
command catalog uses the **exact** `canUse` filter the bot applies at dispatch
(aggregated across the user's mutual guilds).

**Login restriction:** a Discord account that shares no server with the bot is
refused at the `/callback` step with a friendly message.

## Legal pages (`/tos`, `/policy`)

The login card shows **Terms of Service** and **Privacy Policy** links. Both
pages are **public** — they skip the OAuth gate by design, because a visitor
reads them before signing in.

Content is read per request from `legal/terms.md` and `legal/privacy.md` in the
bot's directory (the folder beside `config.yml`), so edits apply immediately:

```bash
$EDITOR legal/terms.md legal/privacy.md
```

- GitHub-flavoured Markdown via `github.com/yuin/goldmark` (headings, lists,
  tables, links, code, blockquotes, task lists).
- Raw HTML in the markdown is omitted, so a document can never inject markup
  into the page.
- Missing files are created with starter text on the first request.
- Rendered in the dashboard's monochrome card style (`rd_legal` template,
  `.markdown` rules in `style.css`).

## Welcome editor (`/g/<id>/welcome`)

The welcome builtin's editor (staff+ for the guild) is a bespoke page, not a
`WebConfigurable` schema: Join/Leave tabs, per-event channel, message + variable
chips, and the embed builder (author/title/description/fields with inline +
reorder, image/thumbnail, footer/icon, timestamp, color picker). `app.js`
hydrates it from an embedded JSON config; the server renders channel options and
preserves a saved-but-deleted channel as a stale option.

| Route | Purpose |
|---|---|
| `GET /api/guilds/<gid>/welcome` | config + last delivery errors |
| `POST /api/guilds/<gid>/welcome` | validate + save the whole config |
| `POST /api/guilds/<gid>/welcome/test` | post a sample (the page saves first) |

All routes require staff+ managing that guild, plus CSRF on POST. The dashboard
resolves `modules.WelcomeAdmin` through the module manager, so an unloaded module
yields a 404 (API) or the "module not loaded" stub (page).

## Moderation editor (`/g/<id>/moderation`)

The moderation builtin's per-server page (staff+): enable toggle, audit and warn
destinations (text/announcement channels **or threads**), warn-notify toggle,
warn expiry, ban message deletion, a rules editor (threshold → timeout/kick/ban
with a timeout duration), and the case audit table (newest 100, filterable by
type and user ID). Warning rows offer one-click removal.

| Route | Purpose |
|---|---|
| `GET /api/guilds/<gid>/moderation` | config + last delivery error |
| `POST /api/guilds/<gid>/moderation` | validate + save settings and rules |
| `GET /api/guilds/<gid>/moderation/cases` | filtered cases (`type`, `user`, `limit`) |
| `DELETE /api/guilds/<gid>/moderation/cases/<id>` | remove a warning (audit-preserving) |

All routes require staff+ managing that guild and CSRF on mutations. The module
is resolved through `modules.ModerationAdmin`; when unloaded the page shows the
"module not loaded" stub and the API returns 404.

## Reaction roles editor (`/g/<id>/reactionroles`)

The reactionroles builtin's page (staff+): a panel list, a full editor (name,
enabled, channel/thread, remove-on-unreact, embed title/description/color/footer/
image/thumbnail), and up to 20 emoji→role rows. The emoji input accepts a pasted
Unicode emoji or a server emoji from the cache datalist (`name:id`). Saving
preserves the module-owned posted state; **Save & post** edits the existing
message (or creates one) and syncs the bot's reactions.

| Route | Purpose |
|---|---|
| `GET /api/guilds/<gid>/reactionroles` | full config + last error |
| `POST /api/guilds/<gid>/reactionroles/panels` | create a panel |
| `POST /api/guilds/<gid>/reactionroles/panels/<id>` | update a panel |
| `POST /api/guilds/<gid>/reactionroles/panels/<id>/post` | post/update message + reactions |
| `DELETE /api/guilds/<gid>/reactionroles/panels/<id>` | delete panel (+ message by default) |

All routes require staff+ managing the guild and CSRF on mutations. The module
is resolved through `modules.ReactionRolesAdmin`.

## Leveling editor (`/g/<id>/leveling`)

The leveling builtin's per-server page (staff+): a one-click **enable/disable**
switch (XP stops immediately, nothing is deleted), message/voice XP settings
(range, cooldown, per-minute rate, require-others, max level), catch-up caps,
the level-up announcement channel + template (`{user}`, `{user.name}`,
`{level}`, `{server}`), stacked role rewards, ignored channels/roles
multi-selects, and the leaderboard (top 100, or one member via `?user=`).
Member rows can have their total XP overwritten or be reset (reset also revokes
every reward role the module granted). **Sync reward roles** backfills already
eligible members.

| Route | Purpose |
|---|---|
| `GET /api/guilds/<gid>/leveling` | config + stats (catch-up/sync progress) + last error |
| `POST /api/guilds/<gid>/leveling` | validate + save the whole config |
| `POST /api/guilds/<gid>/leveling/enabled` | per-server on/off switch |
| `POST /api/guilds/<gid>/leveling/members/<uid>` | set a member's total XP |
| `DELETE /api/guilds/<gid>/leveling/members/<uid>` | reset XP + revoke reward roles |
| `POST /api/guilds/<gid>/leveling/sync` | start a reward-role backfill |

All routes require staff+ managing the guild and CSRF on mutations. The module
is resolved through `modules.LevelingAdmin`; when unloaded the page shows the
"module not loaded" stub and the API returns 404. While a catch-up replay or
reward sync is running the page polls the GET route every 2s and stops when both
are idle.

## Dashboard audit (`/audit`)

Owner + elevated only. A durable JSONL activity log at
`<bot dir>/logs/dashboard-audit.jsonl` (0600) records:

- **Page visits** — every dashboard page, including the public `/tos` and
  `/policy` (anonymous visitors are identified by IP only). Known page routes
  are logged even when a proxy or handler leaves an unexpected content type;
  unknown paths are logged only when the response is HTML.
- **Login** success/failure, **logout**, and every **authenticated mutation**
  (settings, module config, panels, backups, bot control, …).

Dashboard responses also carry `Cache-Control: no-store`, so a browser or
reverse proxy cannot serve a cached page (or a stale `/audit` view) without the
request reaching the bot.

Each entry carries time (UTC), user ID/name, role level, IP, action
(method + path / "Discord OAuth"), a short detail and the HTTP result. Entries
are retained for **30 days**; the page shows the newest 1,000 with event and
user-ID filters. Every event is appended immediately; pruning rewrites the file
atomically. The log degrades to memory-only if it cannot be opened; auditing
never fails a request.

IP handling: `RemoteAddr` is used directly; `X-Forwarded-For` / `X-Real-IP` are
trusted only when the immediate peer is loopback/private (local reverse proxy or
tunnel). Static assets and JSON API reads are not page-logged.

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
