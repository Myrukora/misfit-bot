package updater

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeVersion(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"0.1.0", "0.1.0"},
		{"0.1.0\n", "0.1.0"},
		{"  v0.1.0  ", "0.1.0"},
		{"1.22.3", "1.22.3"},
		{"v1.0.0-rc.1", "1.0.0-rc.1"},
		{"1.0.0+build.5", "1.0.0"},
		// anything else yields "no version" — never an injected string
		{"", ""},
		{"   ", ""},
		{"dev", ""},
		{"1.2", ""},
		{"1.2.3.4", ""},
		{"0.1.0; rm -rf /", ""},
		{"0.1.0\n0.2.0", ""},
		{"../../etc/passwd", ""},
		{"-X main.Version=evil", ""},
	} {
		if got := NormalizeVersion(tc.in); got != tc.want {
			t.Errorf("NormalizeVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestReadVersionFile(t *testing.T) {
	dir := t.TempDir()

	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("0.1.0\n")
	if got := ReadVersionFile(dir); got != "0.1.0" {
		t.Errorf("ReadVersionFile() = %q, want 0.1.0", got)
	}

	write("  v0.2.0  \n\n")
	if got := ReadVersionFile(dir); got != "0.2.0" {
		t.Errorf("ReadVersionFile() = %q, want 0.2.0 (surrounding whitespace tolerated)", got)
	}

	write("")
	if got := ReadVersionFile(dir); got != "" {
		t.Errorf("ReadVersionFile() = %q, want empty for an empty file", got)
	}

	write("garbage\n")
	if got := ReadVersionFile(dir); got != "" {
		t.Errorf("ReadVersionFile() = %q, want empty for a malformed file", got)
	}

	// A missing file (or directory) is simply "no version", never a panic.
	if got := ReadVersionFile(filepath.Join(dir, "does-not-exist")); got != "" {
		t.Errorf("ReadVersionFile(missing dir) = %q, want empty", got)
	}
}
