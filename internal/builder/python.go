package builder

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// lookUV finds the uv executable; tests replace it to simulate uv being absent.
var lookUV = func() (string, error) { return exec.LookPath("uv") }

// noticeOut receives notices that do not stop the build; tests replace it.
var noticeOut io.Writer = os.Stderr

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

// usesUV reports whether the project is managed with uv: it ships a uv.lock
// next to the pyproject.toml that uv syncs from.
func usesUV(path string) bool {
	return exists(filepath.Join(path, "uv.lock")) && exists(filepath.Join(path, "pyproject.toml"))
}

func buildPython(path string) (*BuildResult, error) {
	entryPoint, err := findPythonEntry(path)
	if usesUV(path) {
		uvPath, lookErr := lookUV()
		if lookErr == nil {
			if err != nil {
				return nil, err
			}
			return buildPythonUV(path, uvPath, entryPoint)
		}
		fmt.Fprintf(noticeOut, "Note: %s ships uv.lock; uv is not installed, so mcpm installs it with pip. Install uv to have mcpm use it.\n", path)
	}
	return buildPythonPip(path, entryPoint, err)
}

// buildPythonUV installs the locked dependencies with uv and runs the server
// through uv, so the environment always matches uv.lock.
func buildPythonUV(path, uvPath, entryPoint string) (*BuildResult, error) {
	if err := runCommand(path, uvPath, "sync", "--frozen"); err != nil {
		return nil, err
	}
	return &BuildResult{
		Command:  uvPath,
		Args:     []string{"run", "--frozen", "--project", path, filepath.Join(path, entryPoint)},
		EnvNeeds: []string{},
	}, nil
}

func buildPythonPip(path, entryPoint string, entryErr error) (*BuildResult, error) {
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
		if err := runCommand(path, pipPath, "install", "-r", "requirements.txt"); err != nil {
			return nil, err
		}
	} else if exists(filepath.Join(path, "pyproject.toml")) {
		if err := runCommand(path, pipPath, "install", "."); err != nil {
			return nil, err
		}
	}

	if entryErr != nil {
		return nil, entryErr
	}

	return &BuildResult{
		Command:  pythonPath,
		Args:     []string{filepath.Join(path, entryPoint)},
		EnvNeeds: []string{},
	}, nil
}

func findPythonEntry(path string) (string, error) {
	candidates := []string{"main.py", "server.py", "app.py", "src/main.py", "src/server.py"}
	for _, c := range candidates {
		if exists(filepath.Join(path, c)) {
			return c, nil
		}
	}
	return "", fmt.Errorf("could not auto-detect python entry point")
}
