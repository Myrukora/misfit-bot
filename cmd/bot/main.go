package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/config"
	"github.com/misfit/bot/embed"
	"github.com/misfit/bot/internal/builtin/cleanup"
	"github.com/misfit/bot/internal/builtin/imagefilter"
	"github.com/misfit/bot/internal/builtin/moderation"
	"github.com/misfit/bot/internal/builtin/tickets"
	"github.com/misfit/bot/internal/builtin/welcome"
	"github.com/misfit/bot/internal/dashboard"
	"github.com/misfit/bot/internal/mcpserver"
	"github.com/misfit/bot/logger"
	"github.com/misfit/bot/modules"
	"github.com/misfit/bot/onboarding"
	"github.com/misfit/bot/permissions"
	"github.com/misfit/bot/ratelimit"
	"github.com/misfit/bot/updater"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
)

var (
	// Version is the bot's release version. The single source of truth is the
	// VERSION file at the repository root; the build stamps it with
	//   go build -ldflags "-X main.Version=$(cat VERSION)" -o bot ./cmd/bot/
	// (CI, the release workflow and the updater's self-build all do). A binary
	// built without the injection reports "dev": an unknown version, which the
	// updater treats as "cannot compare by version" and falls back to
	// commit-count tracking. See README → Versioning.
	Version      = "dev"
	Dir          string
	Log          *logger.Logger
	Cfg          *config.Config
	ModMgr       *modules.Manager
	PermMgr      *permissions.Manager
	Client       *bot.Client
	ba           *botAdapter
	cmdOverrides *commands.CommandOverrides
	restartCh    chan struct{}
	shutdownCh   chan struct{}
	noModules    bool
	rtLimiter    *ratelimit.Limiter
	sigCh        chan os.Signal
	updaterMgr   *updater.Manager
	dash         *dashboard.DashboardModule
)

// Auto-delete rule: the bot auto-deletes ONLY error-colored embeds (red,
// embed.ColorError) after errorAutoDeleteDelay so the user can read the error
// before it vanishes. Every other response — success, info, warning, usage
// listings, status reports, plain text — stays on screen permanently. There is
// no per-command "preserve" list because non-error responses never auto-delete.
const errorAutoDeleteDelay = 7 * time.Second

// isErrorResponse reports whether a response's first embed is an error-colored
// (red) embed. The dispatcher uses this to decide whether to schedule deletion.
func isErrorResponse(embeds []discord.Embed) bool {
	return len(embeds) > 0 && embeds[0].Color == embed.ColorError
}

// mcpLogPaths resolves logging.file_path from the loaded config into a
// directory and a file basename (e.g. "logs/bot.log" → "logs", "bot"),
// defaulting to <Dir>/logs/bot.log. Mirrors the dashboard's logFileBase so
// the MCP get_logs tool tails the same file the dashboard log page does.
func mcpLogPaths() (string, string) {
	fp := Cfg.Logging.FilePath
	if fp == "" {
		fp = "logs/bot.log"
	}
	if !filepath.IsAbs(fp) {
		fp = filepath.Join(Dir, fp)
	}
	return filepath.Dir(fp), strings.TrimSuffix(filepath.Base(fp), ".log")
}

func saveLoadedModules() {
	names := ModMgr.GetNames()
	filtered := names[:0]
	for _, n := range names {
		if n == "cleanup" || n == "tickets" || n == "dashboard" {
			continue
		}
		filtered = append(filtered, n)
	}
	data, err := json.Marshal(filtered)
	if err != nil {
		Log.Error("Failed to marshal loaded modules: %v", err)
		return
	}
	path := filepath.Join(Dir, "loaded_modules.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		Log.Error("Failed to save loaded modules: %v", err)
	}
}

func loadSavedModules() []string {
	path := filepath.Join(Dir, "loaded_modules.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil
	}
	// Drop builtin names (dashboard/cleanup/tickets) — they were Go plugins
	// in the old layout and are now compiled-in, so a stale plugin-era state
	// file must not try to load them as modules.
	var out []string
	for _, n := range names {
		if n == "cleanup" || n == "tickets" || n == "dashboard" {
			continue
		}
		out = append(out, n)
	}
	return out
}

func main() {
	var err error
	Dir, err = os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get working directory: %v\n", err)
		os.Exit(1)
	}

	for _, arg := range os.Args[1:] {
		switch arg {
		case "--no-modules":
			noModules = true
		case "--version", "-v":
			// Handled before anything touches config.yml: CI verifies the version
			// stamp on a checkout that has no config, and the bot's own update
			// pipeline can ask the same question the /info embed answers.
			fmt.Println(Version)
			os.Exit(0)
		}
	}

	setupDirs()

	if !config.Exists(Dir) {
		Cfg, err = onboarding.Run(Dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Onboarding failed: %v\n", err)
			os.Exit(1)
		}
	} else {
		Cfg, err = config.Load(Dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
			os.Exit(1)
		}
	}

	// Logger first: everything below (overrides load, PermMgr save callback,
	// updater) logs through it.
	var logErr error
	Log, logErr = logger.New(Dir, Cfg.Logging.Level, Cfg.Logging.Enabled)
	if logErr != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", logErr)
		os.Exit(1)
	}

	// Per-command override store (global + per-guild enable/disable/restrict).
	// Loaded once; nil is safe — dispatchers treat a nil store as "allowed".
	cmdOverrides, err = commands.LoadCommandOverrides(filepath.Join(Dir, "command_overrides.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load command overrides: %v\n", err)
		os.Exit(1)
	}
	defer Log.Close()

	PermMgr = permissions.NewManager(Cfg.Bot.OwnerID, func(ids []string) {
		Cfg.Bot.ElevatedIDs = ids
		if err := config.Save(Cfg, Dir); err != nil {
			Log.Error("Failed to save elevated users: %v", err)
		}
	})
	PermMgr.LoadElevated(Cfg.Bot.ElevatedIDs)
	restartCh = make(chan struct{}, 1)
	shutdownCh = make(chan struct{}, 1)
	rtLimiter = ratelimit.New(ratelimit.DefaultConfig())

	// Register the signal channel ONCE: re-registering a fresh channel on
	// every restart would leak registrations (signal.Notify keeps a reference
	// to each channel until signal.Stop is called).
	sigCh = make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	Log.Info("Starting %s v%s", Cfg.Bot.Name, Version)
	Log.Info("Owner: %s", Cfg.Bot.OwnerID)

	// Self-update manager: constructed ONCE (never inside run()) so in-process
	// restarts don't spawn duplicate poll loops. Run() is a long-lived
	// goroutine that checks GitHub notifications + auto-pulls; the exec-based
	// self-update handoff happens in the restart loop below.
	updaterMgr = updater.New(Dir, Log, func() *config.UpdaterConfig { return &Cfg.Updater })
	// The version stamped into this binary (see VERSION) is the "from" side of
	// every update comparison. An unstamped build reports "dev" and the updater
	// falls back to counting commits.
	updaterMgr.SetVersion(Version)
	updaterMgr.OnApplied(func() {
		// Give Discord a moment to deliver the "Update Applied" embed before
		// the process re-executes itself.
		go func() {
			time.Sleep(2 * time.Second)
			select {
			case restartCh <- struct{}{}:
			default:
			}
		}()
	})
	go updaterMgr.Run(context.Background())

	for {
		// True self-update: the binary on disk was replaced by Apply(), so an
		// in-process restart would keep running the OLD code. Re-exec instead.
		if updaterMgr != nil && updaterMgr.ApplyRequested() {
			exe := filepath.Join(Dir, "bot")
			Log.Info("Self-update: launching new binary %s", exe)
			if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
				Log.Error("Self-update exec failed (%v) — continuing with in-process restart", err)
				updaterMgr.ResetApplied() // don't retry exec on the next loop pass
			}
		}
		if !run() {
			break
		}
		Log.Info("Restarting bot...")
		time.Sleep(2 * time.Second)
	}

	Log.Info("Bot stopped.")
}

// gatewayIntents builds the identify mask: the non-privileged intents the bot
// always needs, plus the privileged ones the config enables. Presence is
// deliberately never requested (the bot only sets its own status).
func gatewayIntents(cfg *config.Config) gateway.Intents {
	mask := gateway.IntentGuilds |
		gateway.IntentGuildMessages |
		gateway.IntentDirectMessages |
		gateway.IntentGuildVoiceStates |
		gateway.IntentGuildModeration |
		gateway.IntentGuildMessageReactions
	if cfg != nil && cfg.Intents.MessageContent {
		mask |= gateway.IntentMessageContent
	}
	if cfg != nil && cfg.Intents.GuildMembers {
		mask |= gateway.IntentGuildMembers
	}
	return mask
}

// privilegedGatewayIntents are the bits Discord gates behind Developer Portal
// approval. They are the only ones the bot can drop and still function.
const privilegedGatewayIntents = gateway.IntentMessageContent | gateway.IntentGuildMembers

// intentMaskWithoutPrivileged drops the privileged bits. ok is false when the
// mask had none, i.e. a 4014 close cannot be about them.
func intentMaskWithoutPrivileged(mask gateway.Intents) (gateway.Intents, bool) {
	if mask&privilegedGatewayIntents == 0 {
		return mask, false
	}
	return mask &^ privilegedGatewayIntents, true
}

// isDisallowedIntentClose reports Discord's 4014 close: the identify requested
// an intent the application may not use (not enabled / not approved).
func isDisallowedIntentClose(err error) bool {
	var closeErr *websocket.CloseError
	return errors.As(err, &closeErr) && closeErr.Code == 4014
}

func run() bool {
	var err error
	ba = &botAdapter{}
	ba.cmdOverrides = cmdOverrides
	ModMgr = modules.NewManager()

	Client, err = disgo.New(Cfg.Bot.Token,
		bot.WithDefaultGateway(),
		bot.WithVoiceManagerConfigOpts(),
		bot.WithCacheConfigOpts(
			// FlagChannels: the dashboard's per-guild pages enumerate channels
			// from the cache (imagefilter log-channel picker, command-run
			// channel targets). Without it ChannelsForGuild is always empty.
			cache.WithCaches(cache.FlagGuilds, cache.FlagMembers, cache.FlagRoles, cache.FlagChannels),
		),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(gatewayIntents(Cfg)),
		),
		bot.WithEventListeners(&events.ListenerAdapter{
			OnReady:                         onReady,
			OnGuildJoin:                     onGuildJoin,
			OnGuildLeave:                    onGuildLeave,
			OnGuildMessageCreate:            onGuildMessageCreate,
			OnGuildMessageUpdate:            onGuildMessageUpdate,
			OnGuildMessageDelete:            onGuildMessageDelete,
			OnGuildMemberJoin:               onGuildMemberJoin,
			OnGuildMemberLeave:              onGuildMemberLeave,
			OnGuildBan:                      onGuildBan,
			OnGuildUnban:                    onGuildUnban,
			OnMessageCreate:                 onMessageCreate,
			OnMessageUpdate:                 onMessageUpdate,
			OnMessageDelete:                 onMessageDelete,
			OnPresenceUpdate:                onPresenceUpdate,
			OnMessageReactionAdd:            onMessageReactionAdd,
			OnMessageReactionRemove:         onMessageReactionRemove,
			OnGuildVoiceStateUpdate:         onGuildVoiceStateUpdate,
			OnGuildChannelDelete:            onGuildChannelDelete,
			OnComponentInteraction:          onComponentInteraction,
			OnModalSubmit:                   onModalSubmit,
			OnApplicationCommandInteraction: onSlashCommand,
		}),
	)
	if err != nil {
		Log.Error("Failed to create bot client: %v", err)
		return false
	}

	Log.Info("Connecting to Discord gateway...")

	// Privileged intents are optional: when Discord rejects them (close 4014:
	// not enabled / not approved) reconnect with only the non-privileged bits
	// and run in gated mode instead of failing to start.
	intents := gatewayIntents(Cfg)
	if err := Client.OpenGateway(context.Background()); err != nil {
		reduced, retry := intentMaskWithoutPrivileged(intents)
		if !retry || !isDisallowedIntentClose(err) {
			Log.Error("Failed to open gateway: %v", err)
			return false
		}
		Log.Warn("Discord rejected the privileged intents (Message Content / Server Members); reconnecting without them — message content and member lookups will be unavailable")
		Client.Gateway = gateway.New(Client.Token, Client.EventManager.HandleGatewayEvent, gateway.WithIntents(reduced))
		if err := Client.OpenGateway(context.Background()); err != nil {
			Log.Error("Failed to open gateway without privileged intents: %v", err)
			return false
		}
		intents = reduced
	}
	ba.memberIntent = intents.Has(gateway.IntentGuildMembers)
	ba.messageContent = intents.Has(gateway.IntentMessageContent)
	Log.Info("Gateway connected (Message Content=%t, Server Members=%t; presence never requested)", ba.messageContent, ba.memberIntent)
	if !ba.messageContent {
		Log.Warn("Message Content intent is off: guild message text/attachments are unavailable (image filter, transcripts)")
	}
	if !ba.memberIntent {
		Log.Warn("Server Members intent is off: member cache/permission lookups are unavailable; the dashboard is gated to the owner and elevated users")
	}

	// Re-attach the (recreated) Discord REST client to the updater.
	if updaterMgr != nil {
		updaterMgr.SetRest(Client.Rest)
	}
	ba.updater = updaterMgr

	// Initialize voice manager
	vm := modules.NewVoiceManager(Client.VoiceManager, Log)
	ba.voiceMgr = vm

	// Initialize Lua loader
	luaLoader := modules.NewLuaLoader(ba, Log, Cfg.Bot.Token, vm)
	ModMgr.SetLuaLoader(luaLoader)

	// Initialize Python loader (needs Client.Rest for async responses)
	sdkPath := filepath.Join(Dir, "sdk", "python")
	pythonLoader := modules.NewPythonLoader(ba, Log, Client.Rest, sdkPath, Cfg.Bot.Token, vm)
	ModMgr.SetPythonLoader(pythonLoader)

	loadCoreModules(ba)

	// Register the compiled-in feature modules (cleanup, tickets) gated by
	// enabled_modules in config (missing key = enabled) BEFORE slash commands
	// are registered, so builtin slash commands (/cleanup, /tickets) are
	// included in the initial global command sync. They are constructed
	// in-process — no .so, no plugin.Open — and OnLoad runs during
	// registration (tickets loads its config/store + event hooks here).
	if err := ModMgr.RegisterBuiltinsWithFilter(Cfg.Modules.EnabledModules, &modules.Context{
		BotName:      Cfg.Bot.Name,
		OwnerID:      Cfg.Bot.OwnerID,
		DataDir:      filepath.Join(Dir, Cfg.Modules.Path),
		Logger:       Log,
		Rest:         Client.Rest,
		Bot:          ba,
		VoiceManager: vm,
		CoreConfig:   Cfg,
	}, cleanup.New, tickets.New, imagefilter.New, welcome.New, moderation.New); err != nil {
		Log.Error("Failed to register builtin modules: %v", err)
	}
	registerSlashCommands()

	// MCP server: a bearer-token-gated Model Context Protocol endpoint that
	// exposes the bot's commands + config to an agent. Always constructed; the
	// per-request auth middleware is the live kill switch (mcp.enabled +
	// mcp.token are read fresh from config.yml on every request).
	mcpLogDir, mcpLogBase := mcpLogPaths()
	if Cfg.MCP.Token == "" {
		tok := make([]byte, 32)
		if _, err := rand.Read(tok); err != nil {
			Log.Error("MCP: failed to generate token: %v", err)
		} else if err := ba.SetConfig("mcp_token", hex.EncodeToString(tok)); err != nil {
			Log.Error("MCP: failed to persist generated token: %v", err)
		} else {
			Log.Info("MCP: generated bearer token, stored in config.yml (mcp.token)")
		}
	}
	mcpSrv := mcpserver.New(mcpserver.Deps{
		Bot:       ba,
		Rest:      Client.Rest,
		ConfigDir: Dir,
		LogDir:    mcpLogDir,
		LogBase:   mcpLogBase,
		Logger:    Log,
	})

	// Start the dashboard as core infrastructure, right after the gateway is
	// up (OAuth guild checks need the client cache). It is always on — never
	// a module, never unloadable. Its data dir is pinned to the historical
	// modules/Go/dashboard/ folder so existing module-local state (session
	// secret, allowed guilds) survives the migration.
	dash = dashboard.New(dashboard.Deps{
		Bot:     ba,
		BotName: Cfg.Bot.Name,
		DataDir: filepath.Join(Dir, Cfg.Modules.Path, "Go", "dashboard"),
		Logger:  Log,
		MCP:     mcpSrv.Handler(),
	})
	mcpSrv.SetApplyCoreSetting(dash.ApplyCoreSetting)
	dash.Start()

	// Apply the persisted presence status (online/idle/dnd/invisible) once the
	// gateway is up. bot.status lives in config.yml and is applied on every
	// (re)start so the bot comes up with the owner's chosen status.
	if Cfg.Bot.Status != "" {
		if err := ba.SetPresence("", Cfg.Bot.Status, ""); err != nil {
			Log.Warn("Failed to apply persisted presence status %q: %v", Cfg.Bot.Status, err)
		} else {
			Log.Info("Applied persisted presence status: %s", Cfg.Bot.Status)
		}
	}

	sc := sigCh // package-level, registered once in main() to avoid per-restart leaks

	select {
	case <-sc:
		Log.Info("Received shutdown signal, closing...")
		if dash != nil {
			dash.Stop()
		}
		Client.Close(context.Background())
		return false
	case <-shutdownCh:
		Log.Info("Shutdown requested, closing...")
		if dash != nil {
			dash.Stop()
		}
		Client.Close(context.Background())
		return false
	case <-restartCh:
		Log.Info("Received restart signal, restarting...")
		if dash != nil {
			dash.Stop()
		}
		Client.Close(context.Background())
		return true
	}
}

func onReady(event *events.Ready) {
	Log.Info("Logged in as %s (ID: %s)", event.User.Username, event.User.ID)
}

func onGuildJoin(event *events.GuildJoin) {
	handlers := safeDispatchFor(event, "onGuildJoin", func(h *modules.EventHooks) []func(*events.GuildJoin) {
		return h.GetGuildJoinHandlers()
	})
	if handlers > 0 {
		Log.Info("Joined guild: %s (ID: %s)", event.Guild.Name, event.Guild.ID)
	}
}

func safeDispatch(event any, eventName string, getHandlers func(*modules.EventHooks) []func(any)) int {
	hooksList := ModMgr.ListModuleHooks()
	total := 0
	for _, hooks := range hooksList {
		handlers := getHandlers(hooks)
		for _, h := range handlers {
			func() {
				defer func() {
					if r := recover(); r != nil {
						Log.Error("Panic in %s handler: %v", eventName, r)
					}
				}()
				h(event)
			}()
			total++
		}
	}
	return total
}

// safeDispatchFor is a generic version of safeDispatch that reduces boilerplate.
// Each event handler becomes:
//
//	func onEvent(event *events.Type) {
//	    safeDispatchFor(event, "onEvent", func(h *modules.EventHooks) []func(*events.Type) {
//	        return h.GetTypeHandlers()
//	    })
//	}
func safeDispatchFor[T any](event T, eventName string, getHandlers func(*modules.EventHooks) []func(T)) int {
	return safeDispatch(event, eventName, func(h *modules.EventHooks) []func(any) {
		handlers := getHandlers(h)
		out := make([]func(any), len(handlers))
		for i, handler := range handlers {
			out[i] = func(e any) { handler(e.(T)) }
		}
		return out
	})
}

func onGuildLeave(event *events.GuildLeave) {
	handlers := safeDispatchFor(event, "onGuildLeave", func(h *modules.EventHooks) []func(*events.GuildLeave) {
		return h.GetGuildLeaveHandlers()
	})
	if handlers > 0 {
		Log.Info("Left guild: %s", event.Guild.Name)
	}
}

func onGuildMessageCreate(event *events.GuildMessageCreate) {
	safeDispatchFor(event, "onGuildMessageCreate", func(h *modules.EventHooks) []func(*events.GuildMessageCreate) {
		return h.GetGuildMessageCreateHandlers()
	})
}

func onGuildMessageUpdate(event *events.GuildMessageUpdate) {
	safeDispatchFor(event, "onGuildMessageUpdate", func(h *modules.EventHooks) []func(*events.GuildMessageUpdate) {
		return h.GetGuildMessageUpdateHandlers()
	})
}

func onGuildMessageDelete(event *events.GuildMessageDelete) {
	safeDispatchFor(event, "onGuildMessageDelete", func(h *modules.EventHooks) []func(*events.GuildMessageDelete) {
		return h.GetGuildMessageDeleteHandlers()
	})
}

func onGuildMemberJoin(event *events.GuildMemberJoin) {
	safeDispatchFor(event, "onGuildMemberJoin", func(h *modules.EventHooks) []func(*events.GuildMemberJoin) {
		return h.GetGuildMemberJoinHandlers()
	})
}

func onGuildMemberLeave(event *events.GuildMemberLeave) {
	safeDispatchFor(event, "onGuildMemberLeave", func(h *modules.EventHooks) []func(*events.GuildMemberLeave) {
		return h.GetGuildMemberLeaveHandlers()
	})
}

func onGuildBan(event *events.GuildBan) {
	safeDispatchFor(event, "onGuildBan", func(h *modules.EventHooks) []func(*events.GuildBan) {
		return h.GetGuildBanHandlers()
	})
}

func onGuildUnban(event *events.GuildUnban) {
	safeDispatchFor(event, "onGuildUnban", func(h *modules.EventHooks) []func(*events.GuildUnban) {
		return h.GetGuildUnbanHandlers()
	})
}

func onMessageUpdate(event *events.MessageUpdate) {
	safeDispatchFor(event, "onMessageUpdate", func(h *modules.EventHooks) []func(*events.MessageUpdate) {
		return h.GetMessageUpdateHandlers()
	})
}

func onMessageDelete(event *events.MessageDelete) {
	safeDispatchFor(event, "onMessageDelete", func(h *modules.EventHooks) []func(*events.MessageDelete) {
		return h.GetMessageDeleteHandlers()
	})
}

func onPresenceUpdate(event *events.PresenceUpdate) {
	safeDispatchFor(event, "onPresenceUpdate", func(h *modules.EventHooks) []func(*events.PresenceUpdate) {
		return h.GetPresenceUpdateHandlers()
	})
}

func onMessageReactionAdd(event *events.MessageReactionAdd) {
	safeDispatchFor(event, "onMessageReactionAdd", func(h *modules.EventHooks) []func(*events.MessageReactionAdd) {
		return h.GetMessageReactionAddHandlers()
	})
}

func onMessageReactionRemove(event *events.MessageReactionRemove) {
	safeDispatchFor(event, "onMessageReactionRemove", func(h *modules.EventHooks) []func(*events.MessageReactionRemove) {
		return h.GetMessageReactionRemoveHandlers()
	})
}

func onGuildVoiceStateUpdate(event *events.GuildVoiceStateUpdate) {
	safeDispatchFor(event, "onVoiceStateUpdate", func(h *modules.EventHooks) []func(*events.GuildVoiceStateUpdate) {
		return h.GetVoiceStateUpdateHandlers()
	})
}

func onGuildChannelDelete(event *events.GuildChannelDelete) {
	safeDispatchFor(event, "onGuildChannelDelete", func(h *modules.EventHooks) []func(*events.GuildChannelDelete) {
		return h.GetGuildChannelDeleteHandlers()
	})
}

func onComponentInteraction(event *events.ComponentInteractionCreate) {
	// Defer the interaction update to acknowledge it (3s timeout) — UNLESS a
	// loaded module claims the interaction (RawComponentHandler), in which
	// case the module picks its own response type (e.g. a modal) and must
	// answer it itself. If deferral fails, the interaction may have already
	// been responded to, so skip dispatching to avoid double-response errors.
	if ModMgr == nil || !ModMgr.NeedsRawComponent(event) {
		if err := event.DeferUpdateMessage(); err != nil {
			Log.Warn("Failed to defer component interaction, skipping dispatch: %v", err)
			return
		}
	}
	safeDispatchFor(event, "onComponentInteraction", func(h *modules.EventHooks) []func(*events.ComponentInteractionCreate) {
		return h.GetComponentInteractionHandlers()
	})
}

func onModalSubmit(event *events.ModalSubmitInteractionCreate) {
	safeDispatchFor(event, "onModalSubmit", func(h *modules.EventHooks) []func(*events.ModalSubmitInteractionCreate) {
		return h.GetModalSubmitHandlers()
	})
}

func onSlashCommand(event *events.ApplicationCommandInteractionCreate) {
	cmdName := event.Data.CommandName()
	channelID := event.Channel().ID().String()
	guildID := ""
	if event.GuildID() != nil {
		guildID = event.GuildID().String()
	}
	user := event.User()

	var scmd *commands.SlashCommand
	for i, cmd := range commands.CoreSlashCommands {
		if cmd.Name == cmdName {
			scmd = &commands.CoreSlashCommands[i]
			break
		}
	}
	if scmd == nil {
		moduleSlashCmds := ModMgr.AllSlashCommands()
		for i, cmd := range moduleSlashCmds {
			if cmd.Name == cmdName {
				scmd = &moduleSlashCmds[i]
				break
			}
		}
	}
	if scmd == nil {
		return
	}

	// Build the positional arg vector from the declared option order (never
	// the interaction's map iteration order).
	var args []string
	if slashData, ok := event.Data.(discord.SlashCommandInteractionData); ok {
		args = commands.SlashArgs(*scmd, slashData)
	}

	// Defer the interaction FIRST: slash commands may do slow REST work
	// (e.g. cleanup's bulk deletes) and Discord only allows ~3 seconds for
	// the initial callback. All responses below go out as follow-up messages
	// on the deferred "thinking" response; error embeds are auto-deleted by
	// removing their follow-up message after the usual delay.
	if err := event.DeferCreateMessage(false); err != nil {
		Log.Error("Failed to defer slash command /%s: %v", cmdName, err)
		return
	}

	// Auto-delete: only error-colored (red) embeds vanish, after 7s. Every
	// other slash response (success/info/warning/usage/plain text) stays.
	scheduleInteractionAutoDelete := func(embeds []discord.Embed, msgID snowflake.ID) {
		if !isErrorResponse(embeds) {
			return
		}
		time.AfterFunc(errorAutoDeleteDelay, func() {
			_ = Client.Rest.DeleteFollowupMessage(event.Client().ApplicationID, event.Token(), msgID)
		})
	}

	ctx := &commands.Context{
		Bot:       ba,
		ChannelID: channelID,
		GuildID:   guildID,
		Author:    user,
		Args:      args,
		Respond: func(embeds ...discord.Embed) error {
			msg, err := Client.Rest.CreateFollowupMessage(event.Client().ApplicationID, event.Token(), discord.MessageCreate{Embeds: embeds})
			if err != nil {
				Log.Error("Slash command respond failed for /%s: %v", cmdName, err)
			} else {
				scheduleInteractionAutoDelete(embeds, msg.ID)
			}
			return err
		},
		ReplyText: func(text string) error {
			// Plain-text replies aren't error embeds, so they never auto-delete.
			_, err := Client.Rest.CreateFollowupMessage(event.Client().ApplicationID, event.Token(), discord.MessageCreate{
				Content: text,
			})
			if err != nil {
				Log.Error("Slash command reply failed for /%s: %v", cmdName, err)
			}
			return err
		},
	}

	guildOwnerID := ba.GetGuildOwnerID(guildID)
	if scmd.SuperOwnerOnly && !ba.IsOwner(user.ID.String()) {
		_ = ctx.Respond(embed.Error("🚫 Permission Denied", "You don't have permission to use this command."))
		return
	}
	if !ba.CanUse(user.ID.String(), ba.GetUserPermissions(user.ID.String(), guildID), scmd.RequiredPerm, scmd.OwnerOnly, guildOwnerID) {
		_ = ctx.Respond(embed.Error("🚫 Permission Denied", "You don't have permission to use this command."))
		return
	}

	// Per-command override enforcement: a disabled/restricted command is
	// refused here. Owner/elevated bypass (they already passed CanUse above),
	// matching the dispatcher's tiering. The dispatcher treats a nil store as
	// "allowed".
	if ov := ba.CommandOverrides(); ov != nil {
		if !ov.Allowed(scmd.Name, guildID, channelID, roleIDs(ba, guildID, user.ID.String()), isMod(ba, guildID, user.ID.String())) {
			_ = ctx.Respond(embed.Error("🚫 Command Disabled", "This command is disabled for this server."))
			return
		}
	}

	// Rate limit slash commands (owner bypasses).
	if Cfg.Bot.OwnerID != user.ID.String() {
		allowed, wait := rtLimiter.Allow(user.ID.String())
		if !allowed {
			_ = ctx.Respond(embed.Error("⏱️ Rate Limited", fmt.Sprintf("Too many commands! Wait %d seconds.", int(wait.Seconds())+1)))
			return
		}
	}

	if err := scmd.Execute(ctx); err != nil {
		Log.Error("Slash command /%s failed: %v", cmdName, err)
		_ = ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Command failed: %v", err)))
	}
}

func onMessageCreate(event *events.MessageCreate) {
	safeDispatchFor(event, "onMessageCreate", func(h *modules.EventHooks) []func(*events.MessageCreate) {
		return h.GetMessageCreateHandlers()
	})
}

var registerSlashMu sync.Mutex

func registerSlashCommands() {
	registerSlashMu.Lock()
	defer registerSlashMu.Unlock()

	// Skip if gateway is closed — prevents deadlock when re-registration
	// goroutine holds the lock while the gateway shuts down.
	if !Client.HasGateway() {
		return
	}

	// Validate every registered slash tree BEFORE the REST call. Discord
	// rejects the entire SetGlobalCommands batch with 50035 when any one
	// command is malformed (most commonly "Required options must be placed
	// before non-required options"), which would silently unregister ALL
	// slash commands. Fail loudly with the exact path instead.
	cmds := make([]discord.ApplicationCommandCreate, 0, len(commands.CoreSlashCommands)+len(ModMgr.AllSlashCommands()))
	register := func(name, description string, opts []discord.ApplicationCommandOption) {
		if errs := commands.ValidateSlashOptions(opts); len(errs) > 0 {
			for _, e := range errs {
				Log.Error("slash schema %q: %s", name, e)
			}
			Log.Error("skipping slash registration of %q (invalid schema)", name)
			return
		}
		cmds = append(cmds, discord.SlashCommandCreate{Name: name, Description: description, Options: opts})
	}
	for _, scmd := range commands.CoreSlashCommands {
		register(scmd.Name, scmd.Description, scmd.Options)
	}
	for _, scmd := range ModMgr.AllSlashCommands() {
		register(scmd.Name, scmd.Description, scmd.Options)
	}

	_, err := Client.Rest.SetGlobalCommands(Client.ApplicationID, cmds)
	if err != nil {
		Log.Error("Failed to register slash commands: %v", err)
	} else {
		Log.Info("Registered %d slash commands", len(cmds))
	}
}

func reRegisterSlashCommands() {
	go registerSlashCommands()
}

type botAdapter struct {
	voiceMgr     *modules.VoiceManager
	updater      *updater.Manager
	cmdOverrides *commands.CommandOverrides

	// Gateway capabilities of the CURRENT connection, set once the gateway is
	// open (the intents can drop on Discord's 4014, so the config alone does
	// not decide these). Read by the dashboard gating.
	memberIntent   bool
	messageContent bool
}

func (b *botAdapter) IsOwner(userID string) bool {
	return PermMgr.IsOwner(userID)
}

// MemberIntentEnabled reports whether the live gateway connection has the
// Server Members intent (see commands.Interface). False ⇒ gated mode.
func (b *botAdapter) MemberIntentEnabled() bool { return b.memberIntent }

// MessageContentEnabled reports whether the live gateway connection has the
// Message Content intent (see commands.Interface).
func (b *botAdapter) MessageContentEnabled() bool { return b.messageContent }

func (b *botAdapter) IsElevated(userID string) bool {
	return PermMgr.IsElevated(userID)
}

func (b *botAdapter) CanUse(userID string, perms discord.Permissions, requiredPerm discord.Permissions, ownerOnly bool, guildOwnerID string) bool {
	return PermMgr.CanUse(userID, perms, requiredPerm, ownerOnly, guildOwnerID)
}

func (b *botAdapter) GetUserPermissions(userID string, guildID string) discord.Permissions {
	if guildID == "" || userID == "" {
		return 0
	}
	guildSnowflake, ok := safeParseID(guildID)
	if !ok {
		return 0
	}
	userSnowflake, ok := safeParseID(userID)
	if !ok {
		return 0
	}
	member, ok := Client.Caches.Member(guildSnowflake, userSnowflake)
	if !ok {
		return 0
	}
	return Client.Caches.MemberPermissions(member)
}

func (b *botAdapter) GetGuildOwnerID(guildID string) string {
	if guildID == "" {
		return ""
	}
	guildSnowflake, ok := safeParseID(guildID)
	if !ok {
		return ""
	}
	guild, ok := Client.Caches.Guild(guildSnowflake)
	if !ok {
		return ""
	}
	return guild.OwnerID.String()
}

func (b *botAdapter) GetSelfUserID() string {
	self, ok := Client.Caches.SelfUser()
	if !ok {
		return ""
	}
	return self.ID.String()
}

func (b *botAdapter) GetName() string {
	return Cfg.Bot.Name
}

func (b *botAdapter) GetVersion() string {
	return Version
}

func (b *botAdapter) GetOwnerID() string {
	return Cfg.Bot.OwnerID
}

func (b *botAdapter) GetToS() string {
	return Cfg.Bot.ToS
}

func (b *botAdapter) GetPrivacy() string {
	return Cfg.Bot.Privacy
}

func (b *botAdapter) GetPermissionManager() *permissions.Manager {
	return PermMgr
}

func (b *botAdapter) GetCachedMember(guildID string, userID string) *discord.Member {
	if guildID == "" || userID == "" {
		return nil
	}
	gSnowflake, ok := safeParseID(guildID)
	if !ok {
		return nil
	}
	uSnowflake, ok := safeParseID(userID)
	if !ok {
		return nil
	}
	member, ok := Client.Caches.Member(gSnowflake, uSnowflake)
	if !ok {
		return nil
	}
	// Escape analysis moves `member` to the heap, so returning &member is safe.
	return &member
}

func (b *botAdapter) GetCachedGuild(guildID string) *discord.Guild {
	if guildID == "" {
		return nil
	}
	gSnowflake, ok := safeParseID(guildID)
	if !ok {
		return nil
	}
	guild, ok := Client.Caches.Guild(gSnowflake)
	if !ok {
		return nil
	}
	return &guild
}

func (b *botAdapter) GetCachedRole(guildID string, roleID string) *discord.Role {
	if guildID == "" || roleID == "" {
		return nil
	}
	gSnowflake, ok := safeParseID(guildID)
	if !ok {
		return nil
	}
	rSnowflake, ok := safeParseID(roleID)
	if !ok {
		return nil
	}
	role, ok := Client.Caches.Role(gSnowflake, rSnowflake)
	if !ok {
		return nil
	}
	return &role
}

func (b *botAdapter) GetCachedChannel(channelID string) discord.GuildChannel {
	if channelID == "" {
		return nil
	}
	cSnowflake, ok := safeParseID(channelID)
	if !ok {
		return nil
	}
	channel, ok := Client.Caches.Channel(cSnowflake)
	if !ok {
		return nil
	}
	return channel
}

func (b *botAdapter) GetMemberRoles(guildID string, userID string) []discord.Role {
	if guildID == "" || userID == "" {
		return nil
	}
	gSnowflake, ok := safeParseID(guildID)
	if !ok {
		return nil
	}
	uSnowflake, ok := safeParseID(userID)
	if !ok {
		return nil
	}
	member, ok := Client.Caches.Member(gSnowflake, uSnowflake)
	if !ok {
		return nil
	}
	return Client.Caches.MemberRoles(member)
}

func (b *botAdapter) SetConfig(key, value string) error {
	if err := Cfg.Set(key, value); err != nil {
		return err
	}
	return nil
}

func (b *botAdapter) GetConfigDir() string {
	return Dir
}

func (b *botAdapter) GetLoadedModuleNames() []string {
	return ModMgr.GetNames()
}

func (b *botAdapter) UnloadAllModules() error {
	if err := ModMgr.UnloadAll(); err != nil {
		return err
	}
	saveLoadedModules()
	return nil
}

func (b *botAdapter) GetAllModuleSlashCommandsByModule() []commands.ModuleSlashCommands {
	return ModMgr.AllSlashCommandsByModule()
}

func (b *botAdapter) GetAvailableModuleNames() []string {
	modulesDir := filepath.Join(Dir, Cfg.Modules.Path)
	var names []string
	// Lua modules: modules/Lua/<name>/<name>.lua or main.lua
	if entries, err := os.ReadDir(filepath.Join(modulesDir, "Lua")); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			luaFile := filepath.Join(modulesDir, "Lua", e.Name(), e.Name()+".lua")
			if _, err := os.Stat(luaFile); err != nil {
				luaFile = filepath.Join(modulesDir, "Lua", e.Name(), "main.lua")
			}
			if _, err := os.Stat(luaFile); err == nil {
				names = append(names, e.Name())
			}
		}
	}
	// Python modules: modules/Python/<name>/main.py
	if entries, err := os.ReadDir(filepath.Join(modulesDir, "Python")); err == nil {
		for _, e := range entries {
			if e.IsDir() && modules.IsPythonModule(filepath.Join(modulesDir, "Python", e.Name())) {
				names = append(names, e.Name())
			}
		}
	}
	// Compiled-in builtins that are currently enabled (missing enabled_modules
	// key = enabled). They appear here so consumers see the full catalog —
	// e.g. the dashboard's "Modules loaded N/M" — not just the dynamic
	// Lua/Python modules.
	for _, name := range []string{"cleanup", "tickets", "imagefilter", "welcome", "moderation"} {
		if enabled, ok := Cfg.Modules.EnabledModules[name]; !ok || enabled {
			names = append(names, name)
		}
	}
	return names
}

// IsBuiltinModule reports whether a module name is a compiled-in feature.
func (b *botAdapter) IsBuiltinModule(name string) bool {
	return name == "cleanup" || name == "tickets" || name == "welcome" || name == "moderation"
}

// SetEnabledModule persists the enabled_modules config for a builtin (applies
// on the next restart, matching the plan's "writes config, replies applies
// after restart").
func (b *botAdapter) SetEnabledModule(name string, enabled bool) error {
	key := "enabled_modules"
	if enabled {
		return b.SetConfig(key, name+"=true")
	}
	return b.SetConfig(key, name+"=false")
}

// resolveModulePath finds the actual file/directory path for a dynamic module
// by name. Only Lua and Python modules load dynamically now (feature modules
// are compiled-in): modules/Lua/<name>/<name>.lua (or main.lua),
// modules/Python/<name>/main.py.
func resolveModulePath(modulesDir, name string) (string, error) {
	// Try Lua (modules/Lua/<name>/<name>.lua, then main.lua)
	luaPath := filepath.Join(modulesDir, "Lua", name, name+".lua")
	if _, err := os.Stat(luaPath); err == nil {
		return luaPath, nil
	}
	luaMain := filepath.Join(modulesDir, "Lua", name, "main.lua")
	if _, err := os.Stat(luaMain); err == nil {
		return luaMain, nil
	}
	// Try Python (modules/Python/<name>/ directory with main.py)
	pyPath := filepath.Join(modulesDir, "Python", name)
	if modules.IsPythonModule(pyPath) {
		return pyPath, nil
	}
	return "", fmt.Errorf("module '%s' not found (tried Lua/<name>/<name>.lua, Python/<name>/main.py)", name)
}

// moduleDataDir returns the module's own folder as its data directory — the
// module script(s), configs, saves and logs all live together. Python modules
// ARE the folder; Go/Lua modules are files inside it.
func moduleDataDir(path string) string {
	if modules.IsPythonModule(path) {
		return path
	}
	return filepath.Dir(path)
}

// isBuiltinModule reports whether name is one of the compiled-in feature
// modules. Builtins come up via enabled_modules at startup; they have no
// file to load and cannot be load/unload/reloaded at runtime. They are
// listed by GetAvailableModuleNames so consumers see the full catalog —
// this guard keeps every runtime action path (Discord command, dashboard,
// MCP) from corrupting a builtin's state.
func isBuiltinModule(name string) bool {
	switch name {
	case "cleanup", "tickets", "imagefilter":
		return true
	}
	return false
}

func builtinModuleError(name string) error {
	return fmt.Errorf("module '%s' is a compiled-in builtin — enable/disable it with `modules enable|disable %s`, then restart the bot", name, name)
}

func (b *botAdapter) LoadModule(name string) error {
	if isBuiltinModule(name) {
		return builtinModuleError(name)
	}
	modulesDir := filepath.Join(Dir, Cfg.Modules.Path)
	path, err := resolveModulePath(modulesDir, name)
	if err != nil {
		return err
	}
	moduleHooks := modules.NewEventHooks()
	mod, err := ModMgr.Load(path, moduleHooks)
	if err != nil {
		return err
	}
	if err := mod.OnLoad(&modules.Context{
		BotName:      Cfg.Bot.Name,
		OwnerID:      Cfg.Bot.OwnerID,
		DataDir:      moduleDataDir(path),
		Logger:       Log,
		Rest:         Client.Rest,
		Bot:          b,
		Events:       moduleHooks,
		VoiceManager: b.voiceMgr,
		CoreConfig:   Cfg,
	}); err != nil {
		ModMgr.Unload(name)
		return err
	}
	Log.Info("Loaded module: %s v%s", mod.Name(), mod.Version())
	saveLoadedModules()
	reRegisterSlashCommands()
	return nil
}

func (b *botAdapter) UnloadModule(name string) error {
	if isBuiltinModule(name) {
		return builtinModuleError(name)
	}
	if err := ModMgr.Unload(name); err != nil {
		return err
	}
	Log.Info("Unloaded module: %s", name)
	saveLoadedModules()
	reRegisterSlashCommands()
	return nil
}

func (b *botAdapter) ReloadModule(name string) error {
	if isBuiltinModule(name) {
		return builtinModuleError(name)
	}
	if err := ModMgr.Unload(name); err != nil {
		return fmt.Errorf("unload failed: %w", err)
	}
	if err := b.LoadModule(name); err != nil {
		Log.Warn("Reload of module '%s' failed after unload — module lost until bot restart", name)
		return err
	}
	return nil
}

func (b *botAdapter) GetModuleManager() interface{} {
	return ModMgr
}

func (b *botAdapter) GetUpdater() interface{} {
	return b.updater
}

func (b *botAdapter) CommandOverrides() *commands.CommandOverrides {
	return b.cmdOverrides
}

// GetClient returns the raw disgo bot.Client as an opaque interface. This
// gives in-process modules (e.g. the dashboard) cache/gateway/rest access
// without exposing bot internals through the typed command path.
func (b *botAdapter) GetClient() interface{} {
	return Client
}

// GetStartTime returns the honest bot process start time for uptime display.
func (b *botAdapter) GetStartTime() time.Time {
	return commands.StartTime()
}

func (b *botAdapter) SetPresence(activityType string, status, text string) error {
	ctx := context.Background()
	// The presence status (online/idle/dnd/invisible) is applied independently
	// of the activity; an empty status leaves the current one untouched.
	var statusOpt gateway.PresenceOpt
	if s := strings.ToLower(strings.TrimSpace(status)); s != "" {
		switch s {
		case "online":
			statusOpt = gateway.WithOnlineStatus(discord.OnlineStatusOnline)
		case "idle":
			statusOpt = gateway.WithOnlineStatus(discord.OnlineStatusIdle)
		case "dnd":
			statusOpt = gateway.WithOnlineStatus(discord.OnlineStatusDND)
		case "invisible":
			statusOpt = gateway.WithOnlineStatus(discord.OnlineStatusInvisible)
		}
	}
	// Empty activity type means "set only the status, keep the current
	// activity" — do not clear the configured activity by sending an empty
	// Playing one.
	if strings.TrimSpace(activityType) == "" {
		if statusOpt != nil {
			return Client.SetPresence(ctx, statusOpt)
		}
		return nil
	}
	switch strings.ToLower(activityType) {
	case "playing":
		if statusOpt != nil {
			return Client.SetPresence(ctx, gateway.WithPlayingActivity(text), statusOpt)
		}
		return Client.SetPresence(ctx, gateway.WithPlayingActivity(text))
	case "watching":
		if statusOpt != nil {
			return Client.SetPresence(ctx, gateway.WithWatchingActivity(text), statusOpt)
		}
		return Client.SetPresence(ctx, gateway.WithWatchingActivity(text))
	case "listening":
		if statusOpt != nil {
			return Client.SetPresence(ctx, gateway.WithListeningActivity(text), statusOpt)
		}
		return Client.SetPresence(ctx, gateway.WithListeningActivity(text))
	case "streaming":
		if statusOpt != nil {
			return Client.SetPresence(ctx, gateway.WithStreamingActivity(text, "https://twitch.tv/"+Cfg.Bot.Name), statusOpt)
		}
		return Client.SetPresence(ctx, gateway.WithStreamingActivity(text, "https://twitch.tv/"+Cfg.Bot.Name))
	case "competing":
		if statusOpt != nil {
			return Client.SetPresence(ctx, gateway.WithCompetingActivity(text), statusOpt)
		}
		return Client.SetPresence(ctx, gateway.WithCompetingActivity(text))
	case "custom":
		if statusOpt != nil {
			return Client.SetPresence(ctx, gateway.WithCustomActivity(text), statusOpt)
		}
		return Client.SetPresence(ctx, gateway.WithCustomActivity(text))
	default:
		if statusOpt != nil {
			return Client.SetPresence(ctx, gateway.WithPlayingActivity(text), statusOpt)
		}
		return Client.SetPresence(ctx, gateway.WithPlayingActivity(text))
	}
}

func (b *botAdapter) GetLatency() string {
	if Client.HasGateway() {
		return Client.Gateway.Latency().Round(time.Millisecond).String()
	}
	return "N/A"
}

func (b *botAdapter) Shutdown() {
	select {
	case shutdownCh <- struct{}{}:
	default:
	}
}

func (b *botAdapter) Restart() {
	select {
	case restartCh <- struct{}{}:
	default:
	}
}

func (b *botAdapter) StatusRateLimit(userID string) (bool, time.Duration) {
	return rtLimiter.Status(userID)
}

func (b *botAdapter) ResetRateLimit(userID string) {
	rtLimiter.Reset(userID)
}

// ExecuteCommand runs a registered slash command (core or any loaded module)
// with a virtual context whose responses are captured instead of posted to
// Discord. This is how the web dashboard executes commands — core, Go module,
// Python module and Lua module alike (Lua routes respond/reply_text through
// the context; Python via SendCommandFromWeb).
//
// Permission mapping (mirrors the Discord dispatcher):
//   - SuperOwnerOnly → ALWAYS denied from the web
//   - OwnerOnly → the requesting user must be owner or elevated (CanUse)
//   - RequiredPerm → CanUse with the user's cached perms; with no guild
//     context, only owner/elevated pass (everyone else has 0 perms)
//
// The Discord rate limiter is intentionally NOT applied here: the web already
// has auth + CSRF + per-command permission checks, and sharing the command
// budget between Discord and the web would let Discord spam block the UI.
func (b *botAdapter) ExecuteCommand(name string, args []string, guildID, channelID, asUserID string) (commands.CommandResult, error) {
	var res commands.CommandResult
	if asUserID == "" {
		return res, fmt.Errorf("no user specified")
	}
	authorID, err := snowflake.Parse(asUserID)
	if err != nil {
		return res, fmt.Errorf("invalid user id")
	}

	// Optional channel context: commands like the cleanup module's subcommands
	// need a real channel. When provided it must exist in the cache and belong
	// to the target guild; anything else is rejected before execution.
	if channelID != "" {
		ch := b.GetCachedChannel(channelID)
		if ch == nil {
			return res, fmt.Errorf("invalid channel: %s", channelID)
		}
		if gch, ok := ch.(discord.GuildChannel); ok && gch.GuildID().String() != guildID {
			return res, fmt.Errorf("channel %s does not belong to guild %s", channelID, guildID)
		}
	}

	// Resolve the slash command: core first, then loaded modules in load order.
	type cmdInfo struct {
		superOwnerOnly, ownerOnly bool
		requiredPerm              discord.Permissions
		exec                      func(*commands.Context) error
	}
	findSlash := func(cmds []commands.SlashCommand) *cmdInfo {
		for i := range cmds {
			c := &cmds[i]
			if c.Name == name {
				return &cmdInfo{c.SuperOwnerOnly, c.OwnerOnly, c.RequiredPerm, c.Execute}
			}
		}
		return nil
	}
	ci := findSlash(commands.CoreSlashCommands)
	if ci == nil {
		ci = findSlash(ModMgr.AllSlashCommands())
	}
	if ci == nil {
		return res, fmt.Errorf("unknown command: %s", name)
	}

	// Permission gate — web execution is never granted for SuperOwnerOnly
	// commands, and everything else mirrors the Discord dispatcher's checks.
	userPerms := b.GetUserPermissions(asUserID, guildID)
	guildOwnerID := b.GetGuildOwnerID(guildID)
	if err := commands.CanExecuteWeb(
		PermMgr.IsOwner(asUserID), PermMgr.IsElevated(asUserID), asUserID == guildOwnerID,
		userPerms, ci.superOwnerOnly, ci.ownerOnly, ci.requiredPerm,
	); err != nil {
		return res, err
	}

	// Virtual context: Respond/ReplyText capture into the result instead of
	// posting to Discord. Only the first embed is captured (the web result box
	// is a single unit); the command's other side effects are its own business.
	// res is written by Respond/ReplyText, which a command may invoke from a
	// goroutine after Execute returns — guard it so the returned copy is safe.
	var resMu sync.Mutex
	ctx := &commands.Context{
		Bot:       b,
		ChannelID: channelID,
		GuildID:   guildID,
		Author:    discord.User{ID: authorID},
		Args:      args,
		Web:       true,
		Respond: func(embeds ...discord.Embed) error {
			resMu.Lock()
			defer resMu.Unlock()
			if len(embeds) > 0 {
				res.Title = embeds[0].Title
				res.Description = embeds[0].Description
				res.Color = embeds[0].Color
			}
			return nil
		},
		ReplyText: func(text string) error {
			resMu.Lock()
			defer resMu.Unlock()
			res.Text = text
			return nil
		},
	}
	if err := ci.exec(ctx); err != nil {
		resMu.Lock()
		defer resMu.Unlock()
		return res, fmt.Errorf("command failed: %v", err)
	}
	resMu.Lock()
	defer resMu.Unlock()
	return res, nil
}

func loadCoreModules(ba *botAdapter) {
	Log.Info("Registering %d core slash commands", len(commands.CoreSlashCommands))

	if noModules {
		Log.Info("Modules disabled via --no-modules flag")
		return
	}

	modulesDir := filepath.Join(Dir, Cfg.Modules.Path)

	saved := loadSavedModules()
	if len(saved) == 0 {
		if Cfg.Modules.AutoLoad {
			Log.Info("AutoLoad enabled, scanning for modules...")
			// Lua modules: modules/Lua/<name>/<name>.lua or main.lua
			if entries, err := os.ReadDir(filepath.Join(modulesDir, "Lua")); err == nil {
				for _, entry := range entries {
					if !entry.IsDir() {
						continue
					}
					name := entry.Name()
					luaFile := filepath.Join(modulesDir, "Lua", name, name+".lua")
					if _, err := os.Stat(luaFile); err != nil {
						luaFile = filepath.Join(modulesDir, "Lua", name, "main.lua")
					}
					if _, err := os.Stat(luaFile); err == nil {
						loadSingleModule(ba, modulesDir, name)
					}
				}
			}
			// Python modules: modules/Python/<name>/main.py
			if entries, err := os.ReadDir(filepath.Join(modulesDir, "Python")); err == nil {
				for _, entry := range entries {
					if entry.IsDir() && modules.IsPythonModule(filepath.Join(modulesDir, "Python", entry.Name())) {
						loadSingleModule(ba, modulesDir, entry.Name())
					}
				}
			}
			saveLoadedModules()
		} else {
			Log.Info("No previously loaded modules found")
		}
		return
	}

	for _, name := range saved {
		if _, err := resolveModulePath(modulesDir, name); err != nil {
			Log.Warn("Previously loaded module %s not found, skipping", name)
			continue
		}

		disabled := false
		for _, d := range Cfg.Modules.Disabled {
			if d == name {
				disabled = true
				break
			}
		}
		if disabled {
			Log.Info("Skipping disabled module: %s", name)
			continue
		}

		loadSingleModule(ba, modulesDir, name)
	}
}

func loadSingleModule(ba *botAdapter, modulesDir, name string) {
	path, err := resolveModulePath(modulesDir, name)
	if err != nil {
		Log.Error("Failed to resolve module %s: %v", name, err)
		return
	}
	moduleHooks := modules.NewEventHooks()
	mod, err := ModMgr.Load(path, moduleHooks)
	if err != nil {
		Log.Error("Failed to load module %s: %v", name, err)
		return
	}

	if err := mod.OnLoad(&modules.Context{
		BotName:      Cfg.Bot.Name,
		OwnerID:      Cfg.Bot.OwnerID,
		DataDir:      moduleDataDir(path),
		Logger:       Log,
		Rest:         Client.Rest,
		Bot:          ba,
		Events:       moduleHooks,
		VoiceManager: ba.voiceMgr,
		CoreConfig:   Cfg,
	}); err != nil {
		Log.Error("Failed to initialize module %s: %v", name, err)
		ModMgr.Unload(name)
		return
	}

	Log.Info("Loaded module: %s v%s", mod.Name(), mod.Version())
}

func safeParseID(s string) (snowflake.ID, bool) {
	id, err := snowflake.Parse(s)
	if err != nil {
		Log.Warn("Failed to parse snowflake ID '%s': %v", s, err)
		return 0, false
	}
	return id, true
}

// roleIDs returns the string IDs of a member's roles in a guild (empty when no
// guild context). Used by the override store's role allowlist.
func roleIDs(b *botAdapter, guildID, userID string) []string {
	if guildID == "" {
		return nil
	}
	roles := b.GetMemberRoles(guildID, userID)
	ids := make([]string, 0, len(roles))
	for _, r := range roles {
		ids = append(ids, r.ID.String())
	}
	return ids
}

// isMod reports whether a member can moderate messages in a guild (has
// ManageMessages or Administrator). The override store's mod_only rule uses it.
func isMod(b *botAdapter, guildID, userID string) bool {
	if guildID == "" {
		return false
	}
	perms := b.GetUserPermissions(userID, guildID)
	return perms.Has(discord.PermissionManageMessages) || perms.Has(discord.PermissionAdministrator)
}

func setupDirs() {
	dirs := []string{
		filepath.Join(Dir, "modules"),
		filepath.Join(Dir, "modules", "Go"),
		filepath.Join(Dir, "modules", "Python"),
		filepath.Join(Dir, "modules", "Lua"),
		filepath.Join(Dir, "logs"),
	}
	for _, d := range dirs {
		os.MkdirAll(d, 0755)
	}
}
