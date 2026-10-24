package tickets

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/misfit/bot/modules"
)

// Ticket state contains private conversations and attachments. Keep the files
// private to the bot process (0600) and the containing directories restricted
// (0700), matching the project's .env/config/audit convention.
const (
	privateFileMode os.FileMode = 0o600
	privateDirMode  os.FileMode = 0o700
)

// ensurePrivateDir creates dir and tightens an existing directory to 0700.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, privateDirMode); err != nil {
		return err
	}
	return os.Chmod(dir, privateDirMode)
}

// writePrivateFile atomically replaces path using a random 0600 temp file in
// the same directory, avoiding predictable .tmp names and preserving 0600 even
// if an old temp file exists.
func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".ticket-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(privateFileMode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// openPrivateFile creates or truncates an attachment in dir and forces 0600,
// including for a file left at 0644 by an older build.
func openPrivateFile(dir, name string) (*os.File, error) {
	if err := ensurePrivateDir(dir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, privateFileMode)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(privateFileMode); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// hardenTicketTree tightens modes left by older builds. It is best-effort and
// never follows symlinks.
func hardenTicketTree(root string, log modules.Logger) {
	if root == "" {
		return
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		var mode os.FileMode
		switch {
		case d.IsDir():
			mode = privateDirMode
		case d.Type().IsRegular():
			mode = privateFileMode
		default:
			return nil
		}
		if err := os.Chmod(path, mode); err != nil && log != nil {
			log.Warn("Tickets: failed to tighten permissions on %s: %v", path, err)
		}
		return nil
	})
}
