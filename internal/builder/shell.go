package builder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// loginShell returns the user's shell and the flags that run a command string
// with the profile loaded (needed for nvm, etc.).
// Priority: zsh (macOS default) -> bash -> sh
func loginShell() (string, []string) {
	if _, err := exec.LookPath("zsh"); err == nil {
		return "zsh", []string{"-l", "-c"} // -l for login (loads profile), no -i to avoid job control issues
	}
	if _, err := exec.LookPath("bash"); err == nil {
		return "bash", []string{"-l", "-c"}
	}
	return "sh", []string{"-c"}
}

func runShellCmd(dir string, command string) error {
	if command == "" {
		return nil
	}

	shell, args := loginShell()
	cmd := exec.Command(shell, append(args, command)...)
	cmd.Dir = dir
	cmd.Env = os.Environ() // Inherit current environment

	// Capture output for debugging
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}
	return nil
}

// CheckCommand reports whether a registered server command can still be
// started. A command that names a path must be an executable file; a bare
// name must resolve on PATH, either in the current environment or through the
// login shell (where nvm, pyenv and similar tools put their binaries).
func CheckCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("command is empty")
	}
	if filepath.IsAbs(command) || strings.ContainsAny(command, "/"+string(filepath.Separator)) {
		return checkExecutable(command)
	}
	if _, err := exec.LookPath(command); err == nil {
		return nil
	}
	// Pass the name as a positional argument so it is never parsed as shell code.
	shell, args := loginShell()
	args = append(args, `command -v "$1"`, shell, command)
	if err := exec.Command(shell, args...).Run(); err != nil {
		return fmt.Errorf("command %q not found in PATH", command)
	}
	return nil
}

func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("command %q does not exist", path)
	}
	if info.IsDir() {
		return fmt.Errorf("command %q is a directory", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("command %q is not executable", path)
	}
	return nil
}

func runCommand(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
