package modules

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/disgoorg/disgo/rest"
	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/logger"
)

// PythonLoader handles loading and managing Python modules.
// Each Python module is a directory containing main.py and optionally requirements.txt.
type PythonLoader struct {
	bridge   *PythonBridge
	sdkPath  string // path to sdk/python directory (contains misfit package)
	logger   *logger.Logger
	voiceMgr *VoiceManager
}

// NewPythonLoader creates a new Python loader.
// sdkPath should point to the sdk/python directory.
// token is the bot token, used by the bridge to proxy Discord API calls.
func NewPythonLoader(bot commands.Interface, log *logger.Logger, restClient rest.Rest, sdkPath, token string, voiceMgr *VoiceManager) *PythonLoader {
	return &PythonLoader{
		bridge:   NewPythonBridge(restClient, log, token, voiceMgr),
		sdkPath:  sdkPath,
		logger:   log,
		voiceMgr: voiceMgr,
	}
}

// IsPythonModule checks if the given path is a Python module (directory with main.py).
func IsPythonModule(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return false
	}
	mainPy := filepath.Join(path, "main.py")
	_, err = os.Stat(mainPy)
	return err == nil
}

// Load loads a Python module from the given directory path.
// It ensures the venv exists, spawns the Python process, and waits for the ready signal.
func (l *PythonLoader) Load(path string) (Module, error) {
	if !IsPythonModule(path) {
		return nil, fmt.Errorf("not a Python module: %s", path)
	}

	mainPy := filepath.Join(path, "main.py")
	if _, err := os.Stat(mainPy); os.IsNotExist(err) {
		return nil, fmt.Errorf("main.py not found in module directory: %s", path)
	}

	// Ensure venv exists and dependencies are installed
	venv := NewPythonVenv(path, l.logger)
	if err := venv.Ensure(); err != nil {
		return nil, fmt.Errorf("venv setup failed: %w", err)
	}

	// Path to the runner script
	runnerPath := filepath.Join(l.sdkPath, "misfit", "runner.py")
	if _, err := os.Stat(runnerPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("runner.py not found at %s", runnerPath)
	}

	// Create the Python process
	cmd := exec.Command(venv.PythonBin(), runnerPath, mainPy)

	// Set PYTHONPATH to include sdk/python so `import misfit` works
	env := os.Environ()
	env = append(env, "PYTHONPATH="+l.sdkPath)
	cmd.Env = env

	// Create IPC handler
	ipc := NewPythonIPC(cmd, l.logger)

	// Channel to receive the ready signal
	readyCh := make(chan PythonReadyInfo, 1)
	var readyOnce sync.Once

	// Set up the ready callback
	ipc.onReady = func(info PythonReadyInfo) {
		readyOnce.Do(func() {
			readyCh <- info
		})
	}

	// Attach bridge callbacks for respond/reply/log/error
	l.bridge.AttachCallbacks(ipc)

	// Start the process
	if err := ipc.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Python process: %w", err)
	}

	// Wait for ready signal with timeout
	select {
	case info := <-readyCh:
		mod := NewPythonModule(path, ipc, l.bridge, info)
		l.logger.Info("Python module ready: %s v%s", mod.Name(), mod.Version())
		return mod, nil
	case <-time.After(30 * time.Second):
		_ = ipc.Stop()
		return nil, fmt.Errorf("Python module %s did not send ready signal within 30s", filepath.Base(path))
	}
}

// DiscoverPythonModules scans a directory for Python modules.
func (l *PythonLoader) DiscoverPythonModules(dir string) []ModuleInfo {
	var mods []ModuleInfo

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		if !IsPythonModule(path) {
			continue
		}

		mods = append(mods, ModuleInfo{
			Name:   entry.Name(),
			Type:   "python",
			Path:   path,
			Loaded: false,
		})
	}

	return mods
}
