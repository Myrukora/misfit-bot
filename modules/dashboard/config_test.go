package main

import (
	"os"
	"testing"
)

func TestExecModeSetValidation(t *testing.T) {
	c := defaultConfig()

	valid := []string{"prefix", "slash"}
	for _, v := range valid {
		if err := c.Set("exec_mode", v); err != nil {
			t.Errorf("Set(exec_mode, %q) unexpected error: %v", v, err)
		}
		if c.ExecMode != v {
			t.Errorf("Set(exec_mode, %q): got %q, want %q", v, c.ExecMode, v)
		}
	}

	for _, v := range []string{"", "Prefix", "slash ", "hybrid", "PREFIX"} {
		if err := c.Set("exec_mode", v); err == nil {
			t.Errorf("Set(exec_mode, %q) expected error, got nil", v)
		}
	}
}

func TestDefaultExecMode(t *testing.T) {
	c := defaultConfig()
	if c.ExecMode != "prefix" {
		t.Errorf("defaultConfig().ExecMode = %q, want %q", c.ExecMode, "prefix")
	}
}

func TestLoadConfigNormalizesInvalidExecMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(cfgPath(dir), []byte("exec_mode: hybrid\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if c.ExecMode != "prefix" {
		t.Errorf("loadConfig normalized ExecMode = %q, want %q", c.ExecMode, "prefix")
	}

	if err := os.WriteFile(cfgPath(dir), []byte("exec_mode: slash\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err = loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if c.ExecMode != "slash" {
		t.Errorf("loadConfig kept ExecMode = %q, want %q", c.ExecMode, "slash")
	}
}
