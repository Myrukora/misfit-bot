package modules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/custombot/bot/logger"
)

const webTestModule = `
M = {name = "webmod", version = "1.0.0", description = "web test", author = "test"}
function M.on_load(M, name) end
function M.commands(M) return {} end
function M.slash_commands(M) return {} end
`

const webTestDash = `
D = {}
D.schema = {
  {key = "enabled", label = "Enabled", type = "toggle", scope = "global", guild_scoped = false},
  {key = "tone", label = "Tone", type = "select", options = {"info", "fancy"}, scope = "guild", guild_scoped = true},
  {key = "threshold", label = "Threshold", type = "range", min = 1, max = 50, step = 1},
}
local vals = {enabled = "true", tone = "info", threshold = "10"}
D.get = function(guild_id)
  return vals
end
D.set = function(guild_id, key, value)
  if key == "tone" and value ~= "info" and value ~= "fancy" then
    return "unknown tone"
  end
  vals[key] = value
  return nil
end
`

// writeWebTestModule creates a temp module dir with (optionally) a dashboard
// script and returns the module .lua path.
func writeWebTestModule(t *testing.T, withDash bool) string {
	t.Helper()
	dir := t.TempDir()
	modPath := filepath.Join(dir, "webmod.lua")
	if err := os.WriteFile(modPath, []byte(webTestModule), 0644); err != nil {
		t.Fatalf("write module: %v", err)
	}
	if withDash {
		dashPath := filepath.Join(dir, "webmod.dashboard.lua")
		if err := os.WriteFile(dashPath, []byte(webTestDash), 0644); err != nil {
			t.Fatalf("write dashboard script: %v", err)
		}
	}
	return modPath
}

func loadWebTestModule(t *testing.T, withDash bool) (*LuaModule, error) {
	t.Helper()
	log, err := logger.New(t.TempDir(), "error", false)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { log.Close() })
	loader := NewLuaLoader(nil, log, "", nil)
	mod, err := loader.Load(writeWebTestModule(t, withDash))
	if err != nil {
		return nil, err
	}
	if err := mod.OnLoad(&Context{DataDir: t.TempDir()}); err != nil {
		return nil, err
	}
	return mod.(*LuaModule), nil
}

// TestLuaNoDashScriptNoIntegration pins "no dashboard script => no dashboard
// integration": the schema is empty and reads/writes fail cleanly.
func TestLuaNoDashScriptNoIntegration(t *testing.T) {
	mod, err := loadWebTestModule(t, false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if mod.WebConfigSchema() != nil {
		t.Fatalf("schema = %v, want nil (no dashboard script)", mod.WebConfigSchema())
	}
	if _, err := mod.WebGetConfig("123"); err == nil {
		t.Fatal("WebGetConfig without dashboard script must error")
	}
	if err := mod.WebSetConfig("123", "k", "v"); err == nil {
		t.Fatal("WebSetConfig without dashboard script must error")
	}
}

// TestLuaDashScriptRoundTrip loads a module WITH its dashboard script and
// exercises the full WebConfigurable contract: schema conversion, value
// reads, writes, and error propagation from D.set.
func TestLuaDashScriptRoundTrip(t *testing.T) {
	mod, err := loadWebTestModule(t, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	schema := mod.WebConfigSchema()
	if len(schema) != 3 {
		t.Fatalf("schema len = %d, want 3", len(schema))
	}
	if schema[0].Key != "enabled" || schema[0].Type != FieldTypeToggle || schema[0].GuildScoped {
		t.Errorf("field 0 = %+v", schema[0])
	}
	if schema[1].Key != "tone" || !schema[1].GuildScoped {
		t.Errorf("field 1 = %+v", schema[1])
	}
	if len(schema[1].Options) != 2 || schema[1].Options[1] != "fancy" {
		t.Errorf("tone options = %v", schema[1].Options)
	}
	if schema[2].Min == nil || *schema[2].Min != 1 || schema[2].Max == nil || *schema[2].Max != 50 || schema[2].Step == nil || *schema[2].Step != 1 {
		t.Errorf("threshold bounds = %+v", schema[2])
	}

	vals, err := mod.WebGetConfig("123")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if vals["enabled"] != "true" || vals["tone"] != "info" {
		t.Errorf("vals = %v", vals)
	}

	if err := mod.WebSetConfig("123", "tone", "fancy"); err != nil {
		t.Fatalf("set fancy: %v", err)
	}
	vals, _ = mod.WebGetConfig("123")
	if vals["tone"] != "fancy" {
		t.Errorf("after set, tone = %q", vals["tone"])
	}

	if err := mod.WebSetConfig("123", "tone", "bogus"); err == nil || !strings.Contains(err.Error(), "unknown tone") {
		t.Fatalf("invalid tone error = %v, want 'unknown tone'", err)
	}

	if err := mod.OnUnload(); err != nil {
		t.Fatalf("unload: %v", err)
	}
}

// TestLuaDashScriptRequiredFields pins that a dashboard script missing the
// schema fails the module load.
func TestLuaDashScriptRequiredFields(t *testing.T) {
	log, err := logger.New(t.TempDir(), "error", false)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { log.Close() })
	loader := NewLuaLoader(nil, log, "", nil)
	dir := t.TempDir()
	modPath := filepath.Join(dir, "webmod.lua")
	if err := os.WriteFile(modPath, []byte(webTestModule), 0644); err != nil {
		t.Fatalf("write module: %v", err)
	}
	// Dashboard script WITHOUT D.schema.
	if err := os.WriteFile(filepath.Join(dir, "webmod.dashboard.lua"), []byte("D = {}\n"), 0644); err != nil {
		t.Fatalf("write dash: %v", err)
	}
	mod, err := loader.Load(modPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := mod.OnLoad(&Context{DataDir: t.TempDir()}); err == nil {
		t.Fatal("OnLoad must fail when the dashboard script is broken")
	}
}

// TestIsLuaDashboardScript pins the naming rule used by every scan site.
func TestIsLuaDashboardScript(t *testing.T) {
	cases := map[string]bool{
		"hello.lua":             false,
		"hello.dashboard.lua":   true,
		"cleanup.dashboard.lua": true,
		"foo.bar.lua":           false,
	}
	for name, want := range cases {
		if got := IsLuaDashboardScript(name); got != want {
			t.Errorf("IsLuaDashboardScript(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestDiscoverLuaModulesSkipsDashboards pins that directory scanning never
// presents a dashboard integration script as a loadable module.
func TestDiscoverLuaModulesSkipsDashboards(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"mod.lua", "mod.dashboard.lua", "other.lua", "other.dashboard.lua"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("M = {}\n"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	loader := &LuaLoader{}
	found := loader.DiscoverLuaModules(dir)
	names := []string{}
	for _, m := range found {
		names = append(names, m.Name)
	}
	if len(names) != 2 || names[0] != "mod" || names[1] != "other" {
		t.Fatalf("discovered = %v, want [mod other] (dashboard scripts skipped)", names)
	}
}

// TestLuaLoaderRejectsDashScript pins the clear error when someone tries to
// load a dashboard integration script as a module.
func TestLuaLoaderRejectsDashScript(t *testing.T) {
	dir := t.TempDir()
	dashPath := filepath.Join(dir, "mod.dashboard.lua")
	if err := os.WriteFile(dashPath, []byte("D = {}\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	loader := &LuaLoader{}
	if _, err := loader.Load(dashPath); err == nil {
		t.Fatal("loading a dashboard script as a module must fail")
	}
}
