package dashboard

import "testing"

// view builds a minimal cmdView for dedupe tests.
func view(name, owner string) cmdView {
	return cmdView{Name: name, ModuleOwner: owner, Category: "general"}
}

func TestDedupeCatalogKeepsFirstOccurrence(t *testing.T) {
	views := []cmdView{
		view("ping", "core"),
		view("ping", "cleanup"),
		view("btest", "core"),
	}
	out := dedupeCatalog(views)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (one entry per command)", len(out))
	}
	for _, v := range out {
		if v.Name == "ping" && v.ModuleOwner != "core" {
			t.Errorf("ping owner = %q, want core (first registration wins)", v.ModuleOwner)
		}
	}
}

func TestDedupeCatalogOrderAndDupes(t *testing.T) {
	views := []cmdView{
		view("a", "core"),
		view("b", "core"),
		view("a", "core"),
		view("b", "core"),
		view("a", "core"), // duplicate registration must not duplicate rows
	}
	out := dedupeCatalog(views)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Name != "a" || out[1].Name != "b" {
		t.Errorf("order changed: got %q, %q — want a, b", out[0].Name, out[1].Name)
	}
}

func TestDedupeCatalogMatchesDispatchPrecedence(t *testing.T) {
	// Registration order mirrors ExecuteCommand's resolution precedence: core
	// first, then modules in load order. A module named "cleanup" sorts BEFORE
	// "core" lexically, so a presentation-order dedupe would pick the module's
	// "ping" while Run executes core's — dedupeCatalog must follow dispatch
	// order, not lexical owner order.
	views := []cmdView{
		{Name: "ping", ModuleOwner: "core"},
		{Name: "ping", ModuleOwner: "cleanup"},
	}
	out := dedupeCatalog(views)
	if len(out) != 1 || out[0].ModuleOwner != "core" {
		t.Fatalf("got %+v, want core ping", out)
	}
}
