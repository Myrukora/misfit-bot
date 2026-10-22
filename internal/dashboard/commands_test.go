package dashboard

import "testing"

// view builds a minimal cmdView for dedupe tests.
func view(name, kind string) cmdView {
	return cmdView{Name: name, Kind: kind, ModuleOwner: "core", Category: "general"}
}

func TestDedupeCatalogPrefersPrefix(t *testing.T) {
	views := []cmdView{
		view("ping", "slash"), // slash first: the prefix entry must still win
		view("ping", "prefix"),
		view("btest", "prefix"),
		view("btest", "slash"),
	}
	out := dedupeCatalog(views)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (one entry per command)", len(out))
	}
	for _, v := range out {
		if v.Kind != "prefix" {
			t.Errorf("command %q kind = %q, want prefix", v.Name, v.Kind)
		}
	}
}

func TestDedupeCatalogKeepsSlashOnly(t *testing.T) {
	// A command that only exists in slash form must survive the prefix
	// preference, so nothing disappears from the catalog.
	out := dedupeCatalog([]cmdView{view("cleanup", "prefix"), view("ping", "slash")})
	got := map[string]string{}
	for _, v := range out {
		got[v.Name] = v.Kind
	}
	if got["cleanup"] != "prefix" {
		t.Errorf("prefix command kind = %q, want prefix", got["cleanup"])
	}
	if got["ping"] != "slash" {
		t.Errorf("slash-only command lost: got %q", got["ping"])
	}
}

func TestDedupeCatalogOrderAndDupes(t *testing.T) {
	views := []cmdView{
		view("a", "slash"),
		view("b", "prefix"),
		view("a", "prefix"),
		view("b", "slash"),
		view("a", "prefix"), // duplicate registration must not duplicate rows
	}
	out := dedupeCatalog(views)
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

func TestDedupeCatalogMatchesDispatchPrecedence(t *testing.T) {
	// Registration order mirrors ExecuteCommand's resolution precedence:
	// core prefix, core slash, then modules in load order. A module named
	// "cleanup" sorts BEFORE "core" lexically, so a presentation-order dedupe
	// would pick the module's "ping" while Run executes core's — dedupeCatalog
	// must follow dispatch order, not lexical owner order.
	views := []cmdView{
		{Name: "ping", Kind: "prefix", ModuleOwner: "core"},
		{Name: "ping", Kind: "slash", ModuleOwner: "core"},
		{Name: "ping", Kind: "prefix", ModuleOwner: "cleanup"},
		{Name: "ping", Kind: "slash", ModuleOwner: "cleanup"},
	}
	out := dedupeCatalog(views)
	if len(out) != 1 || out[0].ModuleOwner != "core" || out[0].Kind != "prefix" {
		t.Fatalf("got %+v, want core prefix ping", out)
	}
}
