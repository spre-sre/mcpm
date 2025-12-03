package builder

import (
	"fmt"
	"path/filepath"
	"runtime"
)

func buildPython(path string) (*BuildResult, error) {
	// Create venv
	venvPath := filepath.Join(path, ".venv")
	// Force python3
	if err := runShellCmd(path, "python3 -m venv .venv"); err != nil {
		// Fallback to just python
		if err := runShellCmd(path, "python -m venv .venv"); err != nil {
			return nil, fmt.Errorf("failed to create venv: %w", err)
		}
	}

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
		// Fallback: try to see if the package installed a CLI bin?
		// For now, fail if no script found.
		return nil, fmt.Errorf("could not auto-detect python entry point")
	}

	return &BuildResult{
		Command:  pythonPath,
		Args:     []string{filepath.Join(path, entryPoint)},
		EnvNeeds: []string{},
	}, nil
}
