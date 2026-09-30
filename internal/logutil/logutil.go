// Package logutil provides shared log-file helpers used by the dashboard and
// any other subsystem that needs to tail the bot's daily-rotated log files.
package logutil

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
)

// ResolvePath picks the newest non-empty daily log file for a directory +
// basename pair, falling back to the legacy <base>.log file.
// Pure and testable: the glob is relative to dir, so callers may point it at
// any directory.
func ResolvePath(dir, base string) string {
	matches, err := filepath.Glob(filepath.Join(dir, base+"-*.log"))
	if err == nil && len(matches) > 0 {
		sort.Strings(matches) // ISO date suffixes sort chronologically
		for i := len(matches) - 1; i >= 0; i-- {
			if st, err := os.Stat(matches[i]); err == nil && st.Size() > 0 {
				return matches[i]
			}
		}
		return matches[len(matches)-1]
	}
	return filepath.Join(dir, base+".log")
}

// TailLines returns the last n lines of a file efficiently.
func TailLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	ring := make([]string, 0, n)
	for sc.Scan() {
		ring = append(ring, sc.Text())
		if len(ring) > n {
			ring = ring[len(ring)-n:]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return ring, nil
}
