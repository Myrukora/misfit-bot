package tickets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/misfit/bot/modules"
)

func TestTicketFilesArePrivate(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.save(&modules.Ticket{ID: "staff-0001", GuildID: "g1", Status: "open"}); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(ticketsRoot(dir), "g1"),
		filepath.Join(ticketsRoot(dir), "g1", "staff-0001.json"),
		filepath.Join(ticketsRoot(dir), "g1", "index.json"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(privateFileMode)
		if info.IsDir() {
			want = privateDirMode
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}

func TestHardenTicketTreeDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "g1"), 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "g1", "ticket.json")
	if err := os.WriteFile(inside, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "g1", "link.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	hardenTicketTree(root, nil)

	for _, path := range []string{root, filepath.Join(root, "g1")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != privateDirMode {
			t.Errorf("%s mode = %o, want %o", path, info.Mode().Perm(), privateDirMode)
		}
	}
	info, err := os.Stat(inside)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != privateFileMode {
		t.Fatalf("ticket file mode = %o, want %o", info.Mode().Perm(), privateFileMode)
	}
	info, err = os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("symlink target mode = %o, want 644", info.Mode().Perm())
	}
}
