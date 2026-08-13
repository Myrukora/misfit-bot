package modules

import (
	"fmt"
	"sync"

	"github.com/custombot/bot/commands"
	"github.com/disgoorg/disgo/events"
	lua "github.com/yuin/gopher-lua"
)

// LuaModule wraps a Lua script and implements the Module interface.
type LuaModule struct {
	name        string
	version     string
	description string
	author      string
	path        string
	L           *lua.LState
	bridge      *LuaBridge
	mu          sync.Mutex
	commands    []commands.Command
	slashCmds   []commands.SlashCommand
	// Optional dashboard integration (modules/<name>.dashboard.lua). Nil =
	// no dashboard integration; LuaModule does not implement WebConfigurable.
	webCfg *luaWebConfig
	webMu  sync.Mutex
}

// NewLuaModule creates a new Lua module wrapper.
func NewLuaModule(path string, bridge *LuaBridge) *LuaModule {
	return &LuaModule{
		path:   path,
		bridge: bridge,
	}
}

// Name returns the module name.
func (m *LuaModule) Name() string { return m.name }

// Version returns the module version.
func (m *LuaModule) Version() string { return m.version }

// Description returns the module description.
func (m *LuaModule) Description() string { return m.description }

// Author returns the module author.
func (m *LuaModule) Author() string { return m.author }

// OnLoad initializes the Lua module.
func (m *LuaModule) OnLoad(ctx *Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Create a new Lua state
	m.L = lua.NewState()

	// Register the bridge context
	m.bridge.RegisterContext(m.L)

	// Load and execute the Lua script
	if err := m.L.DoFile(m.path); err != nil {
		m.L.Close()
		return fmt.Errorf("failed to load Lua script %s: %w", m.path, err)
	}

	// Extract module metadata
	mod := m.L.GetGlobal("M")
	if mod == lua.LNil {
		m.L.Close()
		return fmt.Errorf("Lua script %s does not define module table 'M'", m.path)
	}

	modTable, ok := mod.(*lua.LTable)
	if !ok {
		m.L.Close()
		return fmt.Errorf("Lua script %s: 'M' is not a table", m.path)
	}

	// Read metadata
	m.name = lua.LVAsString(m.L.GetField(modTable, "name"))
	m.version = lua.LVAsString(m.L.GetField(modTable, "version"))
	m.description = lua.LVAsString(m.L.GetField(modTable, "description"))
	m.author = lua.LVAsString(m.L.GetField(modTable, "author"))

	if m.name == "" {
		m.L.Close()
		return fmt.Errorf("Lua script %s: M.name is required", m.path)
	}

	// Call on_load if defined
	onLoad := m.L.GetField(modTable, "on_load")
	if onLoad != lua.LNil {
		if fn, ok := onLoad.(*lua.LFunction); ok {
			if err := m.L.CallByParam(lua.P{
				Fn:      fn,
				NRet:    0,
				Protect: true,
			}, modTable, lua.LString(m.name)); err != nil {
				m.L.Close()
				return fmt.Errorf("Lua script %s: on_load failed: %w", m.path, err)
			}
		}
	}

	// Extract commands
	m.commands = m.extractCommands(modTable)

	// Extract slash commands
	m.slashCmds = m.extractSlashCommands(modTable)

	// Wire event callbacks registered via ctx.on_event during on_load
	if ctx.Events != nil {
		m.registerEventCallbacks(ctx.Events)
	}

	// Load the optional dashboard integration script (modules/<name>.dashboard.lua).
	// A missing script simply means no dashboard settings panel; a broken one
	// fails the module load so the author notices immediately.
	if err := m.loadWebConfig(ctx); err != nil {
		m.L.Close()
		return fmt.Errorf("dashboard integration: %w", err)
	}

	return nil
}

// OnUnload cleans up the Lua module.
func (m *LuaModule) OnUnload() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.L == nil {
		return nil
	}

	// Call on_unload if defined
	mod := m.L.GetGlobal("M")
	if mod != lua.LNil {
		if modTable, ok := mod.(*lua.LTable); ok {
			onUnload := m.L.GetField(modTable, "on_unload")
			if onUnload != lua.LNil {
				if fn, ok := onUnload.(*lua.LFunction); ok {
					_ = m.L.CallByParam(lua.P{
						Fn:      fn,
						NRet:    0,
						Protect: true,
					}, modTable)
				}
			}
		}
	}

	m.L.Close()
	m.L = nil
	// Close the dashboard script's state (if any).
	m.closeWebConfig()
	return nil
}

// Commands returns the module's prefix commands.
func (m *LuaModule) Commands() []commands.Command {
	return m.commands
}

// SlashCommands returns the module's slash commands.
func (m *LuaModule) SlashCommands() []commands.SlashCommand {
	return m.slashCmds
}

// Dependencies returns the module's dependencies (always empty for Lua modules).
func (m *LuaModule) Dependencies() []string {
	return nil
}

// extractCommands reads commands from the Lua module table.
func (m *LuaModule) extractCommands(modTable *lua.LTable) []commands.Command {
	commandsFn := m.L.GetField(modTable, "commands")
	if commandsFn == lua.LNil {
		return nil
	}

	fn, ok := commandsFn.(*lua.LFunction)
	if !ok {
		return nil
	}

	// Call the commands function
	if err := m.L.CallByParam(lua.P{
		Fn:      fn,
		NRet:    1,
		Protect: true,
	}, modTable); err != nil {
		return nil
	}

	// Get the returned table
	ret := m.L.Get(-1)
	m.L.Pop(1)

	cmdTable, ok := ret.(*lua.LTable)
	if !ok {
		return nil
	}

	var cmds []commands.Command

	// Iterate over the table
	cmdTable.ForEach(func(_ lua.LValue, value lua.LValue) {
		cmdDef, ok := value.(*lua.LTable)
		if !ok {
			return
		}

		cmd := commands.Command{
			Name:        lua.LVAsString(m.L.GetField(cmdDef, "name")),
			Description: lua.LVAsString(m.L.GetField(cmdDef, "description")),
			Usage:       lua.LVAsString(m.L.GetField(cmdDef, "usage")),
			Category:    lua.LVAsString(m.L.GetField(cmdDef, "category")),
		}

		// Get the execute function
		executeFn := m.L.GetField(cmdDef, "execute")
		if executeFn == lua.LNil {
			return
		}

		executeLuaFn, ok := executeFn.(*lua.LFunction)
		if !ok {
			return
		}

		// Capture the function for the closure
		L := m.L
		mod := modTable

		cmd.Execute = func(ctx *commands.Context) error {
			// gopher-lua LState is NOT thread-safe; serialize access.
			m.mu.Lock()
			defer m.mu.Unlock()

			// Register command-specific context
			m.bridge.RegisterCommandContext(L, ctx)

			// Call the Lua execute function
			if err := L.CallByParam(lua.P{
				Fn:      executeLuaFn,
				NRet:    0,
				Protect: true,
			}, mod); err != nil {
				return fmt.Errorf("Lua command %s failed: %w", cmd.Name, err)
			}
			return nil
		}

		cmds = append(cmds, cmd)
	})

	return cmds
}

// extractSlashCommands reads slash commands from the Lua module table.
func (m *LuaModule) extractSlashCommands(modTable *lua.LTable) []commands.SlashCommand {
	commandsFn := m.L.GetField(modTable, "slash_commands")
	if commandsFn == lua.LNil {
		return nil
	}

	fn, ok := commandsFn.(*lua.LFunction)
	if !ok {
		return nil
	}

	// Call the slash_commands function
	if err := m.L.CallByParam(lua.P{
		Fn:      fn,
		NRet:    1,
		Protect: true,
	}, modTable); err != nil {
		return nil
	}

	// Get the returned table
	ret := m.L.Get(-1)
	m.L.Pop(1)

	cmdTable, ok := ret.(*lua.LTable)
	if !ok {
		return nil
	}

	var cmds []commands.SlashCommand

	// Iterate over the table
	cmdTable.ForEach(func(_ lua.LValue, value lua.LValue) {
		cmdDef, ok := value.(*lua.LTable)
		if !ok {
			return
		}

		cmd := commands.SlashCommand{
			Name:        lua.LVAsString(m.L.GetField(cmdDef, "name")),
			Description: lua.LVAsString(m.L.GetField(cmdDef, "description")),
			Category:    lua.LVAsString(m.L.GetField(cmdDef, "category")),
		}

		// Get the execute function
		executeFn := m.L.GetField(cmdDef, "execute")
		if executeFn == lua.LNil {
			return
		}

		executeLuaFn, ok := executeFn.(*lua.LFunction)
		if !ok {
			return
		}

		// Capture the function for the closure
		L := m.L
		mod := modTable

		cmd.Execute = func(ctx *commands.Context) error {
			// gopher-lua LState is NOT thread-safe; serialize access.
			m.mu.Lock()
			defer m.mu.Unlock()

			// Register command-specific context
			m.bridge.RegisterCommandContext(L, ctx)

			// Call the Lua execute function
			if err := L.CallByParam(lua.P{
				Fn:      executeLuaFn,
				NRet:    0,
				Protect: true,
			}, mod); err != nil {
				return fmt.Errorf("Lua slash command %s failed: %w", cmd.Name, err)
			}
			return nil
		}

		cmds = append(cmds, cmd)
	})

	return cmds
}

// ── Event system ──────────────────────────────────────────────────

// registerEventCallbacks reads event callbacks registered via ctx.on_event()
// during on_load and wires them to the EventHooks system.
func (m *LuaModule) registerEventCallbacks(hooks *EventHooks) {
	registry := m.L.Get(lua.RegistryIndex)
	regTbl, ok := registry.(*lua.LTable)
	if !ok {
		return
	}
	cbVal := m.L.GetField(regTbl, "__event_callbacks")
	if cbVal == lua.LNil {
		return
	}
	cbTbl, ok := cbVal.(*lua.LTable)
	if !ok {
		return
	}

	cbTbl.ForEach(func(k, v lua.LValue) {
		eventName := lua.LVAsString(k)
		fn, ok := v.(*lua.LFunction)
		if !ok {
			return
		}

		switch eventName {
		case "message_create":
			hooks.AddMessageCreate(func(e *events.MessageCreate) {
				data := map[string]interface{}{
					"message_id": e.MessageID.String(),
					"channel_id": e.ChannelID.String(),
					"content":    e.Message.Content,
				}
				if e.GuildID != nil {
					data["guild_id"] = e.GuildID.String()
				}
				m.callLuaEvent(fn, data)
			})
		case "message_update":
			hooks.AddMessageUpdate(func(e *events.MessageUpdate) {
				data := map[string]interface{}{
					"message_id": e.MessageID.String(),
					"channel_id": e.ChannelID.String(),
					"content":    e.Message.Content,
				}
				if e.Message.Author.ID != 0 {
					data["author"] = map[string]interface{}{"id": e.Message.Author.ID.String()}
				}
				m.callLuaEvent(fn, data)
			})
		case "message_delete":
			hooks.AddMessageDelete(func(e *events.MessageDelete) {
				m.callLuaEvent(fn, map[string]interface{}{
					"channel_id": e.ChannelID.String(),
					"message_id": e.MessageID.String(),
				})
			})
		case "guild_message_create":
			hooks.AddGuildMessageCreate(func(e *events.GuildMessageCreate) {
				m.callLuaEvent(fn, map[string]interface{}{
					"message_id": e.MessageID.String(),
					"guild_id":   e.GuildID.String(),
					"channel_id": e.ChannelID.String(),
					"content":    e.Message.Content,
				})
			})
		case "guild_message_update":
			hooks.AddGuildMessageUpdate(func(e *events.GuildMessageUpdate) {
				data := map[string]interface{}{
					"message_id": e.MessageID.String(),
					"guild_id":   e.GuildID.String(),
					"channel_id": e.ChannelID.String(),
					"content":    e.Message.Content,
				}
				if e.Message.Author.ID != 0 {
					data["author"] = map[string]interface{}{"id": e.Message.Author.ID.String()}
				}
				m.callLuaEvent(fn, data)
			})
		case "guild_message_delete":
			hooks.AddGuildMessageDelete(func(e *events.GuildMessageDelete) {
				m.callLuaEvent(fn, map[string]interface{}{
					"guild_id":   e.GuildID.String(),
					"channel_id": e.ChannelID.String(),
					"message_id": e.MessageID.String(),
				})
			})
		case "guild_member_join":
			hooks.AddGuildMemberJoin(func(e *events.GuildMemberJoin) {
				roles := make([]string, len(e.Member.RoleIDs))
				for i, r := range e.Member.RoleIDs {
					roles[i] = r.String()
				}
				m.callLuaEvent(fn, map[string]interface{}{
					"guild_id": e.GuildID.String(),
					"user_id":  e.Member.User.ID.String(),
					"username": e.Member.User.Username,
					"roles":    roles,
				})
			})
		case "guild_member_leave":
			hooks.AddGuildMemberLeave(func(e *events.GuildMemberLeave) {
				m.callLuaEvent(fn, map[string]interface{}{
					"guild_id": e.GuildID.String(),
					"user_id":  e.User.ID.String(),
				})
			})
		case "guild_ban":
			hooks.AddGuildBan(func(e *events.GuildBan) {
				m.callLuaEvent(fn, map[string]interface{}{
					"guild_id": e.GuildID.String(),
					"user_id":  e.User.ID.String(),
				})
			})
		case "guild_unban":
			hooks.AddGuildUnban(func(e *events.GuildUnban) {
				m.callLuaEvent(fn, map[string]interface{}{
					"guild_id": e.GuildID.String(),
					"user_id":  e.User.ID.String(),
				})
			})
		case "guild_join":
			hooks.AddGuildJoin(func(e *events.GuildJoin) {
				m.callLuaEvent(fn, map[string]interface{}{
					"guild_id": e.GuildID.String(),
					"name":     e.Guild.Name,
				})
			})
		case "guild_leave":
			hooks.AddGuildLeave(func(e *events.GuildLeave) {
				m.callLuaEvent(fn, map[string]interface{}{
					"guild_id": e.GuildID.String(),
					"name":     e.Guild.Name,
				})
			})
		case "voice_state_update":
			hooks.AddVoiceStateUpdate(func(e *events.GuildVoiceStateUpdate) {
				data := map[string]interface{}{
					"guild_id": e.VoiceState.GuildID.String(),
					"user_id":  e.VoiceState.UserID.String(),
				}
				if e.VoiceState.ChannelID != nil {
					data["channel_id"] = e.VoiceState.ChannelID.String()
				}
				m.callLuaEvent(fn, data)
			})
		case "message_reaction_add":
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
				m.callLuaEvent(fn, data)
			})
		case "message_reaction_remove":
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
				m.callLuaEvent(fn, data)
			})
		case "component_interaction":
			hooks.AddComponentInteraction(func(e *events.ComponentInteractionCreate) {
				data := map[string]interface{}{
					"custom_id":  e.Data.CustomID(),
					"channel_id": e.Channel().ID().String(),
					"user_id":    e.User().ID.String(),
				}
				if e.GuildID() != nil {
					data["guild_id"] = e.GuildID().String()
				}
				m.callLuaEvent(fn, data)
			})
		case "modal_submit":
			hooks.AddModalSubmit(func(e *events.ModalSubmitInteractionCreate) {
				data := map[string]interface{}{
					"custom_id":  e.Data.CustomID,
					"channel_id": e.Channel().ID().String(),
					"user_id":    e.User().ID.String(),
				}
				if e.GuildID() != nil {
					data["guild_id"] = e.GuildID().String()
				}
				m.callLuaEvent(fn, data)
			})
		}
	})
}

// callLuaEvent safely calls a Lua event callback with the given data.
func (m *LuaModule) callLuaEvent(fn *lua.LFunction, data map[string]interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.L == nil {
		return
	}

	dataLua := goToLuaValue(m.L, m.bridge.Logger, data)
	if err := m.L.CallByParam(lua.P{
		Fn:      fn,
		NRet:    0,
		Protect: true,
	}, dataLua); err != nil {
		if m.bridge != nil && m.bridge.Logger != nil {
			m.bridge.Logger.Error("Lua event callback failed: %v", err)
		}
	}
}
