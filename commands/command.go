package commands

import (
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/misfit/bot/permissions"
)

// SlashCommand is one registered slash command. The bot exposes only slash
// commands: Discord's message-content policy forbids prefix commands, and the
// Message Content intent is no longer requested for them.
type SlashCommand struct {
	Name           string
	Description    string
	Category       string
	Usage          string // human-readable usage for /help, e.g. "help [command]"
	Options        []discord.ApplicationCommandOption
	RequiredPerm   discord.Permissions
	OwnerOnly      bool
	SuperOwnerOnly bool
	Execute        func(ctx *Context) error
}

type Context struct {
	Bot       Interface
	ChannelID string
	GuildID   string
	Author    discord.User
	Args      []string
	Web       bool // true: invoked from the web dashboard (virtual context)
	Respond   func(embeds ...discord.Embed) error
	ReplyText func(text string) error
}

// Auto-delete rule (enforced by the dispatcher in cmd/bot/main.go):
// the bot auto-deletes ONLY error-colored embeds (red, embed.ColorError) after
// 7 seconds. Every other response — success, info, warning, usage listings,
// status reports, plain text — stays on screen permanently. There is no
// per-command "preserve" opt-in because non-errors never auto-delete anyway.

// ModuleSlashCommands groups a loaded module's slash commands under the
// module's name. /help uses it to list each module's commands in a category
// named after the module (e.g. cleanup's commands appear under "Cleanup").
type ModuleSlashCommands struct {
	Name     string
	Commands []SlashCommand
}

// CommandResult is the captured outcome of a web-executed command. Respond()
// output lands in Title/Description (Color is the first embed's color), and
// ReplyText() output in Text. Everything else (Discord-only side effects) is
// the command's own business.
type CommandResult struct {
	Title       string
	Description string
	Color       int
	Text        string
}

type Interface interface {
	IsOwner(userID string) bool
	IsElevated(userID string) bool
	CanUse(userID string, perms discord.Permissions, requiredPerm discord.Permissions, ownerOnly bool, guildOwnerID string) bool
	GetUserPermissions(userID string, guildID string) discord.Permissions
	GetGuildOwnerID(guildID string) string
	GetSelfUserID() string
	GetName() string
	GetVersion() string
	GetOwnerID() string
	GetToS() string
	GetPrivacy() string
	SetConfig(key, value string) error
	GetConfigDir() string
	GetLoadedModuleNames() []string
	LoadModule(name string) error
	UnloadModule(name string) error
	ReloadModule(name string) error
	UnloadAllModules() error
	GetModuleManager() interface{}
	GetUpdater() interface{}
	// CommandOverrides returns the per-command override store (nil when the
	// feature is disabled). Enabling it is a one-liner in main(); dispatchers
	// and the dashboard read through this accessor.
	CommandOverrides() *CommandOverrides
	GetAllModuleSlashCommandsByModule() []ModuleSlashCommands // module name → its slash commands, in load order
	GetAvailableModuleNames() []string
	// IsBuiltinModule reports whether a module is a compiled-in feature
	// (cleanup/tickets) rather than a dynamic Lua/Python module.
	IsBuiltinModule(name string) bool
	// SetEnabledModule persists the enabled_modules config for a builtin.
	// Applying takes effect on the next restart.
	SetEnabledModule(name string, enabled bool) error
	GetPermissionManager() *permissions.Manager
	SetPresence(activityType string, status, text string) error
	GetLatency() string
	Shutdown()
	Restart()
	GetCachedMember(guildID, userID string) *discord.Member
	GetCachedGuild(guildID string) *discord.Guild
	GetCachedRole(guildID, roleID string) *discord.Role
	GetCachedChannel(channelID string) discord.GuildChannel
	GetMemberRoles(guildID, userID string) []discord.Role
	// MemberIntentEnabled reports whether the active gateway connection has
	// the Server Members intent. Without it member/role caches and per-guild
	// permission lookups are unavailable; the dashboard runs owner/elevated
	// only (gated mode) and permission-checked commands deny non-elevated
	// users. Reflects the ACTUAL connected intents, including an automatic
	// drop when Discord rejects a privileged intent (close code 4014).
	MemberIntentEnabled() bool
	// MessageContentEnabled reports whether the Message Content intent is
	// active. Without it guild message text/attachments are empty: content
	// features (the image filter, ticket transcripts) are unavailable; slash
	// commands are unaffected.
	MessageContentEnabled() bool
	GetClient() interface{}
	GetStartTime() time.Time
	StatusRateLimit(userID string) (allowed bool, wait time.Duration)
	ResetRateLimit(userID string)
	// ExecuteCommand runs a registered slash command (core or any loaded
	// module) with a virtual context whose responses are captured instead of
	// posted to Discord. Permission checks mirror the Discord dispatcher:
	// SuperOwnerOnly commands are ALWAYS denied from the web; OwnerOnly
	// requires the requesting user to be owner or elevated.
	// channelID is optional ("" = no channel context); when set it must be a
	// cached channel of guildID.
	ExecuteCommand(name string, args []string, guildID, channelID, asUserID string) (CommandResult, error)
}

var CoreSlashCommands []SlashCommand

func RegisterCoreSlash(cmd SlashCommand) {
	CoreSlashCommands = append(CoreSlashCommands, cmd)
}
