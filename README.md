# Misfit Bot

A self-hosting Discord bot in Go: hot-loadable modules (Go/Lua/Python), an in-process web dashboard, and a GitHub self-updater.

![Misfit Bot banner](assets/banner.png)

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
go build -ldflags "-X main.Version=$(cat VERSION)" -o bot ./cmd/bot/
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
| `bot` | `token`, `prefix`, `owner_id`, `elevated_ids`, `name`, `tos_url`, `privacy_url` |
| `modules` | `auto_load`, `path`, `disabled` |
| `logging` | `enabled`, `file_path`, `level` |
| `dashboard` | `listen`, `public_url` (optional pins for the dashboard module) |
| `oauth` | `client_secret` (Discord app OAuth2 secret, shared with the dashboard) |
| `updater` | `enabled`, `repo`, `branch`, `token`, `check_interval`, `auto_pull`, `notify_channel` |

Config values can be changed at runtime from the web dashboard's **Configuration** tab (validated, persisted; updater keys too). The `[p]update set <key> <value>` command also remains for updater keys.

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
| `logs` | Enable/disable file logging (`enable` / `disable`) | Owner |
| `backup` | `create` / `verify` / `restore` / `list` config backups | Owner |
| `ratelimit` | Inspect/reset per-user rate limits | Owner |
| `update` | `check` / `now` / `status` / `test` / `set` — self-updater control | Owner |

## Modules

The web dashboard and the cleanup/tickets feature modules are **compiled into the single binary** (core infrastructure — always on, never unloaded). Lua and Python modules stay hot-loadable at runtime and live in language folders under `modules/`:

- **Lua** — `modules/Lua/<name>/<name>.lua` (or `main.lua`)
- **Python** — `modules/Python/<name>/main.py` (+ optional `requirements.txt`; per-module venv, auto-`pip install`)

Each module's runtime data (config files, saves, logs) lives inside its own folder and is gitignored; source stays tracked. The former Go-plugin folders (`modules/Go/<name>/`) remain on disk as **data homes** (config.yml, tickets/, etc.) even though no `.so` is built there.

In-repo examples: `modules/Lua/hello/hello.lua`, `modules/Python/hello_py/`, `internal/builtin/cleanup/` (9-subcommand message cleanup), `internal/builtin/tickets/`, `internal/dashboard/` (the web dashboard — core subsystem), and `modules/voice.go` (the `VoiceManager` API).

A module can implement the optional `WebConfigurable` interface (declare a schema of typed fields — toggle, text, select, channel, secret, …) and the dashboard renders a settings panel for it automatically, with zero dashboard changes.

**See [MODULE_GUIDE.md](MODULE_GUIDE.md)** for the full module-authoring guide (Go, Lua, and Python).

## Versioning

`VERSION` at the repository root is the **single source of truth** for the bot's
version (currently `0.1.0`). Every build site stamps it into the binary:

```bash
go build -ldflags "-X main.Version=$(cat VERSION)" -o bot ./cmd/bot/
```

`install.sh`, CI and the updater's own self-build all do this. Build without the
stamp and the bot reports `dev` — an *unknown* version, not `0.0.0`: the updater
then reports commits instead of versions (see below). `[p]info` shows the
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
fix(tickets): stop double-closing archived tickets   →  VERSION: 0.1.0 → 0.1.1
feat(dashboard): command manager rework              →  VERSION: 0.1.0 → 0.2.0
```

The file holds **one bare version** — `0.1.0`, no `v`, no build metadata (`+`),
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
2. **Auto-update** — when `auto_pull` is on (or via `[p]update now`), the bot fetches the remote (the GitHub token is injected per invocation — never stored in `.git/config`), rebuilds the binary and Go plugin modules, swaps in the new binary, and self-restarts.

The GitHub token lives **only** in the gitignored `config.yml` and never appears in any commit.

## Dashboard

The web dashboard runs in-process (compiled into the single binary). Setup:

1. Create a Discord application and note the OAuth2 **client secret** (Dev Portal → OAuth2 → General).
2. Set `oauth.client_secret` in `config.yml` (or from the dashboard's Admin page).
3. Open the dashboard and complete the **Login with Discord** flow; register the redirect URI it shows in the Dev Portal.
4. The default bind is `http://127.0.0.1:8080` (expose remotely via a reverse proxy/tunnel and set `dashboard.public_url`).

Users log in via Discord OAuth2 and must share at least one server with the bot. Access is tiered: **owner** and **elevated** (everything), **staff** (manages ≥1 guild via ManageGuild/Admin/owner — guild-scoped module settings), and **regular** (status, commands they can actually run — filtered with the same rules as `[p]help`).

The dashboard **Commands** tab lists every command once and runs them in-process (responses are captured into the page, never posted to Discord). The **Command execution way** setting (dashboard module settings, default `prefix`) picks which implementation is shown and executed: `prefix` runs text-command logic (prefix commands require Discord's Message Content intent to be usable in Discord), `slash` prefers the slash implementations and works without the intent. Commands that only exist in one form work regardless of the setting. Slash execution from the dashboard is best-effort: slash implementations that rely on interaction-only flows beyond the standard response helpers (`ctx.Respond` / `ctx.respond`) may produce an empty result.

Run forms are **click-driven**: pick a server in the commands tab and channel/role/user arguments render as dropdowns populated from the bot's cache (member pickers cap at 1000 members, falling back to typing an ID). Argument forms come from each command's slash options — every core command ships a full option schema (see `commands/core.go` `registerCoreSlashCommands`): enumerable options (e.g. `status type`, `set key`) render as dropdowns, numbers as number inputs, booleans as yes/no, and commands with subcommands (`backup`, `ratelimit`, `permissions`, `update`, `cleanup`) get one subcommand dropdown whose arguments switch with the selection. Only module prefix commands without a slash twin fall back to a free-text box; zero-argument commands show no input at all. The settings page works the same way: a server selector (auto-picked to the first manageable server) gives channel/role/user fields — updater notify channel, owner ID — real pickers, the dashboard's **Allowed Guilds** allowlist is a checkbox list of servers, and all global + per-server fields stay visible on one page.

## Project Structure

```
misfit-bot/
├── cmd/bot/               # Entry point — Discord connection, dispatch, lifecycle
├── commands/              # Command types + 19 core commands (prefix + slash)
├── config/                # YAML config load/save/validate
├── embed/                 # Discord embed helpers
├── logger/                # Async JSON logging (stdout + file)
├── modules/               # Module loaders + per-language module folders
│   ├── Go/                # Go plugin modules (dashboard, cleanup)
│   │   ├── dashboard/     # Web dashboard module (.so)
│   │   └── cleanup/       # Message cleanup module (.so)
│   ├── Lua/               # Lua modules (hello/)
│   ├── Python/            # Python modules (hello_py/)
│   ├── voice.go           # VoiceManager API for modules
│   └── lua_loader.go …    # Loader infrastructure (lua/python bridges, ipc)
├── onboarding/            # First-run setup wizard
├── permissions/           # Three-tier permission system
├── ratelimit/             # Per-user rate limiting
├── updater/               # GitHub self-updater + notifications
├── sdk/python/misfit/  # Python module SDK
├── install.sh             # Multi-distro install script
├── shell.nix              # Nix dev shell
└── MODULE_GUIDE.md        # Module authoring guide
```

## Development

```bash
go build -ldflags "-X main.Version=$(cat VERSION)" -o bot ./cmd/bot/           # build the single binary (dashboard + feature modules included)
go test ./...                                                  # run all tests
go vet ./...                                                   # static analysis
```

## Contributing

This is a personal project built for the Misfit's Tavern Discord server; pull requests from self-deployed instances or server-specific modifications will not be merged. Bug reports are welcome if they reproduce on a clean upstream setup. See [CONTRIBUTING.md](CONTRIBUTING.md) for details.

## License

[MIT](LICENSE)
