# Misfit Bot

A self-hosting Discord bot in Go: hot-loadable modules (Go/Lua/Python), an in-process web dashboard, and a GitHub self-updater.

## Features

- **Hot-loadable module system** — Go `.so` plugins (via the Go `plugin` package), Lua scripts (gopher-lua), and Python modules (subprocess IPC with per-module venvs). Load, unload, and reload modules at runtime with `[p]load` / `[p]unload` / `[p]reload`.
- **19 core commands** with prefix **and** slash equivalents: `ping`, `uptime`, `info`, `help`, `modules`, `load`, `unload`, `reload`, `shutdown`, `restart`, `set`, `permissions`, `eval`, `debug`, `status`, `logs`, `backup`, `ratelimit`, `update`.
- **Three-tier permission system** — bot owner/elevated users bypass everything, guild owners bypass Discord permission requirements, everyone else is checked against Discord role permissions. `eval` is additionally `SuperOwnerOnly` (bot owner only).
- **Web dashboard module** — Discord OAuth2 login (mutual-guild enforced), 4 RBAC tiers (owner / elevated / staff / regular), live metrics, a permission-filtered command catalog, and per-module settings panels rendered from each module's opt-in `WebConfigurable` schema. No dashboard code changes needed to support a new module's settings.
- **GitHub self-updater** — polls the bot's own repository for new commits and pull requests, posts embed notifications to a Discord channel, and can automatically pull the latest code, rebuild the bot (and Go plugin modules), and self-restart.
- **Voice API for modules** — `VoiceManager` lets modules join voice channels and play audio via FFmpeg.
- **First-run onboarding wizard** — interactive setup of token, owner ID, and prefix.
- **JSON logging** (async, stdout + file), **per-user rate limiting** (owner bypass), **backup/create/verify/restore** of the config.
- **Multi-distro install script** + Nix dev shell.

## Requirements

- **Linux only** — Go's `plugin` package does not work on other platforms.
- **Go 1.26.x** — module `.so` files must be built with the *exact same* Go version as the bot binary.
- **git** — required by the self-updater.
- **ffmpeg** — required for voice playback.
- **Python 3 + venv + pip** — required only for Python modules (each gets its own venv).
- **pkg-config + libopus development headers** — required for the cgo Opus dependency (voice).
- **A Discord application** with a bot token and the privileged intents `MESSAGE_CONTENT`, `GUILD_MEMBERS`, and `PRESENCES`.

## Installation

### Option 1: install script

```bash
./install.sh            # install system deps, Go toolchain, and build
./install.sh --no-deps  # build only (deps already present)
./install.sh --check    # verify prerequisites without changing anything
```

Auto-detects the distro; supports apt (Debian/Ubuntu), pacman (Arch), dnf (Fedora/RHEL), zypper (openSUSE), apk (Alpine), xbps (Void), emerge (Gentoo), nix (NixOS), and FreeBSD pkg. Override detection with `DISTRO=<id> ./install.sh`.

### Option 2: Nix

```bash
nix-shell   # drops into a dev shell with everything pinned (shell.nix)
```

### Option 3: manual

```bash
go build -o bot ./cmd/bot/
```

### First run

Run `./bot`. If no `config.yml` exists, an interactive onboarding wizard asks for your bot token, owner ID, prefix, and bot name. The default prefix is `[p]`.

## Configuration

All configuration lives in `config.yml` (auto-created, auto-saved). It is **gitignored** — it holds secrets (bot token, GitHub token, OAuth client secret) and must never be committed.

| Section | Keys |
|---|---|
| `bot` | `token`, `prefix`, `owner_id`, `elevated_ids`, `name`, `status`, `tos_url`, `privacy_url` |
| `modules` | `auto_load`, `path`, `disabled` |
| `logging` | `enabled`, `channel_id`, `file_path`, `level` |
| `dashboard` | `listen`, `public_url` (optional pins for the dashboard module) |
| `oauth` | `client_secret` (Discord app OAuth2 secret, shared with the dashboard) |
| `updater` | `enabled`, `repo`, `branch`, `token`, `check_interval`, `auto_pull`, `notify_channel` |

Config values can be changed at runtime with `[p]set <key> <value>` (validated, persisted) and `[p]update set <key> <value>` for updater keys.

## Commands

All commands work with the prefix (e.g. `[p]ping`) and as slash commands (`/ping`). In this documentation `[p]` is a placeholder for your configured prefix (default: `[p]`) — with a `!` or `?` prefix, the same command is `!ping` or `?ping`.

| Command | Description | Access |
|---|---|---|
| `ping` | Check bot latency | Public |
| `uptime` | Check bot uptime | Public |
| `info` | Show bot information | Public |
| `help` | Show available commands (permission-filtered) | Public |
| `status` | Bot status | Administrator |
| `modules` | List loaded/available modules | Owner |
| `load` / `unload` / `reload` | Manage modules (`reload all` supported) | Owner |
| `shutdown` / `restart` | Stop / restart the bot | Owner |
| `set` | Change a config value (validated) | Owner |
| `permissions` | Manage permission overrides | Owner |
| `eval` | Execute a shell command | Super owner only |
| `debug` | Runtime diagnostics (goroutines, memory) | Owner |
| `logs` | View recent logs / logging settings | Owner |
| `backup` | `create` / `verify` / `restore` / `list` config backups | Owner |
| `ratelimit` | Inspect/reset per-user rate limits | Owner |
| `update` | `check` / `now` / `status` / `test` / `set` — self-updater control | Owner |

## Modules

Modules are hot-loadable at runtime; the loader auto-detects the language by file layout:

- **Go** — `modules/<name>.so` (built with `go build -buildmode=plugin`)
- **Lua** — `modules/<name>.lua`
- **Python** — `modules/<name>/main.py` (+ optional `requirements.txt`; per-module venv, auto-`pip install`)

In-repo examples: `modules/hello.lua`, `modules/hello_py/`, `modules/cleanup/` (9-subcommand message cleanup), `modules/voice.go` (the `VoiceManager` API), and `modules/dashboard/` (the web dashboard — a full module dogfooding the `WebConfigurable` contract).

A module can implement the optional `WebConfigurable` interface (declare a schema of typed fields — toggle, text, select, channel, secret, …) and the dashboard renders a settings panel for it automatically, with zero dashboard changes.

**See [MODULE_GUIDE.md](MODULE_GUIDE.md)** for the full module-authoring guide (Go, Lua, and Python).

## Self-updater

The bot polls its own GitHub repository every `check_interval` seconds (default 300):

1. **Notifications** — new open PRs and new commits on the tracked branch get embed notifications (author row → bold title → markdown description) posted to `notify_channel`. Delivery is at-least-once: a failed send is retried on the next poll. Merge commits are skipped and the first poll after enabling seeds silently.
2. **Auto-update** — when `auto_pull` is on (or via `[p]update now`), the bot fetches the remote (the GitHub token is injected per invocation — never stored in `.git/config`), rebuilds the binary and Go plugin modules, swaps in the new binary, and self-restarts.

The GitHub token lives **only** in the gitignored `config.yml` and never appears in any commit.

## Dashboard

The web dashboard runs in-process as a module. Setup:

1. Create a Discord application and note the OAuth2 **client secret** (Dev Portal → OAuth2 → General).
2. `[p]dashboard set client_secret <secret>` (owner-only; also sets `oauth.client_secret` in `config.yml`).
3. `[p]dashboard url` prints the redirect URI — register it in the Dev Portal.
4. Open `http://127.0.0.1:8080` (the default bind; expose remotely via a reverse proxy/tunnel and set `dashboard.public_url`).

Users log in via Discord OAuth2 and must share at least one server with the bot. Access is tiered: **owner** and **elevated** (everything), **staff** (manages ≥1 guild via ManageGuild/Admin/owner — guild-scoped module settings), and **regular** (status, commands they can actually run — filtered with the same rules as `[p]help`).

## Project Structure

```
misfit-bot/
├── cmd/bot/               # Entry point — Discord connection, dispatch, lifecycle
├── commands/              # Command types + 19 core commands (prefix + slash)
├── config/                # YAML config load/save/validate
├── embed/                 # Discord embed helpers
├── logger/                # Async JSON logging (stdout + file)
├── modules/               # Module interface + Go/Lua/Python loaders
│   ├── dashboard/         # Web dashboard module (.so)
│   ├── cleanup/           # Message cleanup module (.so)
│   ├── voice.go           # VoiceManager API for modules
│   ├── hello.lua          # Lua example module
│   └── hello_py/          # Python example module
├── onboarding/            # First-run setup wizard
├── permissions/           # Three-tier permission system
├── ratelimit/             # Per-user rate limiting
├── updater/               # GitHub self-updater + notifications
├── sdk/python/custombot/  # Python module SDK
├── install.sh             # Multi-distro install script
├── shell.nix              # Nix dev shell
└── MODULE_GUIDE.md        # Module authoring guide
```

## Development

```bash
go build -o bot ./cmd/bot/                                     # build the bot
go build -buildmode=plugin -o modules/name.so modules/name/    # build a Go module
go test ./...                                                  # run all tests
go vet ./...                                                   # static analysis
```

## Contributing

This is a personal project built for the Misfit's Tavern Discord server; pull requests from self-deployed instances or server-specific modifications will not be merged. Bug reports are welcome if they reproduce on a clean upstream setup. See [CONTRIBUTING.md](CONTRIBUTING.md) for details.

## License

[MIT](LICENSE)
