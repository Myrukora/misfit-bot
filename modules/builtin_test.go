package modules

import (
	"testing"

	"github.com/misfit/bot/commands"
)

// fakeBuiltin is a minimal Module for builtin-registration tests.
type fakeBuiltin struct {
	name string
}

func (f *fakeBuiltin) Name() string                           { return f.name }
func (f *fakeBuiltin) Version() string                        { return "1.0.0" }
func (f *fakeBuiltin) Description() string                    { return "test builtin" }
func (f *fakeBuiltin) Author() string                         { return "test" }
func (f *fakeBuiltin) OnLoad(*Context) error                  { return nil }
func (f *fakeBuiltin) OnUnload() error                        { return nil }
func (f *fakeBuiltin) Commands() []commands.Command           { return nil }
func (f *fakeBuiltin) SlashCommands() []commands.SlashCommand { return nil }
func (f *fakeBuiltin) Dependencies() []string                 { return nil }

func newFake(name string) func() Module {
	return func() Module { return &fakeBuiltin{name: name} }
}

// TestRegisterBuiltinsRegistersAll verifies that registering two constructors
// registers both modules and they are listed.
func TestRegisterBuiltinsRegistersAll(t *testing.T) {
	m := NewManager()
	m.RegisterBuiltins(newFake("cleanup"), newFake("tickets"))
	names := m.GetNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 builtins, got %d: %v", len(names), names)
	}
	if _, ok := m.Get("cleanup"); !ok {
		t.Fatal("cleanup builtin not registered")
	}
	if _, ok := m.Get("tickets"); !ok {
		t.Fatal("tickets builtin not registered")
	}
}

// TestRegisterBuiltinsEnableFilter verifies that enabled_modules: {a: false}
// skips a.
func TestRegisterBuiltinsEnableFilter(t *testing.T) {
	m := NewManager()
	enabled := map[string]bool{"cleanup": true, "tickets": false}
	m.RegisterBuiltinsWithFilter(enabled, newFake("cleanup"), newFake("tickets"))
	names := m.GetNames()
	if len(names) != 1 || names[0] != "cleanup" {
		t.Fatalf("expected only cleanup enabled, got %v", names)
	}
	if _, ok := m.Get("tickets"); ok {
		t.Fatal("tickets should be skipped when disabled")
	}
}

// TestRegisterBuiltinsNameCollision verifies that a builtin whose name
// collides with an already-loaded module is skipped (dynamic wins).
func TestRegisterBuiltinsNameCollision(t *testing.T) {
	m := NewManager()
	// Simulate a Lua module already loaded under the name "cleanup".
	m.modules["cleanup"] = &LoadedModule{Module: &fakeBuiltin{name: "cleanup"}, ModuleType: "lua"}
	m.order = append(m.order, "cleanup")
	m.RegisterBuiltins(newFake("cleanup"), newFake("tickets"))
	// cleanup should still be the Lua one (dynamic wins), tickets added.
	if got := m.modules["cleanup"].ModuleType; got != "lua" {
		t.Fatalf("builtin should not override loaded module, got type %q", got)
	}
	if _, ok := m.Get("tickets"); !ok {
		t.Fatal("tickets should still be registered")
	}
}

// fakeDisablable wraps a builtin with an OnDisable flag.
type fakeDisablable struct {
	*fakeBuiltin
	disabledCalled bool
}

func (f *fakeDisablable) OnDisable() error { f.disabledCalled = true; return nil }

// TestRegisterBuiltinsDisableHook verifies the manager calls OnDisable (not
// OnUnload) when a registered module implements Disablable.
func TestRegisterBuiltinsDisableHook(t *testing.T) {
	m := NewManager()
	mod := &fakeDisablable{fakeBuiltin: &fakeBuiltin{name: "tickets"}}
	ctor := func() Module { return mod }
	m.RegisterBuiltins(ctor)
	// Unloading a Disablable should route to OnDisable.
	if err := m.Unload("tickets"); err != nil {
		t.Fatalf("unload: %v", err)
	}
	if !mod.disabledCalled {
		t.Fatal("OnDisable not called on unload of a Disablable builtin")
	}
}
