package modules

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
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
		cmd:    cmd,
		logger: log,
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

	if p.onReady != nil {
		p.onReady(info)
	}
}

// handleRespond handles a respond message from Python.
func (p *PythonIPC) handleRespond(message map[string]interface{}) {
	channelID := getString(message, "channel_id")
	title := getString(message, "title")
	description := getString(message, "description")

	if p.onRespond != nil {
		p.onRespond(channelID, title, description)
	}
}

// handleReplyText handles a reply_text message from Python.
func (p *PythonIPC) handleReplyText(message map[string]interface{}) {
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
