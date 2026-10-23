package imagefilter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/misfit/bot/modules"
)

// provision.go — lazy runtime setup. The filter needs two artifacts on disk:
//
//   - lib/onnxruntime/lib/libonnxruntime.so (ONNX Runtime C library)
//   - modules/imagefilter/models/clip-vision-<variant>.onnx (CLIP vision tower)
//
// install.sh and the updater provision them eagerly, but a deployment installed
// with --skip-imagefilter (or one whose artifacts were removed) must still be
// able to turn the filter on: enabling it starts the same idempotent
// scripts/setup_imagefilter.sh in the background, and the model is warmed as
// soon as that script exits. Nothing here ever blocks a gateway goroutine or
// an HTTP response beyond cheap stat calls.
const (
	// provisionTimeout bounds one setup run. The first run may download a
	// ~300 MB ONNX export plus a ~200 MB CPU torch wheel, so this is generous;
	// subsequent runs are near-instant.
	provisionTimeout = 45 * time.Minute
	// provisionTail keeps the last bytes of script output for diagnostics.
	provisionTail = 4 << 10
	// provisionErrCap bounds the string rendered on the dashboard.
	provisionErrCap = 600
)

// provisionState tracks the single in-flight setup run and its outcome.
type provisionState struct {
	mu       sync.Mutex
	running  bool
	lastErr  string
	started  time.Time
	finished time.Time
}

// snapshot reports the current state with the error capped for display.
func (p *provisionState) snapshot() (running bool, lastErr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.lastErr
	if len(err) > provisionErrCap {
		err = err[:provisionErrCap] + "…"
	}
	return p.running, err
}

// runProvisionScript runs the shared setup script. It is a variable so tests
// can exercise the state machine without downloading anything.
var runProvisionScript = func(ctx context.Context, root, script, variant string) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Dir = root
	// The script defaults to b32; pass the configured variant so a non-default
	// install provisions the model it will actually load.
	cmd.Env = append(os.Environ(), "VARIANT="+variant)
	// bash spawns python/pip: put it in its own process group so the timeout
	// kills the whole tree instead of orphaning a downloader.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// provisionTargets resolves the repo root and setup script from the module
// data dir (<root>/modules/imagefilter).
func provisionTargets(dataDir string) (root, script string) {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return "", ""
	}
	root = filepath.Dir(filepath.Dir(abs))
	return root, filepath.Join(root, "scripts", "setup_imagefilter.sh")
}

// artifactsMissing names the first missing runtime artifact ("" = ready).
// initRuntime is called here so a provisioned library is picked up without a
// restart; a failure is not cached (see runtime.go).
func artifactsMissing(dataDir, variant string) string {
	if err := initRuntime(); err != nil {
		return "ONNX Runtime library (lib/onnxruntime)"
	}
	if !ModelFilePresent(dataDir, variant) {
		return "CLIP model for variant " + variant
	}
	return ""
}

// logger returns the module logger (nil before OnLoad).
func (m *ImageFilterModule) logger() modules.Logger {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.ctx == nil {
		return nil
	}
	return m.ctx.Logger
}

// startProvisioning launches the background setup when the artifacts are
// missing. Single-flight: a second enable while a run is in progress is a
// no-op, and the next attempt after a failure is allowed (no retry loop).
func (m *ImageFilterModule) startProvisioning() {
	mgr, ok := m.admin()
	if !ok {
		return
	}
	root, script := provisionTargets(mgr.dataDir)
	if script == "" {
		return
	}

	m.prov.mu.Lock()
	if m.prov.running {
		m.prov.mu.Unlock()
		return
	}
	if _, err := os.Stat(script); err != nil {
		m.prov.lastErr = "setup script not found: " + script
		m.prov.mu.Unlock()
		return
	}
	variant := mgr.cfg.ClipVariant()
	m.prov.running = true
	m.prov.started = time.Now()
	m.prov.finished = time.Time{}
	m.prov.lastErr = ""
	m.prov.mu.Unlock()

	log := m.logger()
	if log != nil {
		log.Info("imagefilter: provisioning runtime artifacts in the background (variant %s) — the first run downloads and exports, this can take minutes", variant)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
		defer cancel()
		out, err := runProvisionScript(ctx, root, script, variant)
		tail := tailString(out, provisionTail)
		if err == nil && m.isLoaded() {
			// The script is idempotent and exits 0 when both artifacts are
			// ready; load the model the enabled guilds were waiting for. If the
			// module was unloaded meanwhile, leave the artifacts for the next
			// start (unloadModel already reset the refcount, so a late warm
			// cannot leak a session).
			if werr := mgr.RetryWarm(); werr != nil {
				err = werr
			}
		}

		m.prov.mu.Lock()
		m.prov.running = false
		m.prov.finished = time.Now()
		if err != nil {
			m.prov.lastErr = strings.TrimSpace(err.Error() + "\n" + tail)
		} else {
			m.prov.lastErr = ""
		}
		m.prov.mu.Unlock()

		if log != nil {
			if err != nil {
				log.Error("imagefilter: runtime provisioning failed: %v\n%s", err, tail)
			} else {
				log.Info("imagefilter: runtime provisioning finished — model warm")
			}
		}
	}()
}

// tailString returns at most the last n bytes of s.
func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…\n" + s[len(s)-n:]
}
