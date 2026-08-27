package commands

import (
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/misfit/bot/permissions"
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
	WebArgs        []WebArg // optional typed args for the dashboard runner
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
	Web       bool   // true: invoked from the web dashboard (virtual context)
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

// WebArg is optional structured metadata for a command's web runner form.
// Commands without WebArgs get a single free-text args input instead.
type WebArg struct {
	Name     string
	Label    string
	Type     string // text | number | toggle | select
	Required bool
	Options  []string // for select
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
	// CommandOverrides returns the per-command override store (nil when the
	// feature is disabled). Enabling it is a one-liner in main(); dispatchers
	// and the dashboard read through this accessor.
	CommandOverrides() *CommandOverrides
	GetAllModuleCommands() []Command
	GetAllModuleCommandsByModule() []ModuleCommands // module name → its prefix commands, in load order
	GetAvailableModuleNames() []string
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
	GetClient() interface{}
	GetStartTime() time.Time
	StatusRateLimit(userID string) (allowed bool, wait time.Duration)
	ResetRateLimit(userID string)
	// ExecuteCommand runs any registered command (core prefix, core slash, or
	// any loaded module's command) with a virtual context whose responses are
	// captured instead of posted to Discord. Permission checks mirror the
	// Discord dispatcher: SuperOwnerOnly commands are ALWAYS denied from the
	// web; OwnerOnly requires the requesting user to be owner or elevated.
	// channelID is optional ("" = no channel context); when set it must be a
	// cached channel of guildID. kind selects which implementation wins when
	// both prefix and slash exist for a name: ExecKindPrefix (default) or
	// ExecKindSlash; commands that only exist in the other kind still resolve.
	ExecuteCommand(name string, args []string, guildID, channelID, asUserID, kind string) (CommandResult, error)
}

// ExecKind values select which command implementation ExecuteCommand resolves
// first. Prefix mode mirrors the prefix dispatcher; slash mode prefers the
// slash implementations (falling back to prefix for commands that only exist
// in prefix form).
const (
	ExecKindPrefix = "prefix"
	ExecKindSlash  = "slash"
)

var CoreCommands []Command
var CoreSlashCommands []SlashCommand

func RegisterCoreCommand(cmd Command) {
	CoreCommands = append(CoreCommands, cmd)
}

func RegisterCoreSlash(cmd SlashCommand) {
	CoreSlashCommands = append(CoreSlashCommands, cmd)
}
