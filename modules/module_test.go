package modules

import (
	"testing"

	"github.com/misfit/bot/commands"
)

// fakeMod is a minimal Module for Manager tests.
type fakeMod struct {
	name string
	cmds []commands.Command
}

func (f *fakeMod) Name() string                           { return f.name }
func (f *fakeMod) Version() string                        { return "0" }
func (f *fakeMod) Description() string                    { return "" }
func (f *fakeMod) Author() string                         { return "" }
func (f *fakeMod) OnLoad(_ *Context) error                { return nil }
func (f *fakeMod) OnUnload() error                        { return nil }
func (f *fakeMod) Commands() []commands.Command           { return f.cmds }
func (f *fakeMod) SlashCommands() []commands.SlashCommand { return nil }
func (f *fakeMod) Dependencies() []string                 { return nil }

// TestAllCommandsByModule verifies that [p]help's grouping source groups each
// module's commands under the module's name, in load order, and skips modules
// that expose no commands.
func TestAllCommandsByModule(t *testing.T) {
	m := &Manager{
		modules: map[string]*LoadedModule{
			"cleanup":   {Module: &fakeMod{name: "cleanup", cmds: []commands.Command{{Name: "cleanup"}, {Name: "purge"}}}},
			"dashboard": {Module: &fakeMod{name: "dashboard", cmds: []commands.Command{{Name: "dashboard"}}}},
			"silent":    {Module: &fakeMod{name: "silent"}}, // no commands — must be skipped
		},
		order: []string{"cleanup", "silent", "dashboard"},
	}

	got := m.AllCommandsByModule()
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
