package modules

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/custombot/bot/commands"
	"github.com/custombot/bot/logger"
)

// LuaLoader handles loading and managing Lua modules.
type LuaLoader struct {
	bridge   *LuaBridge
	voiceMgr *VoiceManager
}

// NewLuaLoader creates a new Lua loader.
// token is the bot token, used by the bridge to proxy Discord API calls.
func NewLuaLoader(bot commands.Interface, log *logger.Logger, token string, voiceMgr *VoiceManager) *LuaLoader {
	return &LuaLoader{
		bridge:   NewLuaBridge(bot, log, token, voiceMgr),
		voiceMgr: voiceMgr,
	}
}

// IsLuaModule checks if the given path is a Lua module (single .lua file).
func IsLuaModule(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir() && strings.HasSuffix(info.Name(), ".lua")
}

// Load loads a Lua module from the given path.
func (l *LuaLoader) Load(path string) (Module, error) {
	// Validate the path
	if !IsLuaModule(path) {
		return nil, fmt.Errorf("not a Lua module: %s", path)
	}
	// Dashboard integration scripts are attached to their module, not
	// loadable as modules themselves.
	if IsLuaDashboardScript(filepath.Base(path)) {
		return nil, fmt.Errorf("%s is a dashboard integration script, not a module (it is loaded automatically with %s)", path, strings.TrimSuffix(filepath.Base(path), ".dashboard.lua")+".lua")
	}

	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("Lua file not found: %s", path)
	}

	// Create and return the Lua module
	mod := NewLuaModule(path, l.bridge)
	return mod, nil
}

// DiscoverLuaModules scans a directory for Lua modules.
func (l *LuaLoader) DiscoverLuaModules(dir string) []ModuleInfo {
	var modules []ModuleInfo

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".lua") {
			continue
		}
		// Dashboard integration scripts are not modules.
		if IsLuaDashboardScript(name) {
			continue
		}

		path := filepath.Join(dir, name)
		moduleName := strings.TrimSuffix(name, ".lua")

		modules = append(modules, ModuleInfo{
			Name:   moduleName,
			Type:   "lua",
			Path:   path,
			Loaded: false,
		})
	}

	return modules
}
