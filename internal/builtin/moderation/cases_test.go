package moderation

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/misfit/bot/modules"
)

func TestCaseStoreSequenceFiltersAndWarnings(t *testing.T) {
	st, err := openCaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	add := func(c modules.ModerationCase) modules.ModerationCase {
		t.Helper()
		got, err := st.Add(c)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	c1 := add(modules.ModerationCase{GuildID: "1", Type: modules.ModerationTypeWarn, TargetID: "20", TargetTag: "Target", Reason: "spam"})
	c2 := add(modules.ModerationCase{GuildID: "1", Type: modules.ModerationTypeBan, TargetID: "21", TargetTag: "Other"})
	c3 := add(modules.ModerationCase{GuildID: "1", Type: modules.ModerationTypeWarn, TargetID: "20", TargetTag: "Target", ExpiresAt: &past})
	c4 := add(modules.ModerationCase{GuildID: "1", Type: modules.ModerationTypeWarn, TargetID: "20", TargetTag: "Target", ExpiresAt: &future})
	if c1.ID != 1 || c2.ID != 2 || c3.ID != 3 || c4.ID != 4 {
		t.Fatalf("ids = %d %d %d %d", c1.ID, c2.ID, c3.ID, c4.ID)
	}

	all, err := st.List("1", modules.ModerationCaseFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 || all[0].ID != 4 {
		t.Fatalf("list order = %+v", all)
	}
	warns, _ := st.List("1", modules.ModerationCaseFilter{Type: modules.ModerationTypeWarn})
	if len(warns) != 3 {
		t.Fatalf("warn filter = %d", len(warns))
	}
	user, _ := st.List("1", modules.ModerationCaseFilter{UserID: "21"})
	if len(user) != 1 || user[0].ID != 2 {
		t.Fatalf("user filter = %+v", user)
	}
	limited, _ := st.List("1", modules.ModerationCaseFilter{Limit: 1})
	if len(limited) != 1 || limited[0].ID != 4 {
		t.Fatalf("limit = %+v", limited)
	}

	active, err := st.ActiveWarnings("1", "20", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 || active[0].ID != 1 || active[1].ID != 4 {
		t.Fatalf("active warnings = %+v", active)
	}

	removed, err := st.RemoveWarning("1", 1, "10", "Mod", "appeal")
	if err != nil {
		t.Fatal(err)
	}
	if !removed.Removed || removed.RemovedByTag != "Mod" {
		t.Fatalf("removed case = %+v", removed)
	}
	active, _ = st.ActiveWarnings("1", "20", now)
	if len(active) != 1 || active[0].ID != 4 {
		t.Fatalf("active after remove = %+v", active)
	}
	if _, err := st.RemoveWarning("1", 1, "10", "Mod", ""); err == nil {
		t.Fatal("removing twice should fail")
	}
	if _, err := st.RemoveWarning("1", 2, "10", "Mod", ""); err == nil {
		t.Fatal("removing a ban should fail")
	}
}

func TestCaseStoreRejectsInvalidGuildsAndCorruption(t *testing.T) {
	dir := t.TempDir()
	st, err := openCaseStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, gid := range []string{"", "0", "..", "abc"} {
		if _, err := st.List(gid, modules.ModerationCaseFilter{}); err == nil {
			t.Errorf("List(%q) should fail", gid)
		}
		if _, err := st.Add(modules.ModerationCase{GuildID: gid}); err == nil {
			t.Errorf("Add(%q) should fail", gid)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.List("1", modules.ModerationCaseFilter{}); err == nil {
		t.Fatal("corrupt case file should be an error")
	}
}

func TestCaseStoreSaveFailureDoesNotMutateMemory(t *testing.T) {
	dir := t.TempDir()
	st, err := openCaseStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.Add(modules.ModerationCase{GuildID: "1", Type: modules.ModerationTypeWarn, TargetID: "20", Reason: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	// A directory at the temp path makes the atomic write fail.
	blocker := filepath.Join(dir, "1.json.tmp")
	if err := os.Mkdir(blocker, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(modules.ModerationCase{GuildID: "1", Type: modules.ModerationTypeBan, TargetID: "21"}); err == nil {
		t.Fatal("save failure should be returned")
	}
	cases, err := st.List("1", modules.ModerationCaseFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 || cases[0].ID != first.ID {
		t.Fatalf("cache mutated after failed add: %+v", cases)
	}
	if _, err := st.RemoveWarning("1", first.ID, "10", "Mod", "x"); err == nil {
		t.Fatal("remove should fail while writes are blocked")
	}
	got, err := st.Get("1", first.ID)
	if err != nil || got.Removed {
		t.Fatalf("removal mutated memory: %+v err %v", got, err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	next, err := st.Add(modules.ModerationCase{GuildID: "1", Type: modules.ModerationTypeBan, TargetID: "21"})
	if err != nil {
		t.Fatalf("add after unblocking: %v", err)
	}
	if next.ID != first.ID+1 {
		t.Fatalf("next id = %d, want %d (failed write must not consume an id)", next.ID, first.ID+1)
	}
}
