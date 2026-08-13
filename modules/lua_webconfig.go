package modules

import (
	"fmt"
	"os"
	"strings"
	"sync"

	lua "github.com/yuin/gopher-lua"
)

// ── Dashboard integration script (Lua) ────────────────────────────────────
//
// A Lua module's dashboard integration lives in a SEPARATE script next to the
// module file: `modules/<name>.dashboard.lua`. The script defines a global
// table `D` describing the module's settings panel:
//
//	D.schema          — ordered array of field tables (see fieldToConfigField)
//	D.get(guild_id)   — returns {key = value, ...} (or nil, error_string)
//	D.set(guild_id, key, value) — returns nil on success or an error string
//
// If the file does not exist the module has NO dashboard integration: the
// wrapper does not implement WebConfigurable and the dashboard shows no
// panel. The script runs in its own Lua state (separate from the module's
// command/event state) and gets the standard `ctx` table plus `ctx.data_dir`
// (the module's config directory) for persisting values.
//
// The file is deliberately NOT loadable as a module itself: every scan site
// (AutoLoad, [p]load all, GetAvailableModuleNames, DiscoverLuaModules) skips
// `*.dashboard.lua`.

// IsLuaDashboardScript reports whether a filename is a dashboard integration
// script (<name>.dashboard.lua) rather than a module.
func IsLuaDashboardScript(name string) bool {
	return strings.HasSuffix(name, ".dashboard.lua")
}

// luaWebConfig holds the dashboard script's Lua state and schema cache.
type luaWebConfig struct {
	L      *lua.LState
	mu     sync.Mutex
	schema []ConfigField
}

// dashboardScriptPath returns the dashboard script path for a module file
// (foo.lua → foo.dashboard.lua), or "" when it does not exist.
func dashboardScriptPath(modulePath string) string {
	p := strings.TrimSuffix(modulePath, ".lua") + ".dashboard.lua"
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// loadWebConfig loads the optional dashboard integration script into a fresh
// Lua state. Returns nil when no script exists (no dashboard integration).
func (m *LuaModule) loadWebConfig(ctx *Context) error {
	scriptPath := dashboardScriptPath(m.path)
	if scriptPath == "" {
		return nil
	}
	w := &luaWebConfig{}
	w.L = lua.NewState()
	m.bridge.RegisterContext(w.L)
	// The script's ctx gets the module's config directory for persistence.
	if ctxTbl, ok := w.L.GetGlobal("ctx").(*lua.LTable); ok {
		w.L.SetField(ctxTbl, "data_dir", lua.LString(ctx.DataDir))
	}
	if err := w.L.DoFile(scriptPath); err != nil {
		w.L.Close()
		return fmt.Errorf("dashboard script %s: %w", scriptPath, err)
	}
	dash := w.L.GetGlobal("D")
	dashTbl, ok := dash.(*lua.LTable)
	if !ok {
		w.L.Close()
		return fmt.Errorf("dashboard script %s must define a table 'D'", scriptPath)
	}
	if w.L.GetField(dashTbl, "schema") == lua.LNil {
		w.L.Close()
		return fmt.Errorf("dashboard script %s: D.schema is required", scriptPath)
	}
	if w.L.GetField(dashTbl, "get") == lua.LNil || w.L.GetField(dashTbl, "set") == lua.LNil {
		w.L.Close()
		return fmt.Errorf("dashboard script %s: D.get and D.set functions are required", scriptPath)
	}
	m.webCfg = w // assigned before schema conversion (webSchemaFromLua reads it)
	schema, err := m.webSchemaFromLua(dashTbl)
	if err != nil {
		w.L.Close()
		m.webCfg = nil
		return fmt.Errorf("dashboard script %s: %w", scriptPath, err)
	}
	w.schema = schema
	return nil
}

// webSchemaFromLua converts the D.schema table into []ConfigField.
func (m *LuaModule) webSchemaFromLua(dashTbl *lua.LTable) ([]ConfigField, error) {
	schemaVal := m.webCfg.L.GetField(dashTbl, "schema")
	schemaTbl, ok := schemaVal.(*lua.LTable)
	if !ok {
		return nil, fmt.Errorf("D.schema must be a table")
	}
	var out []ConfigField
	var convErr error
	schemaTbl.ForEach(func(_, v lua.LValue) {
		if convErr != nil {
			return
		}
		fTbl, ok := v.(*lua.LTable)
		if !ok {
			convErr = fmt.Errorf("D.schema entries must be tables")
			return
		}
		L := m.webCfg.L
		f := ConfigField{
			Key:         lua.LVAsString(L.GetField(fTbl, "key")),
			Label:       lua.LVAsString(L.GetField(fTbl, "label")),
			Help:        lua.LVAsString(L.GetField(fTbl, "help")),
			Type:        lua.LVAsString(L.GetField(fTbl, "type")),
			Scope:       lua.LVAsString(L.GetField(fTbl, "scope")),
			Placeholder: lua.LVAsString(L.GetField(fTbl, "placeholder")),
			GuildScoped: lua.LVAsBool(L.GetField(fTbl, "guild_scoped")),
		}
		if f.Key == "" || f.Type == "" {
			convErr = fmt.Errorf("each D.schema field needs 'key' and 'type'")
			return
		}
		if opts, ok := L.GetField(fTbl, "options").(*lua.LTable); ok {
			opts.ForEach(func(_, o lua.LValue) { f.Options = append(f.Options, lua.LVAsString(o)) })
		}
		// Bounds are presence-based: min=0 (legal for a range) must survive.
		for _, numKey := range []string{"min", "max", "step"} {
			if v := L.GetField(fTbl, numKey); v != lua.LNil {
				fv := float64(lua.LVAsNumber(v))
				switch numKey {
				case "min":
					f.Min = &fv
				case "max":
					f.Max = &fv
				case "step":
					f.Step = &fv
				}
			}
		}
		out = append(out, f)
	})
	if convErr != nil {
		return nil, convErr
	}
	return out, nil
}

// ── WebConfigurable implementation (LuaModule) ────────────────────────────

// HasWebConfig reports whether the module actually HAS a dashboard
// integration script. LuaModule always satisfies WebConfigurable at the type
// level; the dashboard checks this marker so a script-less Lua module is
// treated as not configurable at all (no panel, API writes refused).
func (m *LuaModule) HasWebConfig() bool {
	m.webMu.Lock()
	defer m.webMu.Unlock()
	return m.webCfg != nil
}

// WebConfigSchema returns the field list declared by the dashboard script.
// The dashboard only calls this when IsWebConfigurable succeeds, which
// requires the script to exist.
func (m *LuaModule) WebConfigSchema() []ConfigField {
	m.webMu.Lock()
	defer m.webMu.Unlock()
	if m.webCfg == nil {
		return nil
	}
	return m.webCfg.schema
}

// WebGetConfig reads values via the dashboard script's D.get(guild_id).
func (m *LuaModule) WebGetConfig(guildID string) (map[string]string, error) {
	m.webMu.Lock()
	defer m.webMu.Unlock()
	if m.webCfg == nil || m.webCfg.L == nil {
		return nil, fmt.Errorf("no dashboard integration")
	}
	w := m.webCfg
	w.mu.Lock()
	defer w.mu.Unlock()
	dash := w.L.GetGlobal("D")
	fn, ok := dash.(*lua.LTable)
	if !ok {
		return nil, fmt.Errorf("dashboard script missing D table")
	}
	getFn := w.L.GetField(fn, "get")
	if getFn == lua.LNil {
		return nil, fmt.Errorf("dashboard script missing D.get")
	}
	if err := w.L.CallByParam(lua.P{Fn: getFn, NRet: 2, Protect: true}, lua.LString(guildID)); err != nil {
		return nil, fmt.Errorf("D.get failed: %w", err)
	}
	defer w.L.Pop(2)
	ret := w.L.Get(-2)
	errVal := w.L.Get(-1)
	if errVal != lua.LNil {
		return nil, fmt.Errorf("%s", lua.LVAsString(errVal))
	}
	out := map[string]string{}
	if tbl, ok := ret.(*lua.LTable); ok {
		tbl.ForEach(func(k, v lua.LValue) {
			out[lua.LVAsString(k)] = lua.LVAsString(v)
		})
	}
	return out, nil
}

// WebSetConfig writes one value via the dashboard script's D.set(guild_id,
// key, value). A non-nil first return is treated as an error message.
func (m *LuaModule) WebSetConfig(guildID, key, value string) error {
	m.webMu.Lock()
	defer m.webMu.Unlock()
	if m.webCfg == nil || m.webCfg.L == nil {
		return fmt.Errorf("no dashboard integration")
	}
	w := m.webCfg
	w.mu.Lock()
	defer w.mu.Unlock()
	dash := w.L.GetGlobal("D")
	fn, ok := dash.(*lua.LTable)
	if !ok {
		return fmt.Errorf("dashboard script missing D table")
	}
	setFn := w.L.GetField(fn, "set")
	if setFn == lua.LNil {
		return fmt.Errorf("dashboard script missing D.set")
	}
	if err := w.L.CallByParam(lua.P{Fn: setFn, NRet: 1, Protect: true},
		lua.LString(guildID), lua.LString(key), lua.LString(value)); err != nil {
		return fmt.Errorf("D.set failed: %w", err)
	}
	defer w.L.Pop(1)
	if errVal := w.L.Get(-1); errVal != lua.LNil {
		return fmt.Errorf("%s", lua.LVAsString(errVal))
	}
	return nil
}

// closeWebConfig closes the dashboard script's Lua state (called on unload).
func (m *LuaModule) closeWebConfig() {
	m.webMu.Lock()
	defer m.webMu.Unlock()
	if m.webCfg != nil && m.webCfg.L != nil {
		m.webCfg.L.Close()
		m.webCfg.L = nil
	}
	m.webCfg = nil
}
