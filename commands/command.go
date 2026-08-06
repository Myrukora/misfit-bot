package commands

import (
	"time"

	"github.com/custombot/bot/permissions"
	"github.com/disgoorg/disgo/discord"
)

type Command struct {
	Name           string
	Description    string
	Usage          string
	Category       string
	RequiredPerm   discord.Permissions
	OwnerOnly      bool
	SuperOwnerOnly bool
	Aliases        []string
	Execute        func(ctx *Context) error
}

type SlashCommand struct {
	Name           string
	Description    string
	Category       string
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
	IsSlash   bool
	MessageID string // invoking message ID (prefix commands only; "" for slash)
	Respond   func(embeds ...discord.Embed) error
	ReplyText func(text string) error
}

// Auto-delete rule (enforced by the dispatcher in cmd/bot/main.go):
// the bot auto-deletes ONLY error-colored embeds (red, embed.ColorError) after
// 7 seconds. Every other response — success, info, warning, usage listings,
// status reports, plain text — stays on screen permanently. There is no
// per-command "preserve" opt-in because non-errors never auto-delete anyway.

// ModuleCommands groups a loaded module's prefix commands under the module's
// name. [p]help uses it to list each module's commands in a category named
// after the module (e.g. cleanup's commands appear under "Cleanup").
type ModuleCommands struct {
	Name     string
	Commands []Command
}

type Interface interface {
	IsOwner(userID string) bool
	IsElevated(userID string) bool
	CanUse(userID string, perms discord.Permissions, requiredPerm discord.Permissions, ownerOnly bool, guildOwnerID string) bool
	GetUserPermissions(userID string, guildID string) discord.Permissions
	GetGuildOwnerID(guildID string) string
	GetSelfUserID() string
	GetPrefix() string
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
	GetAllModuleCommands() []Command
	GetAllModuleCommandsByModule() []ModuleCommands // module name → its prefix commands, in load order
	GetAvailableModuleNames() []string
	GetPermissionManager() *permissions.Manager
	SetPresence(activityType string, text string) error
	GetLatency() string
	Shutdown()
	Restart()
	GetCachedMember(guildID, userID string) *discord.Member
	GetCachedGuild(guildID string) *discord.Guild
	GetCachedRole(guildID, roleID string) *discord.Role
	GetCachedChannel(channelID string) discord.GuildChannel
	GetMemberRoles(guildID, userID string) []discord.Role
	GetClient() interface{}
	GetStartTime() time.Time
	StatusRateLimit(userID string) (allowed bool, wait time.Duration)
	ResetRateLimit(userID string)
}

var CoreCommands []Command
var CoreSlashCommands []SlashCommand

func RegisterCoreCommand(cmd Command) {
	CoreCommands = append(CoreCommands, cmd)
}

func RegisterCoreSlash(cmd SlashCommand) {
	CoreSlashCommands = append(CoreSlashCommands, cmd)
}
