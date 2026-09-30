package imagefilter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGuildDirRejectsCraftedIDs pins the directory-name guard: a crafted guild
// ID must never escape the module's image root, even if it reaches the FS
// layer (the dashboard validates the snowflake shape first — this is defense
// in depth).
func TestGuildDirRejectsCraftedIDs(t *testing.T) {
	root := t.TempDir()
	im := newImages(root)
	rootDir := imagesRoot(root)
	for _, bad := range []string{"", ".", "..", "../x", "a/b", "/etc", "./1", "a/../.."} {
		if _, err := im.guildDir(bad); err == nil {
			t.Errorf("guildDir(%q) accepted a crafted id", bad)
		}
		if _, err := im.List(bad); err == nil {
			t.Errorf("List(%q) accepted a crafted id", bad)
		}
		if err := im.Remove(bad, "x.png"); err == nil {
			t.Errorf("Remove(%q) accepted a crafted id", bad)
		}
		if _, err := im.Read(bad, "x.png"); err == nil {
			t.Errorf("Read(%q) accepted a crafted id", bad)
		}
	}
	// A normal snowflake still resolves.
	dir, err := im.guildDir("123456789")
	if err != nil {
		t.Fatalf("valid guild id rejected: %v", err)
	}
	if dir != filepath.Join(rootDir, "123456789") {
		t.Errorf("guildDir = %q", dir)
	}
}

// TestCheckGuildBudget pins the per-guild caps: count, bytes, and that staging
// leftovers are not counted against the budget.
func TestCheckGuildBudget(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "123")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Missing dir → nothing to check.
	if err := checkGuildBudget(filepath.Join(dir, "absent"), 10); err != nil {
		t.Errorf("absent dir: %v", err)
	}

	// Under the count cap → fine.
	for i := 0; i < maxRefImagesPerGuild-1; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("ref%d.png", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkGuildBudget(dir, 1); err != nil {
		t.Errorf("at the limit should pass: %v", err)
	}
	// One more → rejected.
	if err := os.WriteFile(filepath.Join(dir, "last.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkGuildBudget(dir, 1); err == nil {
		t.Error("count cap not enforced")
	} else if !strings.Contains(err.Error(), "full") {
		t.Errorf("count error = %v", err)
	}

	// Byte cap: a single file over the limit is rejected regardless of count.
	bigDir := filepath.Join(t.TempDir(), "456")
	if err := os.MkdirAll(bigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bigDir, "big.png"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkGuildBudget(bigDir, maxRefBytesPerGuild); err == nil {
		t.Error("byte cap not enforced")
	}

	// Subdirs and staging leftovers (.tmp, .tmp-*) don't consume budget.
	skipDir := filepath.Join(t.TempDir(), "789")
	if err := os.MkdirAll(filepath.Join(skipDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.png.tmp", "b.png.tmp-123", "c.png"} {
		if err := os.WriteFile(filepath.Join(skipDir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(skipDir, "nested", "deep.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// One real file counted; adding one more must fit.
	if err := checkGuildBudget(skipDir, 1); err != nil {
		t.Errorf("staging leftovers counted: %v", err)
	}
}

// TestImagesAddEnforcesBudget is the integration half: once the guild is full,
// Add must fail loudly instead of writing a 201st reference image.
func TestImagesAddEnforcesBudget(t *testing.T) {
	dir := t.TempDir()
	im := newImages(dir)
	guildDir := filepath.Join(imagesRoot(dir), "123")
	if err := os.MkdirAll(guildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxRefImagesPerGuild; i++ {
		if err := os.WriteFile(filepath.Join(guildDir, fmt.Sprintf("ref%d.png", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := im.Add("123", smallPNG(t, 4, 4, rgbaOf(1, 2, 3)), "new.png"); err == nil {
		t.Fatal("Add must fail when the guild budget is exhausted")
	}
	// And nothing was written.
	names, err := im.List("123")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != maxRefImagesPerGuild {
		t.Errorf("images = %d, want %d", len(names), maxRefImagesPerGuild)
	}
}

// TestImagesAddIsAtomicOnFailure pins Q11: a failed add leaves no partial file
// (the old fixed "<path>.tmp" staging file could survive a crash/race).
func TestImagesAddIsAtomicOnFailure(t *testing.T) {
	dir := t.TempDir()
	im := newImages(dir)
	if _, _, err := im.Add("123", []byte("not an image"), "x.png"); err == nil {
		t.Fatal("non-image accepted")
	}
	entries, err := os.ReadDir(filepath.Join(imagesRoot(dir), "123"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("leftover staging entry: %s", e.Name())
	}
}
