package modules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/misfit/bot/embed"
	"github.com/misfit/bot/logger"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

const discordAPIBase = "https://discord.com/api/v10"

const maxConcurrentRequests = 20

// PythonBridge provides the Go-side bridge for Python module communication.
// It handles IPC callbacks by sending Discord messages via Rest, routing
// log/error messages to the bot logger, and proxying API/HTTP requests.
type PythonBridge struct {
	rest       rest.Rest
	logger     *logger.Logger
	token      string
	httpClient *http.Client
	voiceMgr   *VoiceManager
	sem        chan struct{} // bounds concurrent API/HTTP goroutines
}

// NewPythonBridge creates a new Python bridge.
func NewPythonBridge(restClient rest.Rest, log *logger.Logger, token string, voiceMgr *VoiceManager) *PythonBridge {
	return &PythonBridge{
		rest:     restClient,
		logger:   log,
		token:    token,
		voiceMgr: voiceMgr,
		sem:      make(chan struct{}, maxConcurrentRequests),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// AttachCallbacks wires the IPC callbacks to the bridge's handler methods.
// API and HTTP request handlers use a semaphore to bound concurrent goroutines
// and prevent unbounded growth under heavy Python module load.
func (b *PythonBridge) AttachCallbacks(ipc *PythonIPC) {
	ipc.onRespond = b.handleRespond
	ipc.onReplyText = b.handleReplyText
	ipc.onLog = b.handleLog
	ipc.onError = b.handleError
	ipc.onAPIRequest = func(id, method, endpoint string, body interface{}) {
		b.sem <- struct{}{} // blocks if at capacity
		go func() {
			defer func() { <-b.sem }()
			b.handleAPIRequest(ipc, id, method, endpoint, body)
		}()
	}
	ipc.onHTTPRequest = func(id, method, url string, headers map[string]string, body string) {
		b.sem <- struct{}{} // blocks if at capacity
		go func() {
			defer func() { <-b.sem }()
			b.handleHTTPRequest(ipc, id, method, url, headers, body)
		}()
	}
	ipc.onVoiceAction = b.handleVoiceAction
}

// handleRespond sends an embed to a Discord channel.
func (b *PythonBridge) handleRespond(channelID, title, description string) {
	chID, err := snowflake.Parse(channelID)
	if err != nil {
		b.logger.Error("Python bridge: invalid channel ID '%s': %v", channelID, err)
		return
	}

	e := embed.Info(title, description)
	_, err = b.rest.CreateMessage(chID, discord.MessageCreate{
		Embeds: []discord.Embed{e},
	})
	if err != nil {
		b.logger.Error("Python bridge: failed to send response: %v", err)
	}
}

// handleReplyText sends plain text to a Discord channel.
func (b *PythonBridge) handleReplyText(channelID, text string) {
	chID, err := snowflake.Parse(channelID)
	if err != nil {
		b.logger.Error("Python bridge: invalid channel ID '%s': %v", channelID, err)
		return
	}

	_, err = b.rest.CreateMessage(chID, discord.MessageCreate{
		Content: text,
	})
	if err != nil {
		b.logger.Error("Python bridge: failed to send reply: %v", err)
	}
}

// handleLog routes a log message from Python to the bot logger.
func (b *PythonBridge) handleLog(level, message string) {
	switch level {
	case "debug":
		b.logger.Debug("Python: %s", message)
	case "info":
		b.logger.Info("Python: %s", message)
	case "warn":
		b.logger.Warn("Python: %s", message)
	case "error":
		b.logger.Error("Python: %s", message)
	default:
		b.logger.Info("Python: %s", message)
	}
}

// handleError routes an error message from Python to the bot logger.
func (b *PythonBridge) handleError(message string) {
	b.logger.Error("Python error: %s", message)
}

// handleAPIRequest proxies a Discord API call from Python to Discord's REST API.
// The token is held securely in Go; Python never sees it.
func (b *PythonBridge) handleAPIRequest(ipc *PythonIPC, id, method, endpoint string, body interface{}) {
	url := discordAPIBase + endpoint

	var reqBody []byte
	if body != nil {
		var err error
		reqBody, err = json.Marshal(body)
		if err != nil {
			_ = ipc.SendAPIResponse(id, nil, fmt.Sprintf("failed to marshal body: %v", err))
			return
		}
	}

	req, err := http.NewRequest(method, url, bytes.NewReader(reqBody))
	if err != nil {
		_ = ipc.SendAPIResponse(id, nil, fmt.Sprintf("failed to create request: %v", err))
		return
	}

	req.Header.Set("Authorization", "Bot "+b.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Misfit (https://github.com/misfit/bot, 1.0)")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		_ = ipc.SendAPIResponse(id, nil, fmt.Sprintf("request failed: %v", err))
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		_ = ipc.SendAPIResponse(id, nil, fmt.Sprintf("failed to read response: %v", err))
		return
	}

	// Try to parse as JSON, fall back to raw string
	var data interface{}
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &data); err != nil {
			data = string(respBody)
		}
	}

	_ = ipc.SendAPIResponse(id, data, "")
}

// handleHTTPRequest proxies a generic HTTP request from Python to any URL.
// Useful for external APIs (OpenAI, CLIP endpoints, etc.).
func (b *PythonBridge) handleHTTPRequest(ipc *PythonIPC, id, method, url string, headers map[string]string, body string) {
	var reqBody io.Reader
	if body != "" {
		reqBody = bytes.NewReader([]byte(body))
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		_ = ipc.SendHTTPResponse(id, 0, "", fmt.Sprintf("failed to create request: %v", err))
		return
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		_ = ipc.SendHTTPResponse(id, 0, "", fmt.Sprintf("request failed: %v", err))
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		_ = ipc.SendHTTPResponse(id, 0, "", fmt.Sprintf("failed to read response: %v", err))
		return
	}

	_ = ipc.SendHTTPResponse(id, resp.StatusCode, string(respBody), "")
}

// handleVoiceAction dispatches voice actions to the VoiceManager.
func (b *PythonBridge) handleVoiceAction(action string, params map[string]interface{}) (map[string]interface{}, error) {
	if b.voiceMgr == nil {
		return nil, fmt.Errorf("voice manager not available")
	}

	switch action {
	case "join":
		guildID, _ := params["guild_id"].(string)
		channelID, _ := params["channel_id"].(string)
		if guildID == "" || channelID == "" {
			return nil, fmt.Errorf("guild_id and channel_id required")
		}
		return nil, b.voiceMgr.JoinVoice(guildID, channelID)

	case "leave":
		guildID, _ := params["guild_id"].(string)
		if guildID == "" {
			return nil, fmt.Errorf("guild_id required")
		}
		return nil, b.voiceMgr.LeaveVoice(guildID)

	case "play":
		guildID, _ := params["guild_id"].(string)
		source, _ := params["source"].(string)
		if guildID == "" || source == "" {
			return nil, fmt.Errorf("guild_id and source required")
		}
		return nil, b.voiceMgr.PlayAudio(guildID, source)

	case "stop":
		guildID, _ := params["guild_id"].(string)
		if guildID == "" {
			return nil, fmt.Errorf("guild_id required")
		}
		return nil, b.voiceMgr.StopAudio(guildID)

	case "pause":
		guildID, _ := params["guild_id"].(string)
		if guildID == "" {
			return nil, fmt.Errorf("guild_id required")
		}
		return nil, b.voiceMgr.PauseAudio(guildID)

	case "resume":
		guildID, _ := params["guild_id"].(string)
		if guildID == "" {
			return nil, fmt.Errorf("guild_id required")
		}
		return nil, b.voiceMgr.ResumeAudio(guildID)

	case "set_volume":
		guildID, _ := params["guild_id"].(string)
		vol, ok := params["volume"].(float64)
		if guildID == "" || !ok {
			return nil, fmt.Errorf("guild_id and volume required")
		}
		return nil, b.voiceMgr.SetVolume(guildID, vol)

	case "set_mute":
		guildID, _ := params["guild_id"].(string)
		mute, ok := params["mute"].(bool)
		if !ok {
			mute = true
		}
		return nil, b.voiceMgr.SetMute(guildID, mute)

	case "set_deafen":
		guildID, _ := params["guild_id"].(string)
		deaf, ok := params["deafen"].(bool)
		if !ok {
			deaf = true
		}
		return nil, b.voiceMgr.SetDeafen(guildID, deaf)

	case "is_connected":
		guildID, _ := params["guild_id"].(string)
		return map[string]interface{}{"connected": b.voiceMgr.IsConnected(guildID)}, nil

	case "is_playing":
		guildID, _ := params["guild_id"].(string)
		return map[string]interface{}{"playing": b.voiceMgr.IsPlaying(guildID)}, nil

	default:
		return nil, fmt.Errorf("unknown voice action: %s", action)
	}
}
