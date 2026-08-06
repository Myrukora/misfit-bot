package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// DefaultRetentionDays is the number of daily log files to keep before
// cleaning up older ones. Files beyond this limit are deleted on each rotation.
const DefaultRetentionDays = 30

// DailyRotatingWriter is an io.WriteCloser that writes to a date-suffixed log
// file each day. On the first Write of a new calendar day, it closes the
// previous file and opens a new one named <basename>-YYYY-MM-DD.log.
//
// Thread-safe: all writes are serialised via an internal mutex.
type DailyRotatingWriter struct {
	dir      string
	basename string
	mu       sync.Mutex
	file     *os.File
	curDate  string // YYYY-MM-DD of the currently open file
}

// NewDailyRotatingWriter creates a writer that produces files like
// <dir>/<basename>-2026-07-24.log. The directory is created if missing.
func NewDailyRotatingWriter(dir, basename string) (*DailyRotatingWriter, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	w := &DailyRotatingWriter{
		dir:      dir,
		basename: basename,
	}
	// Open today's file immediately so that early writes don't trigger
	// a date check on the very first call.
	if err := w.openFile(time.Now()); err != nil {
		return nil, err
	}
	return w, nil
}

// Write implements io.Writer. It checks whether the date has changed and
// rotates the file if necessary, then writes p to the current file.
func (w *DailyRotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if today != w.curDate {
		if err := w.rotateLocked(today); err != nil {
			// If rotation fails, try to write to the old file anyway
			// so we don't lose data. The error is still returned so
			// callers (or monitoring) can observe it.
			n, writeErr := w.file.Write(p)
			if writeErr != nil {
				return n, fmt.Errorf("rotate: %v; write: %w", err, writeErr)
			}
			return n, fmt.Errorf("rotate: %w", err)
		}
	}

	return w.file.Write(p)
}

// Close implements io.Closer. It closes the currently open log file.
func (w *DailyRotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closeLocked()
}

// ── helpers ────────────────────────────────────────────────────────────────

func (w *DailyRotatingWriter) openFile(now time.Time) error {
	w.curDate = now.Format("2006-01-02")
	path := filepath.Join(w.dir, fmt.Sprintf("%s-%s.log", w.basename, w.curDate))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	w.file = f
	return nil
}

func (w *DailyRotatingWriter) rotateLocked(newDate string) error {
	if err := w.closeLocked(); err != nil {
		return err
	}
	if err := w.openFile(time.Now()); err != nil {
		return err
	}
	w.cleanupLocked()
	return nil
}

func (w *DailyRotatingWriter) closeLocked() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	w.curDate = ""
	return err
}

// cleanupLocked removes log files beyond the retention limit. It globs for
// <basename>-YYYY-MM-DD.log files in the log directory, sorts them
// alphabetically (which is also chronological with ISO date suffixes), and
// deletes all but the most recent DefaultRetentionDays files.
//
// Must be called with w.mu held.
func (w *DailyRotatingWriter) cleanupLocked() {
	pattern := filepath.Join(w.dir, fmt.Sprintf("%s-*.log", w.basename))
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) <= DefaultRetentionDays {
		return
	}
	sort.Strings(matches)
	for _, f := range matches[:len(matches)-DefaultRetentionDays] {
		os.Remove(f) // best-effort; failure is non-fatal
	}
}
