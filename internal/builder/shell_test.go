package builder

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunCommandNoShellInjection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	dir := t.TempDir()

	err := runCommand(dir, "sh", "-c", "printf '%s' \"$1\" > out.txt", "sh", "a b;touch INJECTED")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("failed to read out.txt: %v", err)
	}
	if string(data) != "a b;touch INJECTED" {
		t.Fatalf("got %q, want %q", string(data), "a b;touch INJECTED")
	}

	if _, err := os.Stat(filepath.Join(dir, "INJECTED")); err == nil {
		t.Fatal("INJECTED file should not exist")
	}
}

func TestRunCommandNotFound(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	err := runCommand(t.TempDir(), "nonexistent-command-xyz-12345")
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
}
