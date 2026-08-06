package modules

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/custombot/bot/commands"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
)

// PythonModule implements the Module interface for Python modules.
// It communicates with a Python process via IPC.
type PythonModule struct {
	name          string
	version       string
	description   string
	author        string
	path          string
	ipc           *PythonIPC
	bridge        *PythonBridge
	commands      []commands.Command
	slashCmds     []commands.SlashCommand
	eventHandlers []string
	hooks         *EventHooks
	mu            sync.Mutex
	loaded        bool
}

// NewPythonModule creates a new Python module from the ready info.
func NewPythonModule(
	path string,
	ipc *PythonIPC,
	bridge *PythonBridge,
	info PythonReadyInfo,
) *PythonModule {
	m := &PythonModule{
		name:          info.Name,
		version:       info.Version,
		description:   info.Description,
		author:        info.Author,
		path:          path,
		ipc:           ipc,
		bridge:        bridge,
		eventHandlers: info.EventHandlers,
	}

	// Fall back to directory name if module didn't provide a name
	if m.name == "" {
		m.name = filepath.Base(path)
	}

	// Convert Python commands to Go commands
	for _, pc := range info.Commands {
		pc := pc // capture loop variable
		cmd := commands.Command{
			Name:        pc.Name,
			Description: pc.Description,
			Usage:       pc.Usage,
			Category:    pc.Category,
			OwnerOnly:   pc.OwnerOnly,
			Aliases:     pc.Aliases,
			Execute:     m.makeExecute(pc.Name),
		}
		m.commands = append(m.commands, cmd)
	}

	// Convert Python slash commands to Go slash commands
	for _, psc := range info.SlashCommands {
		psc := psc // capture loop variable
		scmd := commands.SlashCommand{
			Name:        psc.Name,
			Description: psc.Description,
			Category:    psc.Category,
			OwnerOnly:   psc.OwnerOnly,
			Execute:     m.makeSlashExecute(psc.Name),
		}
		m.slashCmds = append(m.slashCmds, scmd)
	}

	return m
}

// makeExecute creates an Execute closure for a prefix command.
// The command is forwarded to the Python process via IPC.
func (m *PythonModule) makeExecute(name string) func(*commands.Context) error {
	return func(ctx *commands.Context) error {
		if ctx.Web {
			// Dashboard invocation: block until the module replies, then render
			// the response into the virtual context (captured by the web API).
			resp, err := m.ipc.SendCommandFromWeb(name, ctx.Args, ctx.GuildID, ctx.Author.ID.String())
			if err != nil {
				return err
			}
			return m.renderWebReply(ctx, resp)
		}
		return m.ipc.SendCommand(name, ctx.Args, ctx.ChannelID, ctx.GuildID, ctx.Author.ID.String(), false)
	}
}

// makeSlashExecute creates an Execute closure for a slash command.
// The command is forwarded to the Python process via IPC.
func (m *PythonModule) makeSlashExecute(name string) func(*commands.Context) error {
	return func(ctx *commands.Context) error {
		if ctx.Web {
			resp, err := m.ipc.SendCommandFromWeb(name, ctx.Args, ctx.GuildID, ctx.Author.ID.String())
			if err != nil {
				return err
			}
			return m.renderWebReply(ctx, resp)
		}
		return m.ipc.SendCommand(name, ctx.Args, ctx.ChannelID, ctx.GuildID, ctx.Author.ID.String(), true)
	}
}

// renderWebReply renders a Python respond/reply_text IPC reply into the
// virtual web context.
func (m *PythonModule) renderWebReply(ctx *commands.Context, resp map[string]interface{}) error {
	switch resp["type"] {
	case "respond":
		return ctx.Respond(discord.NewEmbed().
			WithTitle(getString(resp, "title")).
			WithDescription(getString(resp, "description")))
	case "reply_text":
		return ctx.ReplyText(getString(resp, "text"))
	default:
		return nil
	}
}

// Name returns the module name.
func (m *PythonModule) Name() string {
	return m.name
}

// Version returns the module version.
func (m *PythonModule) Version() string {
	return m.version
}

// Description returns the module description.
func (m *PythonModule) Description() string {
	return m.description
}

// Author returns the module author.
func (m *PythonModule) Author() string {
	return m.author
}

// OnLoad sends the init message to the Python process and registers event handlers.
func (m *PythonModule) OnLoad(ctx *Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.loaded {
		return fmt.Errorf("module %s already loaded", m.name)
	}

	prefix := ""
	version := ""
	if ctx.Bot != nil {
		prefix = ctx.Bot.GetPrefix()
		version = ctx.Bot.GetVersion()
	}

	if err := m.ipc.SendInit(ctx.BotName, ctx.OwnerID, prefix, version, ctx.DataDir); err != nil {
		return fmt.Errorf("failed to send init: %w", err)
	}

	// Register event handlers with the EventHooks
	m.hooks = ctx.Events
	m.registerEventHandlers(ctx.Events)

	m.loaded = true
	return nil
}

// registerEventHandlers registers Go event hooks that forward events to the Python process.
// Each event sends rich data: full author info, attachments, message IDs, guild IDs, etc.
func (m *PythonModule) registerEventHandlers(hooks *EventHooks) {
	if hooks == nil {
		return
	}

	// Build a set of requested event handler names for quick lookup
	requested := make(map[string]bool, len(m.eventHandlers))
	for _, name := range m.eventHandlers {
		requested[name] = true
	}

	// buildAuthor creates a map with full author/user info
	buildAuthor := func(user *discord.User) map[string]interface{} {
		if user == nil {
			return nil
		}
		a := map[string]interface{}{
			"id":       user.ID.String(),
			"username": user.Username,
			"bot":      user.Bot,
		}
		if user.GlobalName != nil {
			a["global_name"] = *user.GlobalName
		}
		return a
	}

	// buildAttachments creates a slice of attachment maps
	buildAttachments := func(atts []discord.Attachment) []map[string]interface{} {
		var result []map[string]interface{}
		for _, a := range atts {
			att := map[string]interface{}{
				"id":        a.ID.String(),
				"url":       a.URL,
				"proxy_url": a.ProxyURL,
				"filename":  a.Filename,
				"size":      a.Size,
			}
			if a.ContentType != nil {
				att["content_type"] = *a.ContentType
			}
			if a.Width != nil {
				att["width"] = *a.Width
			}
			if a.Height != nil {
				att["height"] = *a.Height
			}
			result = append(result, att)
		}
		return result
	}

	// Register handlers for each known event type if the module requested it
	if requested["message_create"] {
		hooks.AddMessageCreate(func(e *events.MessageCreate) {
			_ = m.ipc.SendEvent("message_create", map[string]interface{}{
				"message_id":  e.MessageID.String(),
				"channel_id":  e.ChannelID.String(),
				"author":      buildAuthor(&e.Message.Author),
				"content":     e.Message.Content,
				"attachments": buildAttachments(e.Message.Attachments),
				"timestamp":   e.Message.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
			})
		})
	}
	if requested["message_update"] {
		hooks.AddMessageUpdate(func(e *events.MessageUpdate) {
			data := map[string]interface{}{
				"message_id": e.MessageID.String(),
				"channel_id": e.ChannelID.String(),
				"content":    e.Message.Content,
			}
			if e.Message.Author.ID != 0 {
				data["author"] = buildAuthor(&e.Message.Author)
			}
			if len(e.Message.Attachments) > 0 {
				data["attachments"] = buildAttachments(e.Message.Attachments)
			}
			_ = m.ipc.SendEvent("message_update", data)
		})
	}
	if requested["message_delete"] {
		hooks.AddMessageDelete(func(e *events.MessageDelete) {
			_ = m.ipc.SendEvent("message_delete", map[string]interface{}{
				"channel_id": e.ChannelID.String(),
				"message_id": e.MessageID.String(),
			})
		})
	}
	if requested["guild_message_create"] {
		hooks.AddGuildMessageCreate(func(e *events.GuildMessageCreate) {
			_ = m.ipc.SendEvent("guild_message_create", map[string]interface{}{
				"message_id":  e.MessageID.String(),
				"guild_id":    e.GuildID.String(),
				"channel_id":  e.ChannelID.String(),
				"author":      buildAuthor(&e.Message.Author),
				"content":     e.Message.Content,
				"attachments": buildAttachments(e.Message.Attachments),
				"timestamp":   e.Message.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
			})
		})
	}
	if requested["guild_message_update"] {
		hooks.AddGuildMessageUpdate(func(e *events.GuildMessageUpdate) {
			data := map[string]interface{}{
				"message_id": e.MessageID.String(),
				"guild_id":   e.GuildID.String(),
				"channel_id": e.ChannelID.String(),
				"content":    e.Message.Content,
			}
			if e.Message.Author.ID != 0 {
				data["author"] = buildAuthor(&e.Message.Author)
			}
			if len(e.Message.Attachments) > 0 {
				data["attachments"] = buildAttachments(e.Message.Attachments)
			}
			_ = m.ipc.SendEvent("guild_message_update", data)
		})
	}
	if requested["guild_message_delete"] {
		hooks.AddGuildMessageDelete(func(e *events.GuildMessageDelete) {
			_ = m.ipc.SendEvent("guild_message_delete", map[string]interface{}{
				"guild_id":   e.GuildID.String(),
				"channel_id": e.ChannelID.String(),
				"message_id": e.MessageID.String(),
			})
		})
	}
	if requested["guild_member_join"] {
		hooks.AddGuildMemberJoin(func(e *events.GuildMemberJoin) {
			_ = m.ipc.SendEvent("guild_member_join", map[string]interface{}{
				"guild_id": e.GuildID.String(),
				"user":     buildAuthor(&e.Member.User),
				"roles": func() []string {
					ids := make([]string, len(e.Member.RoleIDs))
					for i, r := range e.Member.RoleIDs {
						ids[i] = r.String()
					}
					return ids
				}(),
			})
		})
	}
	if requested["guild_member_leave"] {
		hooks.AddGuildMemberLeave(func(e *events.GuildMemberLeave) {
			_ = m.ipc.SendEvent("guild_member_leave", map[string]interface{}{
				"guild_id": e.GuildID.String(),
				"user": map[string]interface{}{
					"id":       e.User.ID.String(),
					"username": e.User.Username,
				},
			})
		})
	}
	if requested["guild_ban"] {
		hooks.AddGuildBan(func(e *events.GuildBan) {
			_ = m.ipc.SendEvent("guild_ban", map[string]interface{}{
				"guild_id": e.GuildID.String(),
				"user":     buildAuthor(&e.User),
			})
		})
	}
	if requested["guild_unban"] {
		hooks.AddGuildUnban(func(e *events.GuildUnban) {
			_ = m.ipc.SendEvent("guild_unban", map[string]interface{}{
				"guild_id": e.GuildID.String(),
				"user":     buildAuthor(&e.User),
			})
		})
	}
	if requested["guild_join"] {
		hooks.AddGuildJoin(func(e *events.GuildJoin) {
			_ = m.ipc.SendEvent("guild_join", map[string]interface{}{
				"guild_id":     e.GuildID.String(),
				"guild_name":   e.Guild.Name,
				"owner_id":     e.Guild.OwnerID.String(),
				"member_count": e.Guild.ApproximateMemberCount,
			})
		})
	}
	if requested["guild_leave"] {
		hooks.AddGuildLeave(func(e *events.GuildLeave) {
			_ = m.ipc.SendEvent("guild_leave", map[string]interface{}{
				"guild_id":   e.GuildID.String(),
				"guild_name": e.Guild.Name,
			})
		})
	}
	if requested["presence_update"] {
		hooks.AddPresenceUpdate(func(e *events.PresenceUpdate) {
			data := map[string]interface{}{
				"user_id":  e.PresenceUser.ID.String(),
				"guild_id": e.GuildID.String(),
				"status":   string(e.Status),
			}
			if len(e.Activities) > 0 {
				activities := make([]map[string]interface{}, len(e.Activities))
				for i, act := range e.Activities {
					activities[i] = map[string]interface{}{
						"name": act.Name,
						"type": int(act.Type),
					}
				}
				data["activities"] = activities
			}
			_ = m.ipc.SendEvent("presence_update", data)
		})
	}
	if requested["message_reaction_add"] {
		hooks.AddMessageReactionAdd(func(e *events.MessageReactionAdd) {
			emoji := map[string]interface{}{
				"name": e.Emoji.Name,
			}
			if e.Emoji.ID != nil {
				emoji["id"] = e.Emoji.ID.String()
			}
			if e.Emoji.Animated {
				emoji["animated"] = true
			}
			data := map[string]interface{}{
				"channel_id": e.ChannelID.String(),
				"message_id": e.MessageID.String(),
				"user_id":    e.UserID.String(),
				"emoji":      emoji,
			}
			if e.GuildID != nil {
				data["guild_id"] = e.GuildID.String()
			}
			_ = m.ipc.SendEvent("message_reaction_add", data)
		})
	}
	if requested["voice_state_update"] {
		hooks.AddVoiceStateUpdate(func(e *events.GuildVoiceStateUpdate) {
			data := map[string]interface{}{
				"guild_id":   e.VoiceState.GuildID.String(),
				"user_id":    e.VoiceState.UserID.String(),
				"session_id": e.VoiceState.SessionID,
			}
			if e.VoiceState.ChannelID != nil {
				data["channel_id"] = e.VoiceState.ChannelID.String()
			}
			if e.Member.User.ID != 0 {
				data["member"] = map[string]interface{}{
					"user_id":  e.Member.User.ID.String(),
					"username": e.Member.User.Username,
					"bot":      e.Member.User.Bot,
				}
			}
			_ = m.ipc.SendEvent("voice_state_update", data)
		})
	}
	if requested["message_reaction_remove"] {
		hooks.AddMessageReactionRemove(func(e *events.MessageReactionRemove) {
			emoji := map[string]interface{}{
				"name": e.Emoji.Name,
			}
			if e.Emoji.ID != nil {
				emoji["id"] = e.Emoji.ID.String()
			}
			if e.Emoji.Animated {
				emoji["animated"] = true
			}
			data := map[string]interface{}{
				"channel_id": e.ChannelID.String(),
				"message_id": e.MessageID.String(),
				"user_id":    e.UserID.String(),
				"emoji":      emoji,
			}
			if e.GuildID != nil {
				data["guild_id"] = e.GuildID.String()
			}
			_ = m.ipc.SendEvent("message_reaction_remove", data)
		})
	}
	if requested["component_interaction"] {
		hooks.AddComponentInteraction(func(e *events.ComponentInteractionCreate) {
			data := map[string]interface{}{
				"custom_id":  e.Data.CustomID(),
				"channel_id": e.Channel().ID().String(),
				"user_id":    e.User().ID.String(),
			}
			if e.GuildID() != nil {
				data["guild_id"] = e.GuildID().String()
			}
			_ = m.ipc.SendEvent("component_interaction", data)
		})
	}
	if requested["modal_submit"] {
		hooks.AddModalSubmit(func(e *events.ModalSubmitInteractionCreate) {
			data := map[string]interface{}{
				"custom_id":  e.Data.CustomID,
				"channel_id": e.Channel().ID().String(),
				"user_id":    e.User().ID.String(),
			}
			if e.GuildID() != nil {
				data["guild_id"] = e.GuildID().String()
			}
			var comps []map[string]interface{}
			for _, row := range e.Data.Components {
				if ar, ok := row.(discord.ActionRowComponent); ok {
					for _, comp := range ar.Components {
						if ti, ok := comp.(discord.TextInputComponent); ok {
							comps = append(comps, map[string]interface{}{
								"custom_id": ti.CustomID,
								"value":     ti.Value,
							})
						}
					}
				}
			}
			data["components"] = comps
			_ = m.ipc.SendEvent("modal_submit", data)
		})
	}
}

// OnUnload sends the shutdown message and stops the Python process.
func (m *PythonModule) OnUnload() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.loaded {
		return nil
	}

	m.loaded = false
	_ = m.ipc.Stop()
	return nil
}

// Commands returns the module's prefix commands.
func (m *PythonModule) Commands() []commands.Command {
	return m.commands
}

// SlashCommands returns the module's slash commands.
func (m *PythonModule) SlashCommands() []commands.SlashCommand {
	return m.slashCmds
}

// Dependencies returns the module's dependencies (always empty for Python modules).
func (m *PythonModule) Dependencies() []string {
	return nil
}
