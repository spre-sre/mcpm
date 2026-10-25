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

func TestCheckCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	dir := t.TempDir()
	exe := filepath.Join(dir, "server")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		command string
		wantErr bool
	}{
		{"executable file", exe, false},
		{"bare name on PATH", "sh", false},
		{"empty", "  ", true},
		{"missing file", filepath.Join(dir, "missing"), true},
		{"directory", dir, true},
		{"not executable", plain, true},
		{"unknown bare name", "nonexistent-command-xyz-12345", true},
		{"shell code is not run", "x; touch " + filepath.Join(dir, "INJECTED"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckCommand(tt.command)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckCommand(%q) error = %v, wantErr %v", tt.command, err, tt.wantErr)
			}
		})
	}

	if _, err := os.Stat(filepath.Join(dir, "INJECTED")); err == nil {
		t.Fatal("INJECTED file should not exist")
	}
}

func TestCheckCommandLoginShellInjection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "INJECTED")
	// No path separator in the name, so it reaches the login shell lookup.
	t.Setenv("MCPM_TEST_MARKER", marker)
	name := "nope$(touch${IFS}$MCPM_TEST_MARKER)"
	if err := CheckCommand(name); err == nil {
		t.Fatalf("CheckCommand(%q) should fail", name)
	}
	if _, err := os.Stat(marker); err == nil {
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
