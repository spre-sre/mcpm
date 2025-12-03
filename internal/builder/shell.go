package builder

import (
	"fmt"
	"os"
	"os/exec"
)

func runShellCmd(dir string, command string) error {
	if command == "" {
		return nil
	}

	// Use user's shell with login/interactive flags to load profile (needed for nvm, etc.)
	// Priority: zsh (macOS default) -> bash -> sh
	shell := "sh"
	args := []string{"-c", command}

	if _, err := exec.LookPath("zsh"); err == nil {
		shell = "zsh"
		args = []string{"-l", "-c", command} // -l for login (loads profile), no -i to avoid job control issues
	} else if _, err := exec.LookPath("bash"); err == nil {
		shell = "bash"
		args = []string{"-l", "-c", command}
	}

	cmd := exec.Command(shell, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ() // Inherit current environment

	// Capture output for debugging
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
