package main

import "testing"

// view builds a minimal cmdView for dedupe tests.
func view(name, kind string) cmdView {
	return cmdView{Name: name, Kind: kind, ModuleOwner: "core", Category: "general"}
}

func TestDedupeForModePrefixPrefersPrefix(t *testing.T) {
	views := []cmdView{
		view("ping", "prefix"),
		view("ping", "slash"),
		view("backup", "prefix"),
		view("backup", "slash"),
	}
	out := dedupeForMode(views, "prefix")
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (one entry per command)", len(out))
	}
	for _, v := range out {
		if v.Kind != "prefix" {
			t.Errorf("command %q kind = %q, want prefix", v.Name, v.Kind)
		}
	}
}

func TestDedupeForModeSlashPrefersSlash(t *testing.T) {
	views := []cmdView{
		view("ping", "prefix"),
		view("ping", "slash"),
		view("backup", "slash"),
		view("backup", "prefix"),
	}
	out := dedupeForMode(views, "slash")
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (one entry per command)", len(out))
	}
	for _, v := range out {
		if v.Kind != "slash" {
			t.Errorf("command %q kind = %q, want slash", v.Name, v.Kind)
		}
	}
}

func TestDedupeForModeKeepsOnlyKind(t *testing.T) {
	// A prefix-only command must survive in slash mode, and vice versa.
	prefixOnly := dedupeForMode([]cmdView{view("cleanup", "prefix"), view("ping", "slash")}, "slash")
	got := map[string]string{}
	for _, v := range prefixOnly {
		got[v.Name] = v.Kind
	}
	if got["cleanup"] != "prefix" {
		t.Errorf("prefix-only command lost in slash mode: got %q", got["cleanup"])
	}
	if got["ping"] != "slash" {
		t.Errorf("slash command kind = %q, want slash", got["ping"])
	}

	slashOnly := dedupeForMode([]cmdView{view("cleanup", "prefix"), view("ping", "slash")}, "prefix")
	got = map[string]string{}
	for _, v := range slashOnly {
		got[v.Name] = v.Kind
	}
	if got["ping"] != "slash" {
		t.Errorf("slash-only command lost in prefix mode: got %q", got["ping"])
	}
	if got["cleanup"] != "prefix" {
		t.Errorf("prefix command kind = %q, want prefix", got["cleanup"])
	}
}

func TestDedupeForModeOrderAndDupes(t *testing.T) {
	views := []cmdView{
		view("a", "slash"),
		view("b", "prefix"),
		view("a", "prefix"),
		view("b", "slash"),
		view("a", "prefix"), // duplicate registration must not duplicate rows
	}
	out := dedupeForMode(views, "prefix")
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Name != "a" || out[1].Name != "b" {
		t.Errorf("order changed: got %q, %q — want a, b", out[0].Name, out[1].Name)
	}
	if out[0].Kind != "prefix" {
		t.Errorf("a kind = %q, want prefix (preferred even though slash came first)", out[0].Kind)
	}
}

func TestDedupeForModeMatchesDispatchPrecedence(t *testing.T) {
	// Registration order mirrors ExecuteCommand's resolution precedence:
	// core prefix, core slash, then modules in load order. A module named
	// "cleanup" sorts BEFORE "core" lexically, so a presentation-order dedupe
	// would pick the module's "ping" while Run executes core's — dedupeForMode
	// must follow dispatch order, not lexical owner order.
	views := []cmdView{
		{Name: "ping", Kind: "prefix", ModuleOwner: "core"},
		{Name: "ping", Kind: "slash", ModuleOwner: "core"},
		{Name: "ping", Kind: "prefix", ModuleOwner: "cleanup"},
		{Name: "ping", Kind: "slash", ModuleOwner: "cleanup"},
	}
	out := dedupeForMode(views, "prefix")
	if len(out) != 1 || out[0].ModuleOwner != "core" || out[0].Kind != "prefix" {
		t.Fatalf("prefix mode: got %+v, want core prefix ping", out)
	}
	out = dedupeForMode(views, "slash")
	if len(out) != 1 || out[0].ModuleOwner != "core" || out[0].Kind != "slash" {
		t.Fatalf("slash mode: got %+v, want core slash ping", out)
	}
}
