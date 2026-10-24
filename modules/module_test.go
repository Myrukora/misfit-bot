package modules

import (
	"testing"

	"github.com/misfit/bot/commands"
)

// fakeMod is a minimal Module for Manager tests.
type fakeMod struct {
	name      string
	slashCmds []commands.SlashCommand
}

func (f *fakeMod) Name() string                           { return f.name }
func (f *fakeMod) Version() string                        { return "0" }
func (f *fakeMod) Description() string                    { return "" }
func (f *fakeMod) Author() string                         { return "" }
func (f *fakeMod) OnLoad(_ *Context) error                { return nil }
func (f *fakeMod) OnUnload() error                        { return nil }
func (f *fakeMod) SlashCommands() []commands.SlashCommand { return f.slashCmds }
func (f *fakeMod) Dependencies() []string                 { return nil }

// TestAllSlashCommandsByModule verifies that /help's grouping source groups
// each module's slash commands under the module's name, in load order, and
// skips modules that expose no commands.
func TestAllSlashCommandsByModule(t *testing.T) {
	m := &Manager{
		modules: map[string]*LoadedModule{
			"cleanup":   {Module: &fakeMod{name: "cleanup", slashCmds: []commands.SlashCommand{{Name: "cleanup"}, {Name: "purge"}}}},
			"dashboard": {Module: &fakeMod{name: "dashboard", slashCmds: []commands.SlashCommand{{Name: "dashboard"}}}},
			"silent":    {Module: &fakeMod{name: "silent"}}, // no commands — must be skipped
		},
		order: []string{"cleanup", "silent", "dashboard"},
	}

	got := m.AllSlashCommandsByModule()
	if len(got) != 2 {
		t.Fatalf("want 2 groups (silent skipped), got %d: %+v", len(got), got)
	}
	if got[0].Name != "cleanup" || len(got[0].Commands) != 2 {
		t.Errorf("group 0 = %+v, want Name=cleanup with 2 commands", got[0])
	}
	if got[1].Name != "dashboard" || len(got[1].Commands) != 1 {
		t.Errorf("group 1 = %+v, want Name=dashboard with 1 command", got[1])
	}
	// Order must follow m.order, with silent skipped (not 3 entries).
	if got[0].Name != "cleanup" || got[1].Name != "dashboard" {
		t.Errorf("load order not preserved: got %s then %s", got[0].Name, got[1].Name)
	}
}

// fakeWebMod lets a fakeModule opt into WebConfigurable while reporting
// HasWebConfig() == false, like a Lua/Python wrapper without a dashboard script.
type fakeWebMod struct {
	fakeMod
	has bool
}

func (f *fakeWebMod) WebConfigSchema() []ConfigField { return nil }
func (f *fakeWebMod) WebGetConfig(string) (map[string]string, error) {
	return nil, nil
}
func (f *fakeWebMod) WebSetConfig(string, string, string) error { return nil }
func (f *fakeWebMod) HasWebConfig() bool                        { return f.has }

func TestWebConfigForHonorsMarker(t *testing.T) {
	if _, ok := WebConfigFor(&fakeWebMod{fakeMod: fakeMod{name: "plain"}}); ok {
		t.Fatal("WebConfigFor accepted a wrapper with HasWebConfig() == false")
	}
	if _, ok := WebConfigFor(&fakeWebMod{fakeMod: fakeMod{name: "dash"}, has: true}); !ok {
		t.Fatal("WebConfigFor rejected a wrapper with a dashboard integration")
	}
	if _, ok := WebConfigFor(&fakeMod{name: "none"}); ok {
		t.Fatal("WebConfigFor accepted a non-WebConfigurable module")
	}
}

func TestBuiltinCatalog(t *testing.T) {
	if !IsBuiltin("leveling") || IsBuiltin("sample") {
		t.Fatalf("IsBuiltin catalog regression: leveling=%v sample=%v", IsBuiltin("leveling"), IsBuiltin("sample"))
	}
	names := BuiltinNames()
	if len(names) == 0 {
		t.Fatal("BuiltinNames returned an empty catalog")
	}
	names[0] = "mutated"
	if BuiltinNames()[0] == "mutated" {
		t.Fatal("BuiltinNames returned the internal slice")
	}
}
