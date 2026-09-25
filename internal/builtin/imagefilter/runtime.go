package imagefilter

import (
	"os"
	"path/filepath"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// Runtime initialization is process-global in onnxruntime_go. The dashboard
// (and any future consumer) may trigger loads from different goroutines, so
// it's done once, idempotently. The library lives in lib/onnxruntime (see
// scripts/setup_onnx.sh); missing lib = clean error, never a panic.
var (
	runtimeMu     sync.Mutex
	runtimeInit   bool
	runtimeLibErr error
)

// ortLibPath resolves lib/onnxruntime relative to the repo root (found by
// walking up from the CWD — handles both the binary running from the repo dir
// and tests running from a package dir). Falls back to system locations.
func ortLibPath() string {
	systemCandidates := []string{
		"/usr/lib/x86_64-linux-gnu/libonnxruntime.so",
		"/usr/local/lib/libonnxruntime.so",
	}
	dir, err := os.Getwd()
	if err == nil {
		for {
			candidate := filepath.Join(dir, "lib", "onnxruntime", "lib", "libonnxruntime.so")
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				break // repo root reached; lib simply absent
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	for _, p := range systemCandidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join("lib", "onnxruntime", "lib", "libonnxruntime.so")
}

// initRuntime initializes the ONNX Runtime environment once per process.
// Returns an error when the C library is unavailable (the manager surfaces
// this on the dashboard instead of failing the whole module).
func initRuntime() error {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if runtimeInit {
		return runtimeLibErr
	}
	runtimeInit = true
	lib := ortLibPath()
	if _, err := os.Stat(lib); err != nil {
		runtimeLibErr = os.ErrNotExist
		return runtimeLibErr
	}
	ort.SetSharedLibraryPath(lib)
	if err := ort.InitializeEnvironment(); err != nil {
		runtimeLibErr = err
		return err
	}
	runtimeLibErr = nil
	return nil
}
