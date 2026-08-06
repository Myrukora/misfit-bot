package modules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/custombot/bot/logger"
)

// PythonVenv manages a per-module Python virtual environment.
// Each Python module gets its own .venv directory with isolated dependencies.
type PythonVenv struct {
	moduleDir        string
	venvPath         string
	pythonBin        string
	requirementsPath string
	hashPath         string
	logger           *logger.Logger
}

// NewPythonVenv creates a venv manager for a module directory.
func NewPythonVenv(moduleDir string, log *logger.Logger) *PythonVenv {
	return &PythonVenv{
		moduleDir:        moduleDir,
		venvPath:         filepath.Join(moduleDir, ".venv"),
		pythonBin:        filepath.Join(moduleDir, ".venv", "bin", "python3"),
		requirementsPath: filepath.Join(moduleDir, "requirements.txt"),
		hashPath:         filepath.Join(moduleDir, ".venv", ".requirements_hash"),
		logger:           log,
	}
}

// PythonBin returns the path to the venv's python binary.
func (v *PythonVenv) PythonBin() string {
	return v.pythonBin
}

// Ensure creates the venv and installs requirements if needed.
// Uses a hash of requirements.txt to skip redundant installs.
func (v *PythonVenv) Ensure() error {
	// Create venv if it doesn't exist
	if _, err := os.Stat(v.venvPath); os.IsNotExist(err) {
		v.logger.Info("Creating Python venv for module: %s", filepath.Base(v.moduleDir))
		cmd := exec.Command("python3", "-m", "venv", v.venvPath)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to create venv: %w", err)
		}
	}

	// No requirements file — nothing to install
	if _, err := os.Stat(v.requirementsPath); os.IsNotExist(err) {
		return nil
	}

	// Check if requirements hash changed
	currentHash, err := v.computeHash()
	if err != nil {
		return fmt.Errorf("failed to compute requirements hash: %w", err)
	}

	savedHash := ""
	if data, err := os.ReadFile(v.hashPath); err == nil {
		savedHash = string(data)
	}

	if currentHash == savedHash {
		return nil
	}

	// Install requirements
	v.logger.Info("Installing Python dependencies for module: %s", filepath.Base(v.moduleDir))
	cmd := exec.Command(v.pythonBin, "-m", "pip", "install", "-r", v.requirementsPath)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install requirements: %w", err)
	}

	// Save hash
	if err := os.MkdirAll(filepath.Dir(v.hashPath), 0755); err != nil {
		return fmt.Errorf("failed to create hash dir: %w", err)
	}
	if err := os.WriteFile(v.hashPath, []byte(currentHash), 0644); err != nil {
		return fmt.Errorf("failed to save requirements hash: %w", err)
	}

	return nil
}

// computeHash returns the SHA256 hex digest of requirements.txt.
func (v *PythonVenv) computeHash() (string, error) {
	data, err := os.ReadFile(v.requirementsPath)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}
