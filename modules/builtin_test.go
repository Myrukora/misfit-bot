package modules

import (
	"fmt"
	"path/filepath"
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
	ctx := &Context{DataDir: t.TempDir()}
	if err := m.RegisterBuiltins(ctx, newFake("cleanup"), newFake("tickets")); err != nil {
		t.Fatalf("register: %v", err)
	}
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
// skips a, while an absent key leaves the builtin enabled.
func TestRegisterBuiltinsEnableFilter(t *testing.T) {
	m := NewManager()
	ctx := &Context{DataDir: t.TempDir()}
	enabled := map[string]bool{"cleanup": true, "tickets": false}
	if err := m.RegisterBuiltinsWithFilter(enabled, ctx, newFake("cleanup"), newFake("tickets")); err != nil {
		t.Fatalf("register: %v", err)
	}
	names := m.GetNames()
	if len(names) != 1 || names[0] != "cleanup" {
		t.Fatalf("expected only cleanup enabled, got %v", names)
	}
	if _, ok := m.Get("tickets"); ok {
		t.Fatal("tickets should be skipped when disabled")
	}
}

// TestRegisterBuiltinsAbsentKeyEnabled verifies that a builtin whose name is
// NOT present in the enabled_modules map is still registered (missing key =
// enabled). Regression for the presence-vs-boolean bug where an absent key
// evaluated to false and disabled the builtin.
func TestRegisterBuiltinsAbsentKeyEnabled(t *testing.T) {
	m := NewManager()
	ctx := &Context{DataDir: t.TempDir()}
	enabled := map[string]bool{"tickets": false} // cleanup absent
	if err := m.RegisterBuiltinsWithFilter(enabled, ctx, newFake("cleanup"), newFake("tickets")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := m.Get("cleanup"); !ok {
		t.Fatal("cleanup should be registered (absent key = enabled)")
	}
	if _, ok := m.Get("tickets"); ok {
		t.Fatal("tickets should be skipped (explicitly false)")
	}
}

// TestRegisterBuiltinsNameCollision verifies that a builtin whose name
// collides with an already-loaded module is skipped (dynamic wins).
func TestRegisterBuiltinsNameCollision(t *testing.T) {
	m := NewManager()
	ctx := &Context{DataDir: t.TempDir()}
	// Simulate a Lua module already loaded under the name "cleanup".
	m.modules["cleanup"] = &LoadedModule{Module: &fakeBuiltin{name: "cleanup"}, ModuleType: "lua"}
	m.order = append(m.order, "cleanup")
	if err := m.RegisterBuiltins(ctx, newFake("cleanup"), newFake("tickets")); err != nil {
		t.Fatalf("register: %v", err)
	}
	// cleanup should still be the Lua one (dynamic wins), tickets added.
	if got := m.modules["cleanup"].ModuleType; got != "lua" {
		t.Fatalf("builtin should not override loaded module, got type %q", got)
	}
	if _, ok := m.Get("tickets"); !ok {
		t.Fatal("tickets should still be registered")
	}
}

// fakeOnLoadBuiltin records whether OnLoad was invoked and with what DataDir.
type fakeOnLoadBuiltin struct {
	fakeBuiltin
	onLoadCalled bool
	dataDir      string
}

func (f *fakeOnLoadBuiltin) OnLoad(ctx *Context) error {
	f.onLoadCalled = true
	f.dataDir = ctx.DataDir
	return nil
}

// TestRegisterBuiltinsInvokesOnLoad verifies that registering a builtin calls
// its OnLoad with a per-module context (DataDir = base + name) and registers
// its event hooks. Regression for builtins being registered without ever
// being initialized.
func TestRegisterBuiltinsInvokesOnLoad(t *testing.T) {
	m := NewManager()
	base := t.TempDir()
	ctx := &Context{DataDir: base}
	mod := &fakeOnLoadBuiltin{fakeBuiltin: fakeBuiltin{name: "tickets"}}
	ctor := func() Module { return mod }
	if err := m.RegisterBuiltins(ctx, ctor); err != nil {
		t.Fatalf("register: %v", err)
	}
	if !mod.onLoadCalled {
		t.Fatal("OnLoad was not invoked for the registered builtin")
	}
	if mod.dataDir != filepath.Join(base, "tickets") {
		t.Fatalf("OnLoad DataDir = %q, want %q", mod.dataDir, filepath.Join(base, "tickets"))
	}
	if len(m.ListModuleHooks()) != 1 {
		t.Fatalf("expected 1 registered hook set, got %d", len(m.ListModuleHooks()))
	}
}

// fakeFailOnLoad returns an error from OnLoad.
type fakeFailOnLoad struct {
	fakeBuiltin
}

func (f *fakeFailOnLoad) OnLoad(*Context) error { return fmt.Errorf("boom") }

// TestRegisterBuiltinsOnLoadRollback verifies that a builtin whose OnLoad
// fails is rolled back entirely (no modules/order entry, no hooks) and the
// error is propagated.
func TestRegisterBuiltinsOnLoadRollback(t *testing.T) {
	m := NewManager()
	ctx := &Context{DataDir: t.TempDir()}
	err := m.RegisterBuiltins(ctx, newFake("cleanup"), func() Module { return &fakeFailOnLoad{fakeBuiltin: fakeBuiltin{name: "tickets"}} })
	if err == nil {
		t.Fatal("expected error from failing builtin OnLoad")
	}
	if _, ok := m.Get("tickets"); ok {
		t.Fatal("failing builtin should be rolled back")
	}
	if len(m.ListModuleHooks()) != 1 {
		t.Fatalf("expected only cleanup's hooks, got %d", len(m.ListModuleHooks()))
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
	if err := m.RegisterBuiltins(&Context{DataDir: t.TempDir()}, ctor); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Unloading a Disablable should route to OnDisable.
	if err := m.Unload("tickets"); err != nil {
		t.Fatalf("unload: %v", err)
	}
	if !mod.disabledCalled {
		t.Fatal("OnDisable not called on unload of a Disablable builtin")
	}
}
