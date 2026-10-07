package builder

import (
	"fmt"
	"path/filepath"
	"runtime"
)

func ensureVenv(repoPath string, run func(dir, command string) error) error {
	venvPath := filepath.Join(repoPath, ".venv")
	interpreterPath := filepath.Join(venvPath, "bin", "python")
	if runtime.GOOS == "windows" {
		interpreterPath = filepath.Join(venvPath, "Scripts", "python.exe")
	}
	cfgPath := filepath.Join(venvPath, "pyvenv.cfg")

	if exists(interpreterPath) && exists(cfgPath) {
		return nil
	}

	if err := run(repoPath, "python3 -m venv .venv"); err != nil {
		if err2 := run(repoPath, "python -m venv .venv"); err2 != nil {
			return fmt.Errorf("failed to create venv: %w", err2)
		}
	}
	return nil
}

func buildPython(path string) (*BuildResult, error) {
	if err := ensureVenv(path, runShellCmd); err != nil {
		return nil, err
	}

	venvPath := filepath.Join(path, ".venv")
	pipPath := filepath.Join(venvPath, "bin", "pip")
	pythonPath := filepath.Join(venvPath, "bin", "python")
	if runtime.GOOS == "windows" {
		pipPath = filepath.Join(venvPath, "Scripts", "pip.exe")
		pythonPath = filepath.Join(venvPath, "Scripts", "python.exe")
	}

	// Install Deps
	if exists(filepath.Join(path, "requirements.txt")) {
		if err := runShellCmd(path, pipPath+" install -r requirements.txt"); err != nil {
			return nil, err
		}
	} else if exists(filepath.Join(path, "pyproject.toml")) {
		if err := runShellCmd(path, pipPath+" install ."); err != nil {
			return nil, err
		}
	}

	// Find Entry Point
	candidates := []string{"main.py", "server.py", "app.py", "src/main.py", "src/server.py"}
	var entryPoint string
	for _, c := range candidates {
		if exists(filepath.Join(path, c)) {
			entryPoint = c
			break
		}
	}

	if entryPoint == "" {
		return nil, fmt.Errorf("could not auto-detect python entry point")
	}

	return &BuildResult{
		Command:  pythonPath,
		Args:     []string{filepath.Join(path, entryPoint)},
		EnvNeeds: []string{},
	}, nil
}
