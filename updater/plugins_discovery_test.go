package updater

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestDiscoverGoPluginDirs pins the RECURSIVE plugin discovery contract: the
// updater must find Go plugin packages wherever the module layout puts them
// (the modules/Go|Lua|Python restructure would otherwise orphan plugins when
// an OLD updater binary applies a layout-changing update — the .so files were
// never rebuilt into the new locations and the bot silently skipped every
// module on restart).
func TestDiscoverGoPluginDirs(t *testing.T) {
	root := t.TempDir()
	modulesDir := filepath.Join(root, "modules")

	// Go plugins in the current layout.
	mk(t, filepath.Join(modulesDir, "Go", "dashboard", "main.go"))
	mk(t, filepath.Join(modulesDir, "Go", "cleanup", "main.go"))
	// Lua/Python modules: no main.go — must NOT be discovered as Go plugins.
	mk(t, filepath.Join(modulesDir, "Lua", "greet", "greet.lua"))
	mk(t, filepath.Join(modulesDir, "Python", "hello", "main.py"))
	// Hidden directories (Python venvs, .git) must be skipped even when they
	// contain main.go files.
	mk(t, filepath.Join(modulesDir, "Python", "hello", ".venv", "lib", "pkg", "main.go"))
	mk(t, filepath.Join(modulesDir, ".git", "hooks", "main.go"))
	// A nested main.go inside a plugin package is a legit plugin too.
	mk(t, filepath.Join(modulesDir, "Go", "dashboard", "subcmd", "main.go"))

	got, err := discoverGoPluginDirs(modulesDir)
	if err != nil {
		t.Fatalf("discoverGoPluginDirs: %v", err)
	}
	var names []string
	for _, p := range got {
		rel, _ := filepath.Rel(modulesDir, p.dir)
		names = append(names, filepath.ToSlash(rel)+" → "+p.name+".so")
	}
	sort.Strings(names)

	want := []string{
		"Go/cleanup → cleanup.so",
		"Go/dashboard → dashboard.so",
		"Go/dashboard/subcmd → subcmd.so",
	}
	if len(names) != len(want) {
		t.Fatalf("discovered %d plugin dirs, want %d: %v", len(names), len(want), names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("plugin dir #%d = %q, want %q (all: %v)", i, names[i], want[i], names)
		}
	}
}

// TestDiscoverGoPluginDirsMissingModules pins the missing-dir behaviour: a
// fresh repo without modules/ must not error the whole update.
func TestDiscoverGoPluginDirsMissingModules(t *testing.T) {
	got, err := discoverGoPluginDirs(filepath.Join(t.TempDir(), "modules"))
	if err != nil {
		t.Fatalf("discoverGoPluginDirs on missing dir: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want no plugins, got %v", got)
	}
}

func mk(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
}
