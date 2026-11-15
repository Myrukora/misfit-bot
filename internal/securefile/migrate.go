package securefile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// MigrateResult reports what a Migrate pass did, for logging.
type MigrateResult struct {
	Scanned   int // regular files visited
	Encrypted int // files rewritten as ciphertext
	Skipped   int // already encrypted
	Failed    int // could not be rewritten (left untouched)
}

// Migrate encrypts every plaintext file under root in place, recursively.
//
// Lazy migration covers anything the bot re-saves, but state that is written
// once and never touched again (closed tickets, archived transcripts, old case
// files) would stay readable forever. This pass closes that gap at startup.
//
// Properties:
//   - Idempotent: files already carrying the encrypted header are skipped, so
//     running it on every boot is cheap.
//   - Atomic per file: each rewrite goes through a temp file + rename, so an
//     interrupted migration leaves the original intact rather than truncated.
//   - Fail-soft: a single unreadable file is counted and left alone; it never
//     aborts the pass or aborts bot startup.
//   - Symlink-safe: symlinks are ignored entirely (never followed, never
//     rewritten), so a hostile link cannot redirect the migration.
//   - Size-bounded: files above maxMigrateBytes are skipped, because these are
//     attachments and buffering an arbitrarily large file wastes memory. They
//     are covered by re-download/rewrite only if they change.
//
// A nil Key is a no-op pass (nothing to encrypt with).
func Migrate(root string, key *Key, maxMigrateBytes int64) (MigrateResult, error) {
	var res MigrateResult
	if key == nil || root == "" {
		return res, nil
	}
	info, err := os.Stat(root)
	if os.IsNotExist(err) {
		return res, nil // nothing provisioned yet
	}
	if err != nil {
		return res, fmt.Errorf("securefile: stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return res, fmt.Errorf("securefile: %s is not a directory", root)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // permission problem deeper down: keep going
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		// The key itself must never be "migrated".
		if filepath.Base(path) == "data.key" {
			return nil
		}
		fi, statErr := d.Info()
		if statErr != nil {
			res.Failed++
			return nil
		}
		if maxMigrateBytes > 0 && fi.Size() > maxMigrateBytes {
			return nil // large media: leave to its own rewrite path
		}
		res.Scanned++
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			res.Failed++
			return nil
		}
		if IsEncrypted(raw) {
			res.Skipped++
			return nil
		}
		if writeErr := rewriteAtomically(path, key.Seal(raw), fi.Mode().Perm()); writeErr != nil {
			res.Failed++
			return nil
		}
		res.Encrypted++
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("securefile: migrate %s: %w", root, err)
	}
	return res, nil
}

// rewriteAtomically replaces path with data via a temp file in the same
// directory, preserving the original mode (0600 for state files).
func rewriteAtomically(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".migrate-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(mode); err != nil {
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
