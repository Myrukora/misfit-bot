package modules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/logger"
	lua "github.com/yuin/gopher-lua"
)

// LuaBridge provides the Go-side context that Lua scripts can access.
type LuaBridge struct {
	Logger     *logger.Logger
	Bot        commands.Interface
	token      string
	apiBase    string
	httpClient *http.Client
	voiceMgr   *VoiceManager
}

// NewLuaBridge creates a new Lua bridge with the given bot interface and logger.
// token is used to proxy Discord API calls; it never leaves the Go process.
func NewLuaBridge(bot commands.Interface, log *logger.Logger, token string, voiceMgr *VoiceManager) *LuaBridge {
	return &LuaBridge{
		Logger:   log,
		Bot:      bot,
		token:    token,
		voiceMgr: voiceMgr,
		apiBase:  "https://discord.com/api/v10",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ── Context registration ──────────────────────────────────────────

// RegisterContext registers the ctx object and helper functions into a Lua state.
func (b *LuaBridge) RegisterContext(L *lua.LState) {
	// Create the ctx table
	ctx := L.NewTable()

	// Logging functions
	L.SetField(ctx, "log", L.NewFunction(b.luaLog))
	L.SetField(ctx, "log_debug", L.NewFunction(b.luaLogDebug))
	L.SetField(ctx, "log_warn", L.NewFunction(b.luaLogWarn))
	L.SetField(ctx, "log_error", L.NewFunction(b.luaLogError))

	// Bot info functions
	L.SetField(ctx, "get_prefix", L.NewFunction(b.luaGetPrefix))
	L.SetField(ctx, "get_name", L.NewFunction(b.luaGetName))
	L.SetField(ctx, "get_version", L.NewFunction(b.luaGetVersion))
	L.SetField(ctx, "get_owner_id", L.NewFunction(b.luaGetOwnerID))
	L.SetField(ctx, "is_owner", L.NewFunction(b.luaIsOwner))
	L.SetField(ctx, "is_elevated", L.NewFunction(b.luaIsElevated))
	L.SetField(ctx, "get_config_dir", L.NewFunction(b.luaGetConfigDir))
	L.SetField(ctx, "get_latency", L.NewFunction(b.luaGetLatency))
	L.SetField(ctx, "get_self_user_id", L.NewFunction(b.luaGetSelfUserID))

	// Discord REST API proxy (token stays in Go)
	L.SetField(ctx, "api", L.NewFunction(b.luaAPI))

	// External HTTP request proxy (via Go's HTTP client)
	L.SetField(ctx, "http", L.NewFunction(b.luaHTTP))

	// Sleep/delay
	L.SetField(ctx, "sleep", L.NewFunction(b.luaSleep))

	// Event registration (stored in registry, wired by LuaModule.OnLoad)
	L.SetField(ctx, "on_event", L.NewFunction(b.luaOnEvent))

	// Convenience wrappers for common Discord API calls
	L.SetField(ctx, "delete_message", L.NewFunction(b.luaDeleteMessage))
	L.SetField(ctx, "get_message", L.NewFunction(b.luaGetMessage))
	L.SetField(ctx, "get_channel", L.NewFunction(b.luaGetChannel))
	L.SetField(ctx, "get_guild", L.NewFunction(b.luaGetGuild))
	L.SetField(ctx, "get_member", L.NewFunction(b.luaGetMember))
	L.SetField(ctx, "ban", L.NewFunction(b.luaBan))
	L.SetField(ctx, "kick", L.NewFunction(b.luaKick))
	L.SetField(ctx, "add_role", L.NewFunction(b.luaAddRole))
	L.SetField(ctx, "remove_role", L.NewFunction(b.luaRemoveRole))

	// Voice channel functions
	L.SetField(ctx, "voice_join", L.NewFunction(b.luaVoiceJoin))
	L.SetField(ctx, "voice_leave", L.NewFunction(b.luaVoiceLeave))
	L.SetField(ctx, "voice_play", L.NewFunction(b.luaVoicePlay))
	L.SetField(ctx, "voice_stop", L.NewFunction(b.luaVoiceStop))
	L.SetField(ctx, "voice_pause", L.NewFunction(b.luaVoicePause))
	L.SetField(ctx, "voice_resume", L.NewFunction(b.luaVoiceResume))
	L.SetField(ctx, "voice_set_volume", L.NewFunction(b.luaVoiceSetVolume))
	L.SetField(ctx, "voice_set_mute", L.NewFunction(b.luaVoiceSetMute))
	L.SetField(ctx, "voice_set_deafen", L.NewFunction(b.luaVoiceSetDeafen))
	L.SetField(ctx, "voice_is_connected", L.NewFunction(b.luaVoiceIsConnected))
	L.SetField(ctx, "voice_is_playing", L.NewFunction(b.luaVoiceIsPlaying))

	// Set ctx as a global
	L.SetGlobal("ctx", ctx)
}

// ── Event registration ────────────────────────────────────────────

// luaOnEvent registers a Lua callback for a Discord gateway event.
// The callback is stored in the Lua registry table __event_callbacks
// and wired to EventHooks after on_load completes.
func (b *LuaBridge) luaOnEvent(L *lua.LState) int {
	n := L.GetTop()
	if n < 2 {
		L.Push(lua.LString("on_event requires event_name (string) and callback (function)"))
		return 1
	}
	eventName, ok := L.Get(1).(lua.LString)
	if !ok {
		L.Push(lua.LString("event_name must be a string"))
		return 1
	}
	callback, ok := L.Get(2).(*lua.LFunction)
	if !ok {
		L.Push(lua.LString("callback must be a function"))
		return 1
	}

	// Store in Lua registry: __event_callbacks[name] = callback
	registry := L.Get(lua.RegistryIndex)
	eventsTbl, ok := registry.(*lua.LTable)
	if !ok {
		return 0
	}

	callbacks := L.GetField(eventsTbl, "__event_callbacks")
	var tbl *lua.LTable
	if callbacks == lua.LNil {
		tbl = L.NewTable()
		L.SetField(eventsTbl, "__event_callbacks", tbl)
	} else if cbTable, ok := callbacks.(*lua.LTable); ok {
		tbl = cbTable
	} else {
		// Registry entry is non-nil but not a table (e.g., malicious script or corruption)
		tbl = L.NewTable()
		L.SetField(eventsTbl, "__event_callbacks", tbl)
	}
	L.SetField(tbl, string(eventName), callback)
	return 0
}

// ── Go value to Lua value conversion ──────────────────────────────

// goToLuaValue converts a Go value (from map[string]interface{}) to a Lua value.
// If logger is non-nil, unknown types are logged to help module authors debug data loss.
func goToLuaValue(L *lua.LState, logger *logger.Logger, v interface{}) lua.LValue {
	switch val := v.(type) {
	case nil:
		return lua.LNil
	case string:
		return lua.LString(val)
	case bool:
		return lua.LBool(val)
	case float64:
		return lua.LNumber(val)
	case float32:
		return lua.LNumber(float64(val))
	case int:
		return lua.LNumber(val)
	case int32:
		return lua.LNumber(int64(val))
	case int64:
		return lua.LNumber(val)
	case uint:
		return lua.LNumber(int64(val))
	case uint32:
		return lua.LNumber(int64(val))
	case uint64:
		return lua.LNumber(int64(val))
	case map[string]interface{}:
		tbl := L.NewTable()
		for k, v := range val {
			L.SetField(tbl, k, goToLuaValue(L, logger, v))
		}
		return tbl
	case []interface{}:
		tbl := L.NewTable()
		for i, v := range val {
			L.RawSetInt(tbl, i+1, goToLuaValue(L, logger, v))
		}
		return tbl
	default:
		// Log unknown types to help module authors debug data loss.
		// Returning LNil silently drops the value, which is confusing.
		if logger != nil {
			logger.Warn("goToLuaValue: unsupported Go type %T, returning nil", v)
		}
		return lua.LNil
	}
}

// ── Lua value conversion helpers ──────────────────────────────────

// luaValueToInterface converts a Lua value to a Go interface{} for JSON marshaling.
func luaValueToInterface(v lua.LValue) interface{} {
	switch val := v.(type) {
	case lua.LString:
		return string(val)
	case lua.LNumber:
		return float64(val)
	case lua.LBool:
		return bool(val)
	case *lua.LTable:
		// Detect array vs map: check if keys are sequential numbers starting at 1
		maxN := val.MaxN()
		if maxN > 0 {
			// Lua table used as array
			arr := make([]interface{}, 0, maxN)
			for i := 1; i <= maxN; i++ {
				arr = append(arr, luaValueToInterface(val.RawGetInt(i)))
			}
			return arr
		}
		// Lua table used as map
		result := make(map[string]interface{})
		val.ForEach(func(k, v lua.LValue) {
			key := lua.LVAsString(k)
			result[key] = luaValueToInterface(v)
		})
		return result
	default:
		return nil
	}
}

// luaTableToJSON converts a Lua table (or nil) to a JSON byte slice.
func luaTableToJSON(L *lua.LState, idx int) ([]byte, error) {
	val := L.Get(idx)
	if val == lua.LNil {
		return nil, nil
	}
	tbl, ok := val.(*lua.LTable)
	if !ok {
		return nil, fmt.Errorf("expected table, got %s", val.Type())
	}
	data := luaValueToInterface(tbl)
	return json.Marshal(data)
}

// apiCallJSON makes a Discord API call and returns the response as a JSON string.
func (b *LuaBridge) apiCallJSON(method, endpoint string, body []byte) (string, error) {
	url := b.apiBase + endpoint

	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return "", fmt.Errorf("request creation failed: %w", err)
	}

	req.Header.Set("Authorization", "Bot "+b.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Misfit (https://github.com/misfit/bot, 1.0)")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("response read failed: %w", err)
	}

	// Return raw JSON string — Lua can decode it with json library if available
	return string(respBody), nil
}

// ── Discord REST API proxy ────────────────────────────────────────

func (b *LuaBridge) luaAPI(L *lua.LState) int {
	method := L.CheckString(1)
	endpoint := L.CheckString(2)

	var body []byte
	if L.GetTop() >= 3 {
		var err error
		body, err = luaTableToJSON(L, 3)
		if err != nil {
			L.Push(lua.LNil)
			L.Push(lua.LString(err.Error()))
			return 2
		}
	}

	result, err := b.apiCallJSON(method, endpoint, body)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}

	L.Push(lua.LString(result))
	return 1
}

// ── External HTTP request proxy ───────────────────────────────────

func (b *LuaBridge) luaHTTP(L *lua.LState) int {
	method := L.CheckString(1)
	url := L.CheckString(2)

	var reqBody io.Reader
	if L.GetTop() >= 4 {
		body := L.CheckString(4)
		if body != "" {
			reqBody = strings.NewReader(body)
		}
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}

	// Optional headers table
	if L.GetTop() >= 3 {
		hdrTbl, ok := L.Get(3).(*lua.LTable)
		if ok {
			hdrTbl.ForEach(func(k, v lua.LValue) {
				req.Header.Set(lua.LVAsString(k), lua.LVAsString(v))
			})
		}
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}

	// Return response table: {status = code, body = "..."}
	result := L.NewTable()
	L.SetField(result, "status", lua.LNumber(resp.StatusCode))
	L.SetField(result, "body", lua.LString(string(respBody)))

	L.Push(result)
	return 1
}

// ── Sleep ─────────────────────────────────────────────────────────

func (b *LuaBridge) luaSleep(L *lua.LState) int {
	ms := L.CheckInt(1)
	time.Sleep(time.Duration(ms) * time.Millisecond)
	return 0
}

// ── Convenience wrappers ──────────────────────────────────────────

func (b *LuaBridge) luaDeleteMessage(L *lua.LState) int {
	channelID := L.CheckString(1)
	messageID := L.CheckString(2)
	result, err := b.apiCallJSON("DELETE", "/channels/"+channelID+"/messages/"+messageID, nil)
	if err != nil {
		L.Push(lua.LString(err.Error()))
		return 1
	}
	L.Push(lua.LString(result))
	return 1
}

func (b *LuaBridge) luaGetMessage(L *lua.LState) int {
	channelID := L.CheckString(1)
	messageID := L.CheckString(2)
	result, err := b.apiCallJSON("GET", "/channels/"+channelID+"/messages/"+messageID, nil)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	L.Push(lua.LString(result))
	return 1
}

func (b *LuaBridge) luaGetChannel(L *lua.LState) int {
	channelID := L.CheckString(1)
	result, err := b.apiCallJSON("GET", "/channels/"+channelID, nil)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	L.Push(lua.LString(result))
	return 1
}

func (b *LuaBridge) luaGetGuild(L *lua.LState) int {
	guildID := L.CheckString(1)
	result, err := b.apiCallJSON("GET", "/guilds/"+guildID, nil)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	L.Push(lua.LString(result))
	return 1
}

func (b *LuaBridge) luaGetMember(L *lua.LState) int {
	guildID := L.CheckString(1)
	userID := L.CheckString(2)
	result, err := b.apiCallJSON("GET", "/guilds/"+guildID+"/members/"+userID, nil)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	L.Push(lua.LString(result))
	return 1
}

func (b *LuaBridge) luaBan(L *lua.LState) int {
	guildID := L.CheckString(1)
	userID := L.CheckString(2)
	reason := L.OptString(3, "")

	var body []byte
	if reason != "" {
		body, _ = json.Marshal(map[string]string{"reason": reason})
	}

	result, err := b.apiCallJSON("PUT", "/guilds/"+guildID+"/bans/"+userID, body)
	if err != nil {
		L.Push(lua.LString(err.Error()))
		return 1
	}
	L.Push(lua.LString(result))
	return 1
}

func (b *LuaBridge) luaKick(L *lua.LState) int {
	guildID := L.CheckString(1)
	userID := L.CheckString(2)
	result, err := b.apiCallJSON("DELETE", "/guilds/"+guildID+"/members/"+userID, nil)
	if err != nil {
		L.Push(lua.LString(err.Error()))
		return 1
	}
	L.Push(lua.LString(result))
	return 1
}

func (b *LuaBridge) luaAddRole(L *lua.LState) int {
	guildID := L.CheckString(1)
	userID := L.CheckString(2)
	roleID := L.CheckString(3)

	result, err := b.apiCallJSON("PUT", "/guilds/"+guildID+"/members/"+userID+"/roles/"+roleID, nil)
	if err != nil {
		L.Push(lua.LString(err.Error()))
		return 1
	}
	L.Push(lua.LString(result))
	return 1
}

func (b *LuaBridge) luaRemoveRole(L *lua.LState) int {
	guildID := L.CheckString(1)
	userID := L.CheckString(2)
	roleID := L.CheckString(3)

	result, err := b.apiCallJSON("DELETE", "/guilds/"+guildID+"/members/"+userID+"/roles/"+roleID, nil)
	if err != nil {
		L.Push(lua.LString(err.Error()))
		return 1
	}
	L.Push(lua.LString(result))
	return 1
}

// ── Voice channel functions ──────────────────────────────────────

func voiceGuard(b *LuaBridge, L *lua.LState) error {
	if b.voiceMgr == nil {
		L.Push(lua.LString("voice manager not available"))
		return fmt.Errorf("voice manager not available")
	}
	return nil
}

func (b *LuaBridge) luaVoiceJoin(L *lua.LState) int {
	if err := voiceGuard(b, L); err != nil {
		return 1
	}
	guildID := L.CheckString(1)
	channelID := L.CheckString(2)
	if err := b.voiceMgr.JoinVoice(guildID, channelID); err != nil {
		L.Push(lua.LString(err.Error()))
		return 1
	}
	L.Push(lua.LNil)
	return 1
}

func (b *LuaBridge) luaVoiceLeave(L *lua.LState) int {
	if err := voiceGuard(b, L); err != nil {
		return 1
	}
	guildID := L.CheckString(1)
	if err := b.voiceMgr.LeaveVoice(guildID); err != nil {
		L.Push(lua.LString(err.Error()))
		return 1
	}
	L.Push(lua.LNil)
	return 1
}

func (b *LuaBridge) luaVoicePlay(L *lua.LState) int {
	if err := voiceGuard(b, L); err != nil {
		return 1
	}
	guildID := L.CheckString(1)
	source := L.CheckString(2)
	if err := b.voiceMgr.PlayAudio(guildID, source); err != nil {
		L.Push(lua.LString(err.Error()))
		return 1
	}
	L.Push(lua.LNil)
	return 1
}

func (b *LuaBridge) luaVoiceStop(L *lua.LState) int {
	if err := voiceGuard(b, L); err != nil {
		return 1
	}
	guildID := L.CheckString(1)
	if err := b.voiceMgr.StopAudio(guildID); err != nil {
		L.Push(lua.LString(err.Error()))
		return 1
	}
	L.Push(lua.LNil)
	return 1
}

func (b *LuaBridge) luaVoicePause(L *lua.LState) int {
	if err := voiceGuard(b, L); err != nil {
		return 1
	}
	guildID := L.CheckString(1)
	if err := b.voiceMgr.PauseAudio(guildID); err != nil {
		L.Push(lua.LString(err.Error()))
		return 1
	}
	L.Push(lua.LNil)
	return 1
}

func (b *LuaBridge) luaVoiceResume(L *lua.LState) int {
	if err := voiceGuard(b, L); err != nil {
		return 1
	}
	guildID := L.CheckString(1)
	if err := b.voiceMgr.ResumeAudio(guildID); err != nil {
		L.Push(lua.LString(err.Error()))
		return 1
	}
	L.Push(lua.LNil)
	return 1
}

func (b *LuaBridge) luaVoiceSetVolume(L *lua.LState) int {
	if err := voiceGuard(b, L); err != nil {
		return 1
	}
	guildID := L.CheckString(1)
	volume := L.CheckNumber(2)
	if err := b.voiceMgr.SetVolume(guildID, float64(volume)); err != nil {
		L.Push(lua.LString(err.Error()))
		return 1
	}
	L.Push(lua.LNil)
	return 1
}

func (b *LuaBridge) luaVoiceSetMute(L *lua.LState) int {
	if err := voiceGuard(b, L); err != nil {
		return 1
	}
	guildID := L.CheckString(1)
	mute := L.OptBool(2, false)
	if err := b.voiceMgr.SetMute(guildID, bool(mute)); err != nil {
		L.Push(lua.LString(err.Error()))
		return 1
	}
	L.Push(lua.LNil)
	return 1
}

func (b *LuaBridge) luaVoiceSetDeafen(L *lua.LState) int {
	if err := voiceGuard(b, L); err != nil {
		return 1
	}
	guildID := L.CheckString(1)
	deaf := L.OptBool(2, false)
	if err := b.voiceMgr.SetDeafen(guildID, bool(deaf)); err != nil {
		L.Push(lua.LString(err.Error()))
		return 1
	}
	L.Push(lua.LNil)
	return 1
}

func (b *LuaBridge) luaVoiceIsConnected(L *lua.LState) int {
	if err := voiceGuard(b, L); err != nil {
		return 1
	}
	guildID := L.CheckString(1)
	L.Push(lua.LBool(b.voiceMgr.IsConnected(guildID)))
	return 1
}

func (b *LuaBridge) luaVoiceIsPlaying(L *lua.LState) int {
	if err := voiceGuard(b, L); err != nil {
		return 1
	}
	guildID := L.CheckString(1)
	L.Push(lua.LBool(b.voiceMgr.IsPlaying(guildID)))
	return 1
}

// ── Logging functions ─────────────────────────────────────────────

func (b *LuaBridge) luaLog(L *lua.LState) int {
	msg := L.CheckString(1)
	b.Logger.Info("%s", msg)
	return 0
}

func (b *LuaBridge) luaLogDebug(L *lua.LState) int {
	msg := L.CheckString(1)
	b.Logger.Debug("%s", msg)
	return 0
}

func (b *LuaBridge) luaLogWarn(L *lua.LState) int {
	msg := L.CheckString(1)
	b.Logger.Warn("%s", msg)
	return 0
}

func (b *LuaBridge) luaLogError(L *lua.LState) int {
	msg := L.CheckString(1)
	b.Logger.Error("%s", msg)
	return 0
}

// ── Bot info functions ────────────────────────────────────────────

func (b *LuaBridge) luaGetPrefix(L *lua.LState) int {
	L.Push(lua.LString(b.Bot.GetPrefix()))
	return 1
}

func (b *LuaBridge) luaGetName(L *lua.LState) int {
	L.Push(lua.LString(b.Bot.GetName()))
	return 1
}

func (b *LuaBridge) luaGetVersion(L *lua.LState) int {
	L.Push(lua.LString(b.Bot.GetVersion()))
	return 1
}

func (b *LuaBridge) luaGetOwnerID(L *lua.LState) int {
	L.Push(lua.LString(b.Bot.GetOwnerID()))
	return 1
}

func (b *LuaBridge) luaIsOwner(L *lua.LState) int {
	userID := L.CheckString(1)
	L.Push(lua.LBool(b.Bot.IsOwner(userID)))
	return 1
}

func (b *LuaBridge) luaIsElevated(L *lua.LState) int {
	userID := L.CheckString(1)
	L.Push(lua.LBool(b.Bot.IsElevated(userID)))
	return 1
}

func (b *LuaBridge) luaGetConfigDir(L *lua.LState) int {
	L.Push(lua.LString(b.Bot.GetConfigDir()))
	return 1
}

func (b *LuaBridge) luaGetLatency(L *lua.LState) int {
	L.Push(lua.LString(b.Bot.GetLatency()))
	return 1
}

func (b *LuaBridge) luaGetSelfUserID(L *lua.LState) int {
	L.Push(lua.LString(b.Bot.GetSelfUserID()))
	return 1
}

// ── Command context registration ──────────────────────────────────

// RegisterCommandContext registers a command-specific context with respond/reply functions.
// Command-specific fields (channel_id, guild_id, author_id, is_slash, args, respond, reply_text)
// are updated each call, but any pre-existing fields on the ctx table are preserved,
// allowing stateful Lua modules to store data between commands.
func (b *LuaBridge) RegisterCommandContext(L *lua.LState, ctx *commands.Context) {
	// Get the existing ctx table or create new one. Guard against a user
	// script overwriting the global with a non-table value.
	ctxValue := L.GetGlobal("ctx")
	ctxTable, ok := ctxValue.(*lua.LTable)
	if !ok {
		ctxTable = L.NewTable()
		L.SetGlobal("ctx", ctxTable)
	}

	// Update command-specific fields (these change per command)
	L.SetField(ctxTable, "channel_id", lua.LString(ctx.ChannelID))
	L.SetField(ctxTable, "guild_id", lua.LString(ctx.GuildID))
	L.SetField(ctxTable, "author_id", lua.LString(ctx.Author.ID.String()))
	L.SetField(ctxTable, "is_slash", lua.LBool(ctx.IsSlash))

	// Update args (refreshed each command)
	argsTable := L.NewTable()
	for i, arg := range ctx.Args {
		L.RawSet(argsTable, lua.LNumber(i+1), lua.LString(arg))
	}
	L.SetField(ctxTable, "args", argsTable)

	// Update respond function (re-bound to current ctx)
	L.SetField(ctxTable, "respond", L.NewFunction(func(L *lua.LState) int {
		title := L.CheckString(1)
		description := L.OptString(2, "")
		embed := luaEmbed(title, description)
		if err := ctx.Respond(embed); err != nil {
			L.Push(lua.LString(err.Error()))
			return 1
		}
		L.Push(lua.LNil)
		return 1
	}))

	// Update reply_text function (re-bound to current ctx)
	L.SetField(ctxTable, "reply_text", L.NewFunction(func(L *lua.LState) int {
		text := L.CheckString(1)
		if err := ctx.ReplyText(text); err != nil {
			L.Push(lua.LString(err.Error()))
			return 1
		}
		L.Push(lua.LNil)
		return 1
	}))
}
