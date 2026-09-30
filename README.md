# Misfit Bot

A self-hosting Discord bot in Go: a compiled-in core (web dashboard + feature modules) with hot-loadable Lua and Python modules, and a GitHub self-updater.

![Misfit Bot banner](assets/banner.png)

## Features

- **Hot-loadable module system** — Lua scripts (gopher-lua) and Python modules (subprocess IPC with per-module venvs). Load, unload, and reload them at runtime with `[p]load` / `[p]unload` / `[p]reload`. The dashboard and the cleanup/tickets/imagefilter feature modules are compiled into the single binary instead (core infrastructure, always on).
- **13 core commands** with prefix **and** slash equivalents: `ping`, `uptime`, `info`, `help`, `modules`, `load`, `unload`, `reload`, `shutdown`, `restart`, `permissions`, `ratelimit`, `update`.
- **Three-tier permission system** — bot owner/elevated users bypass everything, guild owners bypass Discord permission requirements, everyone else is checked against Discord role permissions.
- **Web dashboard** — Discord OAuth2 login (mutual-guild enforced), 4 RBAC tiers (owner / elevated / staff / regular), live metrics, a permission-filtered command catalog, and per-module settings panels rendered from each module's opt-in `WebConfigurable` schema. No dashboard code changes needed to support a new module's settings.
- **GitHub self-updater** — polls the bot's own repository for new commits and pull requests, posts embed notifications to a Discord channel, and can automatically pull the latest code, rebuild the binary, and self-restart.
- **Voice API for modules** — `VoiceManager` lets modules join voice channels and play audio via FFmpeg.
- **First-run onboarding wizard** — interactive setup of token, owner ID, and prefix.
- **JSON logging** (async, stdout + file), **per-user rate limiting** (owner bypass), **config backups** (create / verify / restore, surfaced owner-only at `/api/backups`).
- **Multi-distro install script** + Nix dev shell.

## Requirements

- **Linux only** — the voice stack (cgo + FFmpeg/Opus) and the install script target Linux servers.
- **Go 1.26.x** — build the single binary from source (`go.mod` pins `go 1.26.4`).
- **git** — required by the self-updater.
- **ffmpeg** — required for voice playback.
- **Python 3 + venv + pip** — required only for Python modules (each gets its own venv).
- **pkg-config + libopus development headers** — required for the cgo Opus dependency (voice).
- **A Discord application** with a bot token and the privileged intents `MESSAGE_CONTENT`, `GUILD_MEMBERS`, and `PRESENCES`.

## Installation

### Option 1: install script

```bash
./install.sh                    # install system deps, Go toolchain, and build
./install.sh --no-deps          # build only (deps already present)
./install.sh --check            # verify prerequisites without changing anything
./install.sh --skip-imagefilter # skip the image-filter runtime (ONNX + CLIP)
```

`install.sh` also provisions the **image filter** runtime: the ONNX Runtime library and the CLIP `b32` model (both gitignored, so a fresh clone has neither). It is idempotent and a no-op once they exist, and never fails the install — a machine that cannot build them simply leaves the filter cold, with the reason shown on the dashboard. The same step runs automatically after a self-update. Non-default CLIP variants are exported on demand with `scripts/export_clip_onnx.py`.

Auto-detects the distro; supports apt (Debian/Ubuntu), pacman (Arch), dnf (Fedora/RHEL), zypper (openSUSE), apk (Alpine), xbps (Void), emerge (Gentoo), nix (NixOS), and FreeBSD pkg. Override detection with `DISTRO=<id> ./install.sh`.

### Option 2: Nix

```bash
nix-shell   # drops into a dev shell with everything pinned (shell.nix)
```

### Option 3: manual

```bash
go build -ldflags "-X main.Version=$(./scripts/version.sh)" -o bot ./cmd/bot/
bash scripts/setup_imagefilter.sh   # optional: provisions the image filter runtime
```

The `-ldflags` stamp injects the version from the `VERSION` file (the single
source of truth). Build without it and the bot reports `dev` — harmless, but
the updater then falls back to commit counting instead of release versions.
See [Versioning](#versioning).

### First run

Run `./bot`. If no `config.yml` exists, an interactive onboarding wizard asks for your bot token, owner ID, prefix, and bot name. The default prefix is `[p]`.

## Configuration

All configuration lives in `config.yml` (auto-created, auto-saved). It is **gitignored** — it holds secrets (bot token, GitHub token, OAuth client secret) and must never be committed.

| Section | Keys |
|---|---|
| `bot` | `token`, `prefix`, `owner_id`, `elevated_ids`, `name`, `status`, `tos_url`, `privacy_url`, `bot_allowlist` |
| `modules` | `auto_load`, `path`, `disabled`, `enabled_modules` (gates the compiled-in builtins: cleanup, tickets, imagefilter) |
| `logging` | `enabled`, `file_path`, `level` |
| `dashboard` | `listen`, `public_url` (optional pins for the dashboard module) |
| `oauth` | `client_secret` (Discord app OAuth2 secret, shared with the dashboard) |
| `updater` | `enabled`, `repo`, `branch`, `token`, `check_interval`, `auto_pull`, `notify_channel` |
| `mcp` | `enabled`, `token` (built-in MCP server on the dashboard listener; token auto-generated when empty) |

Config values can be changed at runtime from the web dashboard's **Configuration** tab (validated, persisted; updater keys too). The `[p]update set <key> <value>` command also remains for updater keys.

## Commands

All commands work with the prefix (e.g. `[p]ping`) and as slash commands (`/ping`). In this documentation `[p]` is a placeholder for your configured prefix (default: `[p]`) — with a `!` or `?` prefix, the same command is `!ping` or `?ping`.

| Command | Description | Access |
|---|---|---|
| `ping` | Check bot latency | Public |
| `uptime` | Check bot uptime | Public |
| `info` | Show bot information | Public |
| `help` | Show available commands (permission-filtered) | Public |
| `modules` | List loaded/available modules, or `enable`/`disable <name>` a compiled-in feature (cleanup/tickets only — the config key `modules.enabled_modules` also accepts imagefilter; applies after restart) | Owner |
| `load` / `unload` / `reload` | Manage Lua/Python modules (`all` supported) | Owner |
| `shutdown` / `restart` | Stop / restart the bot | Owner |
| `permissions` | Manage elevated (owner-like) users | Owner |
| `ratelimit` | Inspect/reset per-user rate limits | Owner |
| `update` | `check` / `now` / `status` / `test` / `set` — self-updater control | Owner |

## Modules

The web dashboard and the cleanup/tickets/imagefilter builtins are **compiled into the single binary** and cannot be loaded or unloaded at runtime. The three builtins are gated by `modules.enabled_modules` (missing key = enabled) and can be toggled with `[p]modules enable|disable <name>` (cleanup and tickets only; imagefilter is configured per-server on the dashboard). Lua and Python modules stay hot-loadable and live in language folders under `modules/`:

- **Lua** — `modules/Lua/<name>/<name>.lua` (or `main.lua`)
- **Python** — `modules/Python/<name>/main.py` (+ optional `requirements.txt`; per-module venv, auto-`pip install`)

Each Lua/Python module's runtime data (config files, saves, logs) lives inside its own folder and is gitignored; source stays tracked. The compiled-in feature modules get a per-module data directory at `modules/<name>/` — the dashboard is the pinned exception, still using `modules/Go/dashboard/` (with a fallback to `module_configs/dashboard/`). A 0.1.0 → 0.2.0 upgrade adopts the plugin-era `modules/Go/<name>/` directories for cleanup/tickets/imagefilter automatically; when both locations hold state the bot refuses to merge and logs a warning, so reconcile by hand. See [docs/builtin-data-paths.md](docs/builtin-data-paths.md).

In-repo examples of authorable (hot-loadable) modules: `examples/lua/sample.lua` and `examples/python/samplepy/`. The compiled-in Go sources — not user-loadable — live in `internal/builtin/cleanup/` (9-subcommand message cleanup), `internal/builtin/tickets/` (the ticket system), `internal/builtin/imagefilter/` (the CLIP image filter) and `internal/dashboard/` (the web dashboard — core subsystem); `modules/voice.go` is the module-facing `VoiceManager` API.

A module can implement the optional `WebConfigurable` interface (declare a schema of typed fields — toggle, text, select, channel, secret, …) and the dashboard renders a settings panel for it automatically, with zero dashboard changes. The same request-time resolution drives the other opt-in module contracts (`modules.TicketProvider` / `TicketAdmin` / `TicketTranscript`): a module that implements the contract gets the dashboard surface, one that doesn't gets a stub or a 404. `modules.TicketTranscript.RefreshTranscript` is the tickets module's: it rebuilds the ticket's stored conversation log into its HTML transcript and returns the bytes, which backs `GET /api/tickets/<guild>/<ticket>/transcript` (guild-gated, downloads `ticket-<id>.html`) — so the uploaded `.html` is a cache of the log, regenerated at close and on demand, not a frozen close-time snapshot.

**See [MODULE_GUIDE.md](MODULE_GUIDE.md)** for the full module-authoring guide (Python and Lua).

## Versioning

`VERSION` at the repository root is the **single source of truth** for the bot's
version (currently `0.2.0`). Every build site stamps it into the binary:

```bash
go build -ldflags "-X main.Version=$(./scripts/version.sh)" -o bot ./cmd/bot/
```

`install.sh`, CI and the updater's own self-build all do this. `VERSION` is read
through **one parser** — `scripts/version.sh` for the shell sites and
`updater.ReadVersionFile` (its Go twin) for the self-build — so the tag, the
stamped binary and the release workflow cannot disagree about what the file
says. The script also *validates*: a `VERSION` that is missing, empty or not
bare SemVer fails CI, fails `install.sh` and fails the release workflow, rather
than stamping a value nothing downstream can parse. Build without the stamp and
the bot reports `dev` — an *unknown* version, not `0.0.0`: the updater then
reports commits instead of versions (see below). `[p]info` shows the
version, and `./bot --version` prints it without touching `config.yml`.

### What the numbers mean

Semantic versioning, in the **0.x era**: `v1.0.0` is *the* release, and it is
not close.

| Bump | Tag | Used for |
|---|---|---|
| **major** | `v1.0.0` | the release itself (reserved — while the major is `0` it stays unused) |
| **minor** | `v0.(Y+1).0` | feature waves and notable reworks (tickets v2, a dashboard redesign) |
| **patch** | `v0.Y.(Z+1)` | small features, bugfixes, subtle fixes — the default |

This is standard pre-`1.0` SemVer: the minor slot carries what would be a major
later, the patch slot carries everything smaller.

### Bumping

Bumping **is part of the PR** — edit `VERSION` in your branch alongside the
change, and the merge to `main` becomes that release. No labels, no separate
release branch, no version to guess after the fact.

```text
fix(tickets): stop double-closing archived tickets   →  VERSION: 0.2.0 → 0.2.1
feat(dashboard): command manager rework              →  VERSION: 0.2.0 → 0.3.0
```

The file holds **one bare version** — `0.2.0`, no `v`, no build metadata (`+`),
no trailing comment. Blank lines and `#` comment lines around it are ignored;
anything that is not a valid SemVer fails the release workflow rather than
publishing a tag the binary's own parser would reject.

CI does not fail when code changes without a bump — it posts a warning, because
pure refactors and docs-only PRs genuinely have nothing to release. Reviewers
treat that warning as a question, not a formality.

### Tagging and releases

`.github/workflows/release.yml` runs on every push to `main`: it reads `VERSION`,
and if `v<VERSION>` does not exist yet it creates the annotated tag and publishes
a GitHub Release with `--generate-notes` (the changelog is the merged PRs since
the previous tag). It is idempotent — a merge that did not bump `VERSION` finds
its tag already there and does nothing.

## Self-updater

The bot polls its own GitHub repository every `check_interval` seconds (default 300):

1. **Notifications** — new open PRs and new commits on the tracked branch get embed notifications (author row → bold title → markdown description) posted to `notify_channel`. Delivery is at-least-once: a failed send is retried on the next poll. Merge commits are skipped and the first poll after enabling seeds silently.
2. **Auto-update** — when `auto_pull` is on (or via `[p]update now`), the bot fetches the remote (the GitHub token is injected per invocation — never stored in `.git/config`), rebuilds the single binary (dashboard and feature modules included), swaps in the new binary, and self-restarts.

The GitHub token lives **only** in the gitignored `config.yml` and never appears in any commit.

## Dashboard

The web dashboard runs in-process (compiled into the single binary). Setup:

1. Create a Discord application and note the OAuth2 **client secret** (Dev Portal → OAuth2 → General).
2. Set `oauth.client_secret` in `config.yml` (or from the dashboard's Admin page).
3. Open the dashboard and complete the **Login with Discord** flow; register the redirect URI it shows in the Dev Portal.
4. The default bind is `http://127.0.0.1:8080` (expose remotely via a reverse proxy/tunnel and set `dashboard.public_url`).

Users log in via Discord OAuth2 and must share at least one server with the bot. Access is tiered: **owner** and **elevated** (everything), **staff** (manages ≥1 guild via ManageGuild/Admin/owner — guild-scoped module settings), and **regular** (status, commands they can actually run — filtered with the same rules as `[p]help`).

The dashboard **Commands** tab lists every command once and runs them in-process (responses are captured into the page, never posted to Discord). The **Command execution way** setting (dashboard module settings, default `prefix`) picks which implementation is shown and executed: `prefix` runs text-command logic (prefix commands require Discord's Message Content intent to be usable in Discord), `slash` prefers the slash implementations and works without the intent. Commands that only exist in one form work regardless of the setting. Slash execution from the dashboard is best-effort: slash implementations that rely on interaction-only flows beyond the standard response helpers (`ctx.Respond` / `ctx.respond`) may produce an empty result.

Each command card's **Run** button executes the command in-process with **no arguments** (`POST /api/exec` takes an optional `args` array; the commands tab sends an empty one). The JSON catalog (`GET /api/commands`) does expose each command's slash-option schema — subcommand groups, typed options and their choices — for tools that want to build their own argument forms, but the dashboard page itself renders no argument inputs.

### Connecting an MCP client

The bot ships a built-in **MCP (Model Context Protocol)** server, mounted at `/mcp` on the dashboard listener (it follows `dashboard.listen` — default `http://127.0.0.1:8080`). Register it with:

```bash
claude mcp add -t http misfit http://127.0.0.1:8080/mcp --header "Authorization: Bearer <token from config.yml mcp.token>"
```

The bearer token is **auto-generated on first start** and saved to `config.yml` (`mcp.token`) — read it there, or replace it in that file (the dashboard's core settings API accepts `mcp_token` too). The endpoint exposes 13 tools covering bot status, commands, logs, config, message sending, and command execution. It grants **full owner-level access** (the token *is* the owner identity, and execution results are never posted to Discord), so treat the token as a secret and keep the endpoint loopback-only or behind a reverse proxy.

## Project Structure

```
misfit-bot/
├── cmd/bot/               # Entry point — Discord connection, dispatch, lifecycle
├── commands/              # Command types + 13 core commands (prefix + slash)
├── config/                # YAML config load/save/validate
├── embed/                 # Discord embed helpers
├── logger/                # Async JSON logging (stdout + file)
├── internal/
│   ├── dashboard/         # Web dashboard — compiled-in core subsystem
│   ├── mcpserver/         # Built-in MCP server (mounted at /mcp)
│   ├── builtin/           # Compiled-in feature modules: cleanup, tickets, imagefilter
│   ├── logutil/           # Shared log-file helpers
│   └── util/              # Small shared helpers
├── modules/               # Module loaders + Lua/Python module folders
│   ├── Go/                # data homes for compiled-in modules (gitignored state; dashboard pinned here)
│   ├── Lua/               # Lua modules (modules/Lua/<name>/<name>.lua)
│   ├── Python/            # Python modules (modules/Python/<name>/main.py)
│   ├── voice.go           # VoiceManager API for modules
│   └── lua_loader.go …    # Loader infrastructure (lua/python bridges, ipc)
├── onboarding/            # First-run setup wizard
├── permissions/           # Three-tier permission system
├── ratelimit/             # Per-user rate limiting
├── updater/               # GitHub self-updater + notifications
├── sdk/python/misfit/     # Python module SDK
├── examples/              # Sample Lua + Python modules
├── docs/                  # Supplemental documentation
├── assets/                # Banner + static assets
├── install.sh             # Multi-distro install script
├── shell.nix              # Nix dev shell
└── MODULE_GUIDE.md        # Module authoring guide
```

## Development

```bash
go build -ldflags "-X main.Version=$(./scripts/version.sh)" -o bot ./cmd/bot/           # build the single binary (dashboard + feature modules included)
go test ./...                                                  # run all tests
go vet ./...                                                   # static analysis
```

## Contributing

This is a personal project built for the Misfit's Tavern Discord server; pull requests from self-deployed instances or server-specific modifications will not be merged. Bug reports are welcome if they reproduce on a clean upstream setup. See [CONTRIBUTING.md](CONTRIBUTING.md) for details.

## License

[MIT](LICENSE)
