# Repository Guidelines

## Project Overview

Misfit Bot is a private, self-hosted Discord bot for a single community (see
`CONTRIBUTING.md`). It is a **single Linux binary** built with cgo: the web
dashboard, MCP server, and six feature modules are compiled in, while Lua and
Python modules remain hot-loadable at runtime.

- **Commands are slash-only.** There is no prefix-command parser; message
  events are used by module features, not command dispatch.
- **No database.** State is YAML/JSON on disk: `config.yml`, `.env` secrets,
  per-module files under `modules/`, and small JSON state files. The dashboard
  uses in-memory sessions.
- **The owner is the primary operator.** Changes are server-specific;
  `CONTRIBUTING.md` says PRs from self-deployed forks are not merged.

## Architecture & Data Flow

### Startup and wiring

- `cmd/bot/main.go` is the only entry point. `main()` resolves the working
  directory and flags (`--no-modules`, `--version`), creates runtime dirs,
  loads or runs onboarding for `config.yml`, initializes the async logger,
  command overrides, permissions, rate limiter, and the self-updater. It then
  runs the restart loop; after an applied self-update it re-execs the new
  binary before calling `run()` again.
- `run()` creates the disgo client (guild/member/role/channel/emoji caches),
  opens the gateway with the configured intents, and on Discord close `4014`
  retries without the privileged intents and runs in gated mode. It attaches
  REST/voice, installs the Lua and Python loaders, loads saved (or AutoLoad)
  dynamic modules, registers the compiled-in builtins, registers slash
  commands, builds the MCP server, and finally constructs and starts the
  dashboard **in-process**.
- `botAdapter` implements `commands.Interface`; it is the seam used by
  commands, modules, the dashboard, and MCP. Builtins are registered before
  slash registration so their commands are included in the first sync.

### Request flows

- **Discord slash command:** `onSlashCommand` resolves a core command first,
  then loaded-module commands; `commands.SlashArgs` builds the positional
  argument vector from the declared option order; the interaction is deferred;
  permission gates run (`SuperOwnerOnly`, `CanUse`, per-command overrides,
  rate limit); `Execute` responds with follow-up embeds/text. Only
  error-colored (red) embeds auto-delete, after 7 seconds.
- **Dashboard/MCP command:** `botAdapter.ExecuteCommand` runs a registered
  slash command with a virtual context. Responses are captured, nothing is
  posted to Discord, and the same permission rules apply. `SuperOwnerOnly`
  commands are never executable from the web. The dashboard additionally
  requires the command to be on `exec_allowlist`; MCP is owner-equivalent.
- **Events:** disgo listeners call package handlers which fan out through
  `safeDispatchFor` to every loaded module/builtin `EventHooks`. A panic in
  one handler is recovered and logged; the rest still run. Hooks are
  registered in `OnLoad` and removed on unload.
- **Dashboard HTTP:** `buildHandler` is
  recovery → request logging → security headers → auth → audit → mux. OAuth2
  login uses signed session cookies and a mutual-guild check; RBAC tiers are
  `owner`, `elevated`, `staff`, `regular`. Pages render `rd_*` templates;
  `/api/*` JSON mutations require the CSRF header. Handlers are panic-recovered
  because the dashboard shares the bot process.
- **MCP:** the streamable-HTTP handler is mounted at `/mcp` on the dashboard
  listener. Its bearer token is read from `config.yml` per request (live kill
  switch) and is owner-equivalent. Tools cover status/config, module config
  and lifecycle, command execution, message read/send/edit/delete, and the
  updater.
- **Updater:** polls the configured GitHub repo/branch, posts commit/PR
  notifications to per-server channels, resolves version tags reachable from
  the tracked branch, and (when `auto_pull`) fetches, fast-forwards, rebuilds
  with the `VERSION` stamp, swaps the binary, and triggers a self-exec.

### Built-in features

`internal/builtin/` contains the compiled-in features: `cleanup` (message
cleanup, stateless), `tickets` (per-guild config, panels, ticket JSON, mirrored
media, HTML transcripts), `imagefilter` (ONNX CLIP detection, reference-image
galleries, punishments), `welcome` (join/leave messages and embed builder),
`moderation` (warnings, cases, escalation rules), and `reactionroles` (panels
and reaction-role sync). They are gated by `modules.enabled_modules` at startup
(missing key = enabled); changing the map takes effect after a restart. The
runtime builtin guard used by `/modules enable|disable` currently recognizes
`cleanup`, `tickets`, and `imagefilter`; edit the config map directly for the
other builtin names.

### Module system

- `modules.Module` requires `OnLoad`, `OnUnload`, `SlashCommands`, and
  `Dependencies`; dynamic modules are Lua files or Python source directories.
  There is **no Go plugin path** — compiled-in Go features use the same
  interface but are registered by the manager.
- Lua modules live at `modules/Lua/<name>/<name>.lua` (or `main.lua`) and use
  the global `M` table. Python modules live at
  `modules/Python/<name>/main.py`, use the SDK in `sdk/python/misfit/`, and run
  in a subprocess with a per-module venv.
- Dashboard settings are opt-in via `WebConfigurable`: a module declares
  `[]modules.ConfigField` and implements `WebGetConfig`/`WebSetConfig`; the
  dashboard renders it automatically. Lua/Python use separate
  `<name>.dashboard.lua` / `dashboard.py` integration files. Optional
  dashboard contracts live in `modules/*_contract.go`.
- Each module gets a `modules.Context` (`Bot`, `Rest`, `Events`,
  `VoiceManager`, `Logger`, `DataDir`, `CoreConfig`). Builtins should implement
  `Disablable` for `/modules disable`; a failed runtime reload loses the module
  until restart, so prefer load/restart over reload when recovery matters.

## Key Directories

| Path | Purpose |
|---|---|
| `cmd/bot/` | Entry point, startup/restart loop, gateway listeners, slash dispatch, `botAdapter`, subsystem wiring. |
| `commands/` | Slash command types and core commands; positional argument extraction; slash-schema validation; per-command overrides; web-execution permission gate. |
| `config/` | `config.yml` model/validation, `.env` secret storage, backups. |
| `modules/` | Dynamic-module manager and Lua/Python loaders, event hooks, dashboard/module contracts, voice manager. |
| `sdk/python/misfit/` | Python module SDK and runner (`Module`, `SlashCommand`, IPC, context, logger). |
| `internal/dashboard/` | In-process web UI: OAuth, RBAC, pages, JSON API, audit log, templates and static assets. |
| `internal/builtin/` | Compiled-in cleanup, tickets, imagefilter, welcome, moderation, and reactionroles features. |
| `internal/mcpserver/` | MCP server, bearer auth, and tool implementations. |
| `updater/` | GitHub polling, notifications, version comparison, pull/build/swap/self-exec. |
| `logger/`, `onboarding/`, `permissions/`, `ratelimit/`, `embed/` | Async JSON logger, first-run wizard, three-tier permissions, per-user limiter, embed helpers. |
| `scripts/`, `install.sh`, `shell.nix` | Runtime provisioning, version parsing, image-filter artifacts, multi-distro install, Nix dev shell. |
| `docs/`, `MODULE_GUIDE.md`, `internal/dashboard/README.md` | Builtin data-path reference, module-authoring guide, dashboard operations/design notes. |
| `examples/` | In-repo sample Lua and Python modules. |
| `legal/` (gitignored) | Runtime `terms.md` and `privacy.md`, read per request and seeded on first use. |

## Development Commands

```bash
# Build (cgo; requires a C toolchain + pkg-config + libopus headers)
CGO_ENABLED=1 go build -ldflags "-X main.Version=$(./scripts/version.sh)" -o bot ./cmd/bot/

# Format and static checks (no Makefile / golangci-lint in this repo)
gofmt -w <changed .go files>
go vet ./...

# Tests
go test ./...
go test ./internal/dashboard/          # fast dashboard/template suite
go test -race ./...                    # recommended locally for concurrency paths

# Dashboard visual preview (writes HTML to /tmp/dash-preview)
PREVIEW_DUMP=1 go test ./internal/dashboard -run TestPreviewDumpRedesign

# Run
./bot
./bot --no-modules
./bot --version

# Optional image-filter runtime (ONNX Runtime + CLIP model are gitignored)
bash scripts/setup_imagefilter.sh --check
bash scripts/setup_imagefilter.sh

# Install / Nix
./install.sh --check
./install.sh
nix-shell --run './install.sh --no-deps'
```

`go test ./...` stays green without the ONNX artifacts: those tests skip when
the runtime library or model is missing.

## Code Conventions & Common Patterns

### Commands and permissions

- Use `commands.SlashCommand` / `RegisterCoreSlash`; do not add prefix-command
  code. Declare all required options before optional ones. Validate option
  trees with `commands.ValidateSlashOptions` before registration: Discord
  rejects the whole command batch (`50035`) for a single invalid tree.
- Read arguments only through `commands.SlashArgs` (or the equivalent in
  module SDKs). Values are strings in declared order; subcommand groups emit
  `[group, sub, ...]`; absent optional options are skipped, not blanked.
- Permission tiers: owner/elevated bypass; guild owner bypasses
  `RequiredPerm`; `OwnerOnly` blocks non-owners; `SuperOwnerOnly` is checked
  before `CanUse` and is never web-executable. Dashboard/MCP execution must
  mirror these rules via `CanExecuteWeb` or `ExecuteCommand`.
- Respond with `embed.Success`, `embed.Error`, `embed.Info`, or
  `embed.Warning`. `WithFields` **replaces** the field slice; build one slice
  and pass it once.
- Auto-delete is intentionally narrow: only the first embed being red
  (`embed.ColorError`) deletes the response, after 7s. Success/info/warning/
  usage/status/plain-text responses persist.
- Slash handlers must defer before slow REST work (3-second interaction
  deadline). Component handlers auto-defer unless a module claims the
  interaction via `RawComponentHandler` and sends its own response.

### Modules and dashboard integration

- Implement `modules.Module`; register event hooks in `OnLoad` only. Use
  `ctx.DataDir` for module state, not the process CWD.
- Dynamic modules are Lua or Python only. Compiled-in features use
  `RegisterBuiltinsWithFilter` and implement `Disablable` when disabling should
  deregister hooks/flush state.
- For dashboard settings, implement `modules.WebConfigurable`; values cross the
  boundary as strings and the module owns validation. All field render types
  are `modules.FieldType*` (`toggle`, `text`, `textarea`, `number`, `range`,
  `select`, `multi`, `secret`, `channel`, `role`, `user`). Never add
  module-specific rendering to dashboard code.
- Builtin integration contracts (`TicketProvider`, `TicketAdmin`,
  `TicketTranscript`, `ImageFilterAdmin`, welcome/moderation/reactionroles
  contracts) are resolved at request time. Implement the contract to expose a
  surface; absence should yield a stub/404, not a dashboard import.
- Dashboard page pattern: build `d := m.baseData(session)`, set `d.Page` and
  `d.Content`, then `m.tmpl.render(w, "rd_<page>", d)`. Templates are
  `rd_*.html`; shared chrome is `rd_header`/`rd_footer`; settings fields use
  the `rd_field` partial. Static assets are `go:embed` — there is no frontend
  build step.
- Frontend JS is vanilla, in `web/static/app.js`; activate a block only when
  its elements exist, read CSRF from `<meta name="csrf-token">`, and send it as
  `X-CSRF-Token` on mutations. Extend the design tokens in
  `web/static/style.css` rather than duplicating rules.

### Errors, concurrency, and state

- Wrap errors with context (`fmt.Errorf("load tickets: %w", err)`) and return
  them to the dispatcher, which logs and posts a red error embed. Do not panic
  in HTTP/command handlers; recovery middleware/`safeDispatch` exists because
  the dashboard and modules share the bot process.
- Guard shared maps/slices with the local mutex; the updater, audit log,
  sessions, and module manager are deliberately concurrent. Use
  `sync.RWMutex` for read-heavy state and never hold a lock across network or
  filesystem I/O when avoidable.
- Persist user-facing core settings through `config.Set`/`Save` (validation +
  disk write), not by mutating `Cfg` directly. Secrets belong in `.env` at
  0600: `DISCORD_BOT_TOKEN`, `DISCORD_OAUTH_CLIENT_SECRET`,
  `UPDATER_GITHUB_TOKEN`. The MCP bearer token is the exception and lives in
  `config.yml`.
- The dashboard's data dir is the historical
  `modules/Go/dashboard/`; other stateful builtins use `modules/<name>/`
  (`cleanup` is stateless). Runtime state is gitignored; never commit
  `config.yml`, `.env`, logs, ticket/case data, reference images, models, or
  `lib/`.
- Use `snowflake.ID.String()` (never `string(id)`) and `util.ExtractID` for
  mention/ID normalization. Log through the provided `Logger`/`Log`; do not log
  secrets or raw tokens.
- Bump `VERSION` for code changes. `scripts/version.sh` is the single parser
  and validator; CI and the release workflow use it.

### Naming and layout

- Go packages are lower-case and feature-oriented. Constructors are `New...`;
  optional capabilities are small interfaces/contracts (`WebConfigurable`,
  `Disablable`, `HasWebConfig`). Tests live beside the code as `_test.go`.
- Dashboard templates use `rd_<page>.html`; assets are versioned with
  `?v=<VERSION>` by the templates.
- Module integration files are named `<module>.dashboard.lua` (Lua) and
  `dashboard.py` (Python), beside the module source.
- Per-guild state uses `guilds/<guildID>.yml` / `cases/<guildID>.json`; tickets
  use `tickets/<guildID>/<ticketID>.json` and `<ticketID>/files/`. Follow the
  existing atomic-write helpers and 0600 permissions for sensitive files.

## Important Files

| File | Why it matters |
|---|---|
| `cmd/bot/main.go` | Startup, restart/self-exec loop, gateway listeners, slash dispatch, `botAdapter`, all subsystem wiring. |
| `commands/command.go` | `SlashCommand`, `Context`, `CommandResult`, and the `commands.Interface` contract. |
| `commands/core.go` | Core commands (`ping`, `uptime`, `info`, `help`, `modules`, `load`, `unload`, `reload`, `update`) and their embed patterns. |
| `commands/slashargs.go`, `commands/slash_validate.go` | Declared-order argument mapping and Discord option-schema validation. |
| `commands/web.go`, `commands/overrides.go` | Web execution permission gate and per-command override enforcement. |
| `modules/module.go` | `Module`, `Context`, `EventHooks`, `ConfigField`, manager, builtin registration. |
| `modules/*_contract.go` | Optional dashboard surfaces: tickets, imagefilter, welcome, moderation, reactionroles. |
| `internal/dashboard/main.go`, `server.go`, `pages.go` | Dashboard construction/start, middleware chain/routing, page rendering and settings schema. |
| `internal/dashboard/auth.go`, `acl.go`, `api*.go` | OAuth/session handling, 4-tier RBAC, JSON endpoints and CSRF. |
| `internal/builtin/tickets/main.go`, `store.go`, `transcript.go` | Ticket lifecycle, file-backed store, HTML transcript generation. |
| `internal/builtin/imagefilter/main.go`, `manager.go`, `clip.go` | Image-filter lifecycle, warm/cold manager, CLIP ONNX embedding. |
| `internal/mcpserver/server.go`, `tools.go` | MCP transport/auth and tool implementations. |
| `config/config.go`, `config/secrets.go` | YAML model/defaults/validation, `.env` secret read/write. |
| `updater/updater.go`, `updater/semver.go` | Poll/notify, version-aware update checks, fetch/build/swap/self-exec. |
| `VERSION`, `scripts/version.sh` | Single source of truth and its only parser/validator. |
| `.github/workflows/ci.yml`, `install.sh`, `shell.nix` | CI gates, system/runtime provisioning, Nix dev environment. |
| `MODULE_GUIDE.md`, `docs/builtin-data-paths.md`, `internal/dashboard/README.md` | Authoritative module, data-path, and dashboard details beyond this summary. |

## Runtime/Tooling Preferences

- **OS:** Linux only (cgo voice stack and install script). FreeBSD is partially
  supported by the installer, but Linux is the target.
- **Go:** `go.mod` pins Go **1.26.4**; CI reads the version from `go.mod`.
- **Build dependencies:** C toolchain, `pkg-config`, `libopus-dev` /
  `libopusfile-dev` (cgo Opus). Build with `CGO_ENABLED=1`.
- **Runtime dependencies:** `git` (updater), `python3` + venv/pip (Python
  modules), `ffmpeg` (voice playback). ONNX Runtime + the CLIP model are
  optional and provisioned by `scripts/setup_imagefilter.sh`.
- **Tooling:** Go modules for code dependencies; no Node/npm/Bun or frontend
  build, no Makefile, no database. System dependencies come from `install.sh`
  (or `nix-shell`). `gofmt` + `go vet` are the quality gates.
- **Processes:** dashboard and MCP run inside the bot process; the dashboard
  binds `127.0.0.1:8080` by default and MCP is mounted at `/mcp` on the same
  listener. There is no separate frontend/server process.
- **Versioning/releases:** bump `VERSION` with code changes (SemVer; patch by
  default). CI validates and stamps it; the release workflow tags `v<VERSION>`
  on `main` and publishes a GitHub Release. An unstamped build reports `dev`.
- **Change flow:** the owner prefers direct commit + push to `main` for small
  routine fixes; use a branch/PR for larger changes or when asked. `main` is
  protected — if a direct push is rejected, use the PR flow rather than
  bypassing protection.
- **Ignored/generated state:** `config.yml`, `.env`, `logs/`, module runtime
  dirs (`modules/tickets/`, `modules/imagefilter/`, `modules/welcome/`, etc.),
  `loaded_modules.json`, `command_overrides.json`, `updater_state.json`,
  `lib/`, and image-filter models. Keep them out of commits.

## Testing & QA

- **Framework:** standard library `testing` only (no testify/assertion
  packages). Tests are colocated `_test.go` files, mostly table-driven, with
  `t.TempDir()` for filesystem state and `httptest` for HTTP handlers.
  Discord/REST interactions use fakes/stubs; there is no live Discord in the
  automated suite. The suite currently does **not** use `t.Parallel`.
- **CI:** `.github/workflows/ci.yml` installs the Opus cgo dependencies, runs
  `go vet ./...`, `go test ./...`, validates `VERSION`, builds the stamped
  binary, and checks `./bot --version`. There is no coverage threshold and CI
  does not run the race detector.
- **Local concurrency checks:** run `go test -race ./...` after touching the
  updater, dashboard sessions/audit, module manager, or other shared state.
- **Dashboard/UI:** template-render tests cover page rendering and field types.
  For layout work, `PREVIEW_DUMP=1 go test ./internal/dashboard -run
  TestPreviewDumpRedesign` writes representative pages to `/tmp/dash-preview`.
  Real OAuth login is an owner-run manual check.
- **Image filter:** ONNX tests skip when `lib/onnxruntime` or the CLIP model is
  missing; use `bash scripts/setup_imagefilter.sh --check` before diagnosing a
  skip. The heavy tests are not required for ordinary Go changes.
- **What to test:** add regression tests for behavior boundaries, validation,
  persistence/round-trips, permissions, and failure paths. Avoid tests that
  only assert wiring, forwarding, or implementation details. When changing a
  contract or schema, update the existing tests and docs in the same change.
