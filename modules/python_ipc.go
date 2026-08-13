package modules

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/custombot/bot/logger"
)

// PythonIPC handles communication with a Python module process.
type PythonIPC struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	logger *logger.Logger
	mu     sync.Mutex

	// Handlers for messages from Python
	onReady     func(info PythonReadyInfo)
	onRespond   func(channelID, title, description string)
	onReplyText func(channelID, text string)
	onLog       func(level, message string)
	onError     func(message string)

	// Request/response for API proxy
	onAPIRequest  func(id, method, endpoint string, body interface{})
	onHTTPRequest func(id, method, url string, headers map[string]string, body string)

	// Voice action handler
	onVoiceAction func(action string, params map[string]interface{}) (map[string]interface{}, error)

	// Dashboard command execution: req_id → response channel. A command sent
	// with source:"dashboard" gets a req_id; the module echoes it in its
	// respond/reply_text/error replies, and deliverWeb routes the reply to the
	// waiting HTTP caller instead of Discord.
	pendingMu sync.Mutex
	pending   map[string]chan map[string]interface{}
}

// PythonReadyInfo contains the module metadata and commands from the ready message.
type PythonReadyInfo struct {
	Name          string
	Version       string
	Description   string
	Author        string
	Commands      []PythonCommand
	SlashCommands []PythonSlashCommand
	EventHandlers []string
	// Dashboard integration (optional dashboard.py): HasWebConfig is true
	// only when the module directory contains dashboard.py.
	HasWebConfig bool
	WebSchema    []PythonWebField
}

// PythonWebField is one dashboard settings field declared by dashboard.py.
type PythonWebField struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Help        string   `json:"help"`
	Type        string   `json:"type"`
	Scope       string   `json:"scope"`
	GuildScoped bool     `json:"guild_scoped"`
	Placeholder string   `json:"placeholder"`
	Options     []string `json:"options"`
	Min         *float64 `json:"min"`
	Max         *float64 `json:"max"`
	Step        *float64 `json:"step"`
}

// PythonCommand represents a command definition from Python.
type PythonCommand struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Usage       string   `json:"usage"`
	Category    string   `json:"category"`
	OwnerOnly   bool     `json:"owner_only"`
	Aliases     []string `json:"aliases"`
}

// PythonSlashCommand represents a slash command definition from Python.
type PythonSlashCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	OwnerOnly   bool   `json:"owner_only"`
}

// NewPythonIPC creates a new Python IPC handler.
func NewPythonIPC(cmd *exec.Cmd, log *logger.Logger) *PythonIPC {
	return &PythonIPC{
		cmd:     cmd,
		logger:  log,
		pending: map[string]chan map[string]interface{}{},
	}
}

// Start starts the Python process and begins reading messages.
func (p *PythonIPC) Start() error {
	var err error

	// Get stdin/stdout pipes
	p.stdin, err = p.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	p.stdout, err = p.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	p.stderr, err = p.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	// Start the process
	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start Python process: %w", err)
	}

	// Start reading messages in background
	go p.readMessages()
	go p.readStderr()

	return nil
}

// Stop stops the Python process with graceful shutdown.
// It sends a shutdown message, waits up to 5 seconds for graceful exit,
// then force kills the process if it doesn't respond.
func (p *PythonIPC) Stop() error {
	// Send shutdown message
	p.Send(map[string]interface{}{
		"type": "shutdown",
	})

	// Close stdin to signal EOF (under lock to prevent race with Send)
	p.mu.Lock()
	if p.stdin != nil {
		p.stdin.Close()
	}
	p.mu.Unlock()

	// Release pending dashboard waiters — the process is going away, so no
	// reply will ever arrive; unblock them with a clear error. Each entry is
	// claimed (removed) before being closed so a concurrent deliverWeb can
	// never send to an already-closed channel.
	p.pendingMu.Lock()
	claimed := make([]chan map[string]interface{}, 0, len(p.pending))
	for id, ch := range p.pending {
		delete(p.pending, id)
		claimed = append(claimed, ch)
	}
	p.pendingMu.Unlock()
	for _, ch := range claimed {
		close(ch)
	}

	// Wait for process to exit with timeout
	if p.cmd != nil && p.cmd.Process != nil {
		done := make(chan error, 1)
		go func() {
			done <- p.cmd.Wait()
		}()

		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			// Graceful shutdown timed out, force kill
			p.logger.Warn("Python module did not shutdown gracefully within 5s, force killing")
			if err := p.cmd.Process.Kill(); err != nil {
				return err
			}
			// Wait one more time after kill
			return p.cmd.Wait()
		}
	}
	return nil
}

// Send sends a JSON message to the Python process.
func (p *PythonIPC) Send(message map[string]interface{}) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stdin == nil {
		return fmt.Errorf("stdin is nil")
	}

	jsonBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	_, err = fmt.Fprintf(p.stdin, "%s\n", string(jsonBytes))
	if err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	return nil
}

// SendInit sends the init message to the Python process.
func (p *PythonIPC) SendInit(botName, ownerID, prefix, version, dataDir string) error {
	return p.Send(map[string]interface{}{
		"type": "init",
		"context": map[string]interface{}{
			"bot_name": botName,
			"owner_id": ownerID,
			"prefix":   prefix,
			"version":  version,
			"data_dir": dataDir,
		},
	})
}

// SendCommand sends a command invocation to the Python process.
func (p *PythonIPC) SendCommand(name string, args []string, channelID, guildID, authorID string, isSlash bool) error {
	return p.Send(map[string]interface{}{
		"type":       "command",
		"name":       name,
		"args":       args,
		"channel_id": channelID,
		"guild_id":   guildID,
		"author_id":  authorID,
		"is_slash":   isSlash,
	})
}

// SendCommandFromWeb sends a dashboard-sourced command invocation and waits
// for the module's respond/reply_text/error reply (correlated via req_id,
// 5s timeout). The reply map is returned raw so the caller can render it.
func (p *PythonIPC) SendCommandFromWeb(name string, args []string, guildID, authorID string, isSlash bool) (map[string]interface{}, error) {
	reqID := fmt.Sprintf("web-%d-%d", time.Now().UnixNano(), p.nextReqSeq())
	ch := make(chan map[string]interface{}, 4)
	p.pendingMu.Lock()
	if p.pending == nil {
		p.pending = map[string]chan map[string]interface{}{}
	}
	p.pending[reqID] = ch
	p.pendingMu.Unlock()
	defer func() {
		p.pendingMu.Lock()
		delete(p.pending, reqID)
		p.pendingMu.Unlock()
	}()

	if err := p.Send(map[string]interface{}{
		"type":       "command",
		"name":       name,
		"args":       args,
		"channel_id": "",
		"guild_id":   guildID,
		"author_id":  authorID,
		"is_slash":   isSlash,
		"source":     "dashboard",
		"req_id":     reqID,
	}); err != nil {
		return nil, err
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("python module stopped while running %q", name)
		}
		if resp["type"] == "error" {
			return nil, fmt.Errorf("%s", getString(resp, "message"))
		}
		return resp, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("python module timed out responding to %q", name)
	}
}

var webReqSeq int64

func (p *PythonIPC) nextReqSeq() int64 {
	return atomic.AddInt64(&webReqSeq, 1)
}

// SendWebGetConfig asks the module's dashboard integration for the current
// values of all schema fields (guildID "" = global scope) and waits for the
// web_config_response reply (5s timeout).
func (p *PythonIPC) SendWebGetConfig(guildID string) (map[string]string, error) {
	resp, err := p.sendWebConfig(map[string]interface{}{
		"type":     "web_get_config",
		"guild_id": guildID,
	})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if vals, ok := resp["values"].(map[string]interface{}); ok {
		for k, v := range vals {
			// The wire is string-typed; convert whatever the module returned
			// (bools/numbers are legal from Python's web_get_config).
			switch t := v.(type) {
			case string:
				out[k] = t
			case bool:
				out[k] = strconv.FormatBool(t)
			case float64:
				out[k] = strconv.FormatFloat(t, 'f', -1, 64)
			case nil:
				out[k] = ""
			default:
				out[k] = fmt.Sprint(t)
			}
		}
	}
	return out, nil
}

// SendWebSetConfig writes one field through the module's dashboard
// integration (guildID "" = global scope) and waits for the reply.
func (p *PythonIPC) SendWebSetConfig(guildID, key, value string) error {
	_, err := p.sendWebConfig(map[string]interface{}{
		"type":     "web_set_config",
		"guild_id": guildID,
		"key":      key,
		"value":    value,
	})
	return err
}

// sendWebConfig sends a dashboard-config request and waits for the correlated
// web_config_response (req_id, 5s timeout). Mirrors SendCommandFromWeb.
func (p *PythonIPC) sendWebConfig(msg map[string]interface{}) (map[string]interface{}, error) {
	reqID := fmt.Sprintf("webcfg-%d-%d", time.Now().UnixNano(), p.nextReqSeq())
	ch := make(chan map[string]interface{}, 1)
	p.pendingMu.Lock()
	if p.pending == nil {
		p.pending = map[string]chan map[string]interface{}{}
	}
	p.pending[reqID] = ch
	p.pendingMu.Unlock()
	defer func() {
		p.pendingMu.Lock()
		delete(p.pending, reqID)
		p.pendingMu.Unlock()
	}()

	msg["req_id"] = reqID
	if err := p.Send(msg); err != nil {
		return nil, err
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("python module stopped during dashboard config request")
		}
		if errStr := getString(resp, "error"); errStr != "" {
			return nil, fmt.Errorf("%s", errStr)
		}
		return resp, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("python module timed out answering dashboard config request")
	}
}

// SendEvent sends an event to the Python process.
func (p *PythonIPC) SendEvent(name string, data interface{}) error {
	return p.Send(map[string]interface{}{
		"type": "event",
		"name": name,
		"data": data,
	})
}

// SendAPIResponse sends a response to a previous API request from Python.
func (p *PythonIPC) SendAPIResponse(id string, data interface{}, errStr string) error {
	msg := map[string]interface{}{
		"type": "api_response",
		"id":   id,
	}
	if errStr != "" {
		msg["error"] = errStr
	} else {
		msg["data"] = data
	}
	return p.Send(msg)
}

// SendHTTPResponse sends a response to a previous HTTP request from Python.
func (p *PythonIPC) SendHTTPResponse(id string, statusCode int, body string, errStr string) error {
	msg := map[string]interface{}{
		"type": "http_response",
		"id":   id,
	}
	if errStr != "" {
		msg["error"] = errStr
	} else {
		msg["status"] = statusCode
		msg["body"] = body
	}
	return p.Send(msg)
}

// readMessages reads JSON messages from stdout.
func (p *PythonIPC) readMessages() {
	scanner := bufio.NewScanner(p.stdout)
	// Increase buffer size to handle large messages (default 64KB is too small)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var message map[string]interface{}
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			p.logger.Error("Failed to parse Python message: %s", err.Error())
			continue
		}

		p.handleMessage(message)
	}

	if err := scanner.Err(); err != nil {
		p.logger.Error("Python stdout read error: %s", err.Error())
	}

	// Reap the process to prevent zombie accumulation.
	// This runs after stdout is exhausted (Python process exited or stdin was closed).
	if p.cmd != nil && p.cmd.Process != nil {
		if err := p.cmd.Wait(); err != nil {
			p.logger.Debug("Python process exited: %v", err)
		}
	}
}

// readStderr reads stderr output from the Python process.
func (p *PythonIPC) readStderr() {
	scanner := bufio.NewScanner(p.stderr)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		p.logger.Error("Python stderr: %s", scanner.Text())
	}
}

// waitProcess reaps the Python process. Safe to call multiple times.
func (p *PythonIPC) waitProcess() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Wait()
}

// handleMessage handles a message from the Python process.
func (p *PythonIPC) handleMessage(message map[string]interface{}) {
	msgType, ok := message["type"].(string)
	if !ok {
		p.logger.Error("Python message missing type field")
		return
	}

	switch msgType {
	case "ready":
		p.handleReady(message)
	case "respond":
		p.handleRespond(message)
	case "reply_text":
		p.handleReplyText(message)
	case "log":
		p.handleLog(message)
	case "error":
		p.handleError(message)
	case "api_request":
		p.handleAPIRequest(message)
	case "http_request":
		p.handleHTTPRequest(message)
	case "voice":
		p.handleVoice(message)
	case "web_config_response":
		// Route to the pending dashboard-config waiter (req_id-correlated).
		p.deliverWeb(message)
	default:
		p.logger.Error("Unknown Python message type: %s", msgType)
	}
}

// handleReady handles the ready message from Python.
func (p *PythonIPC) handleReady(message map[string]interface{}) {
	info := PythonReadyInfo{
		Name:        getString(message, "name"),
		Version:     getString(message, "version"),
		Description: getString(message, "description"),
		Author:      getString(message, "author"),
	}

	// Parse commands
	if cmds, ok := message["commands"].([]interface{}); ok {
		for _, cmd := range cmds {
			if cmdMap, ok := cmd.(map[string]interface{}); ok {
				c := PythonCommand{
					Name:        getString(cmdMap, "name"),
					Description: getString(cmdMap, "description"),
					Usage:       getString(cmdMap, "usage"),
					Category:    getString(cmdMap, "category"),
					OwnerOnly:   getBool(cmdMap, "owner_only"),
				}
				if aliases, ok := cmdMap["aliases"].([]interface{}); ok {
					for _, a := range aliases {
						if alias, ok := a.(string); ok {
							c.Aliases = append(c.Aliases, alias)
						}
					}
				}
				info.Commands = append(info.Commands, c)
			}
		}
	}

	// Parse slash commands
	if cmds, ok := message["slash_commands"].([]interface{}); ok {
		for _, cmd := range cmds {
			if cmdMap, ok := cmd.(map[string]interface{}); ok {
				info.SlashCommands = append(info.SlashCommands, PythonSlashCommand{
					Name:        getString(cmdMap, "name"),
					Description: getString(cmdMap, "description"),
					Category:    getString(cmdMap, "category"),
					OwnerOnly:   getBool(cmdMap, "owner_only"),
				})
			}
		}
	}

	// Parse event handlers
	if handlers, ok := message["event_handlers"].([]interface{}); ok {
		for _, h := range handlers {
			if handler, ok := h.(string); ok {
				info.EventHandlers = append(info.EventHandlers, handler)
			}
		}
	}

	// Parse dashboard integration (optional dashboard.py).
	if has, ok := message["has_web_config"].(bool); ok {
		info.HasWebConfig = has
	}
	if fields, ok := message["web_schema"].([]interface{}); ok {
		for _, f := range fields {
			fm, ok := f.(map[string]interface{})
			if !ok {
				continue
			}
			wf := PythonWebField{
				Key:         getString(fm, "key"),
				Label:       getString(fm, "label"),
				Help:        getString(fm, "help"),
				Type:        getString(fm, "type"),
				Scope:       getString(fm, "scope"),
				GuildScoped: getBool(fm, "guild_scoped"),
				Placeholder: getString(fm, "placeholder"),
			}
			if opts, ok := fm["options"].([]interface{}); ok {
				for _, o := range opts {
					if s, ok := o.(string); ok {
						wf.Options = append(wf.Options, s)
					}
				}
			}
			for _, numKey := range []string{"min", "max", "step"} {
				if v, ok := fm[numKey].(float64); ok {
					pv := v
					switch numKey {
					case "min":
						wf.Min = &pv
					case "max":
						wf.Max = &pv
					case "step":
						wf.Step = &pv
					}
				}
			}
			info.WebSchema = append(info.WebSchema, wf)
		}
	}

	if p.onReady != nil {
		p.onReady(info)
	}
}

// deliverWeb routes a Python reply to a pending dashboard waiter when it
// carries a req_id. Returns true when the message was consumed by a waiter.
//
// The pending entry is claimed (removed) while holding pendingMu, so a
// concurrent Stop() can never close a channel this function is about to send
// to, and repeated replies after the first are ignored (unknown req_id).
func (p *PythonIPC) deliverWeb(message map[string]interface{}) bool {
	reqID := getString(message, "req_id")
	if reqID == "" {
		return false
	}
	p.pendingMu.Lock()
	ch, ok := p.pending[reqID]
	if ok {
		delete(p.pending, reqID)
	}
	p.pendingMu.Unlock()
	if !ok {
		if p.logger != nil {
			p.logger.Error("Python web reply for unknown req_id %s (module restarted?)", reqID)
		}
		return true // consumed: don't double-deliver to Discord
	}
	select {
	case ch <- message:
	default: // waiter already timed out — drop rather than block the reader
	}
	return true
}

// handleRespond handles a respond message from Python.
func (p *PythonIPC) handleRespond(message map[string]interface{}) {
	if p.deliverWeb(message) {
		return
	}
	channelID := getString(message, "channel_id")
	title := getString(message, "title")
	description := getString(message, "description")

	if p.onRespond != nil {
		p.onRespond(channelID, title, description)
	}
}

// handleReplyText handles a reply_text message from Python.
func (p *PythonIPC) handleReplyText(message map[string]interface{}) {
	if p.deliverWeb(message) {
		return
	}
	channelID := getString(message, "channel_id")
	text := getString(message, "text")

	if p.onReplyText != nil {
		p.onReplyText(channelID, text)
	}
}

// handleLog handles a log message from Python.
func (p *PythonIPC) handleLog(message map[string]interface{}) {
	level := getString(message, "level")
	msg := getString(message, "message")

	if p.onLog != nil {
		p.onLog(level, msg)
	}
}

// handleError handles an error message from Python.
func (p *PythonIPC) handleError(message map[string]interface{}) {
	if p.deliverWeb(message) {
		return
	}
	msg := getString(message, "message")

	if p.onError != nil {
		p.onError(msg)
	}
}

// handleAPIRequest handles an API request from Python.
func (p *PythonIPC) handleAPIRequest(message map[string]interface{}) {
	if p.onAPIRequest == nil {
		return
	}
	id := getString(message, "id")
	method := getString(message, "method")
	endpoint := getString(message, "endpoint")
	p.onAPIRequest(id, method, endpoint, message["body"])
}

// handleHTTPRequest handles an HTTP request from Python.
func (p *PythonIPC) handleHTTPRequest(message map[string]interface{}) {
	if p.onHTTPRequest == nil {
		return
	}
	id := getString(message, "id")
	method := getString(message, "method")
	url := getString(message, "url")

	headers := make(map[string]string)
	if h, ok := message["headers"].(map[string]interface{}); ok {
		for k, v := range h {
			if vs, ok := v.(string); ok {
				headers[k] = vs
			}
		}
	}

	body := getString(message, "body")
	p.onHTTPRequest(id, method, url, headers, body)
}

// handleVoice handles a voice action message from Python.
func (p *PythonIPC) handleVoice(message map[string]interface{}) {
	if p.onVoiceAction == nil {
		return
	}
	action := getString(message, "action")
	params, _ := message["params"].(map[string]interface{})
	if params == nil {
		params = make(map[string]interface{})
	}
	id := getString(message, "id")

	data, err := p.onVoiceAction(action, params)
	if id != "" {
		errStr := ""
		if err != nil {
			errStr = err.Error()
		}
		_ = p.SendAPIResponse(id, data, errStr)
	} else if err != nil {
		p.logger.Error("Voice action '%s' failed: %v", action, err)
	}
}

// Helper functions for extracting values from maps

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getBool(m map[string]interface{}, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}
