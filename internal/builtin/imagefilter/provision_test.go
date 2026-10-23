package imagefilter

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// provisionHarness points a module at a fake repo root containing
// scripts/setup_imagefilter.sh and swaps the script runner.
func provisionHarness(t *testing.T, run func(ctx context.Context, root, script, variant, dataDir string) (string, error)) (*ImageFilterModule, *manager) {
	t.Helper()
	mod, mgr, _ := newTestModule(t)

	root := t.TempDir()
	dataDir := filepath.Join(root, "modules", "imagefilter")
	scriptDir := filepath.Join(root, "scripts")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "setup_imagefilter.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	mgr.dataDir = dataDir

	orig := runProvisionScript
	runProvisionScript = run
	t.Cleanup(func() { runProvisionScript = orig })
	return mod, mgr
}

// waitProvisionDone blocks until the background setup has exited.
func waitProvisionDone(t *testing.T, mod *ImageFilterModule) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mod.prov.mu.Lock()
		running := mod.prov.running
		mod.prov.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("provisioning did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestProvisionSingleFlight pins that two enables while a setup is running
// start exactly one script invocation.
func TestProvisionSingleFlight(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	mod, _ := provisionHarness(t, func(context.Context, string, string, string, string) (string, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return "ok", nil
	})

	mod.startProvisioning()
	<-entered
	mod.startProvisioning() // must observe running and bail out
	close(release)
	waitProvisionDone(t, mod)

	if got := calls.Load(); got != 1 {
		t.Fatalf("script runs = %d, want 1", got)
	}
}

// TestProvisionFailureRecorded pins the error surface: the exit error and the
// script output tail are kept for the dashboard.
func TestProvisionFailureRecorded(t *testing.T) {
	mod, _ := provisionHarness(t, func(context.Context, string, string, string, string) (string, error) {
		return "network down: dial tcp: timeout", errors.New("exit status 1")
	})
	mod.startProvisioning()
	waitProvisionDone(t, mod)

	running, errText := mod.prov.snapshot()
	if running {
		t.Fatal("provisioning should be finished")
	}
	if !strings.Contains(errText, "exit status 1") || !strings.Contains(errText, "network down") {
		t.Fatalf("error = %q, want the exit error and the output tail", errText)
	}
}

// TestProvisionSuccessWarms pins the recovery path: an enable that already
// failed on a missing model is completed by the background setup, which loads
// the model with the configured variant once the artifacts exist.
func TestProvisionSuccessWarms(t *testing.T) {
	var gotVariant string
	mod, mgr := provisionHarness(t, func(_ context.Context, _, _, variant, _ string) (string, error) {
		gotVariant = variant
		return "done", nil
	})
	if _, err := mgr.cfg.SetClipVariant("b16"); err != nil {
		t.Fatalf("set variant: %v", err)
	}

	// Simulate the pre-provision enable: the flag persists, the load fails.
	mgr.loadFn = func(string, string) (embedder, error) { return nil, errors.New("model missing") }
	if err := mgr.SetGuildEnabled("1", true); err == nil {
		t.Fatal("expected the load to fail while the model is missing")
	}
	if mgr.warm() {
		t.Fatal("model should be cold after the failed load")
	}

	mgr.loadFn = func(string, string) (embedder, error) {
		return &fakeEmbedder{inputSize: 224, dim: 8}, nil
	}
	mod.startProvisioning()
	waitProvisionDone(t, mod)

	if !mgr.warm() {
		t.Fatal("model should be warm once provisioning finished")
	}
	if gotVariant != "b16" {
		t.Errorf("script variant = %q, want the configured b16", gotVariant)
	}
}

// TestProvisionScriptMissing pins the early error when the checkout has no
// setup script (packaged deployments without the repo scripts).
func TestProvisionScriptMissing(t *testing.T) {
	mod, mgr, _ := newTestModule(t)
	mgr.dataDir = filepath.Join(t.TempDir(), "modules", "imagefilter")

	// The real checkout has the script; point the fallback at an empty dir so
	// this test exercises the missing-script branch (and never runs the script).
	empty := t.TempDir()
	orig := repoRootFn
	repoRootFn = func() (string, bool) { return empty, true }
	t.Cleanup(func() { repoRootFn = orig })

	mod.startProvisioning()
	_, errText := mod.prov.snapshot()
	if !strings.Contains(errText, "setup script not found") {
		t.Fatalf("error = %q, want the missing-script error", errText)
	}
}

// TestStatusReportsProvisioning pins the dashboard surface.
func TestStatusReportsProvisioning(t *testing.T) {
	mod, _, _ := newTestModule(t)
	mod.prov.mu.Lock()
	mod.prov.running = true
	mod.prov.lastErr = "boom"
	mod.prov.mu.Unlock()

	st := mod.Status()
	if !st.Provisioning {
		t.Error("Status.Provisioning = false, want true")
	}
	if st.ProvisionError != "boom" {
		t.Errorf("Status.ProvisionError = %q, want boom", st.ProvisionError)
	}
}

// TestProvisionRunsRealScript exercises the production runner (not the seam):
// the script runs from the repo root with the configured VARIANT and the
// module data dir in its environment.
func TestProvisionRunsRealScript(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	mod, mgr, _ := newTestModule(t)
	root := t.TempDir()
	mgr.dataDir = filepath.Join(root, "modules", "imagefilter")
	scriptDir := filepath.Join(root, "scripts")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	marker := filepath.Join(root, "ran.txt")
	script := "#!/bin/sh\nprintf '%s\\n%s' \"$VARIANT\" \"$IMAGEFILTER_DATA_DIR\" > \"" + marker + "\"\n"
	if err := os.WriteFile(filepath.Join(scriptDir, "setup_imagefilter.sh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if _, err := mgr.cfg.SetClipVariant("l14"); err != nil {
		t.Fatalf("set variant: %v", err)
	}

	mod.startProvisioning()
	waitProvisionDone(t, mod)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("script did not run: %v", err)
	}
	parts := strings.SplitN(string(data), "\n", 2)
	if len(parts) != 2 || parts[0] != "l14" {
		t.Errorf("script saw VARIANT=%q, want l14", parts[0])
	}
	if len(parts) != 2 || parts[1] != mgr.dataDir {
		t.Errorf("script saw IMAGEFILTER_DATA_DIR=%q, want %q", parts[1], mgr.dataDir)
	}
	if _, errText := mod.prov.snapshot(); errText != "" {
		t.Errorf("unexpected setup error: %s", errText)
	}
}
