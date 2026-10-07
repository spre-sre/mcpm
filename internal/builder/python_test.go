package builder

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type call struct {
	dir     string
	command string
}

func TestEnsureVenvSkipsWhenExists(t *testing.T) {
	dir := t.TempDir()
	venv := filepath.Join(dir, ".venv")
	binDir := filepath.Join(venv, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "python"), []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = /usr"), 0o644); err != nil {
		t.Fatal(err)
	}

	var calls []call
	run := func(d, c string) error {
		calls = append(calls, call{d, c})
		return nil
	}

	if err := ensureVenv(dir, run); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected no calls, got %d: %v", len(calls), calls)
	}
}

func TestEnsureVenvCreatesWhenMissing(t *testing.T) {
	dir := t.TempDir()

	var calls []call
	run := func(d, c string) error {
		calls = append(calls, call{d, c})
		return nil
	}

	if err := ensureVenv(dir, run); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(calls), calls)
	}
	if calls[0].command != "python3 -m venv .venv" {
		t.Fatalf("got command %q, want %q", calls[0].command, "python3 -m venv .venv")
	}
}

func TestEnsureVenvFallback(t *testing.T) {
	dir := t.TempDir()

	var calls []call
	run := func(d, c string) error {
		calls = append(calls, call{d, c})
		if c == "python3 -m venv .venv" {
			return errors.New("python3 not found")
		}
		return nil
	}

	if err := ensureVenv(dir, run); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %v", len(calls), calls)
	}
	if calls[1].command != "python -m venv .venv" {
		t.Fatalf("got command %q, want %q", calls[1].command, "python -m venv .venv")
	}
}

func TestEnsureVenvBothFail(t *testing.T) {
	dir := t.TempDir()

	run := func(d, c string) error {
		return errors.New("no python")
	}

	err := ensureVenv(dir, run)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), "failed to create venv") {
		t.Fatalf("got %q, want prefix 'failed to create venv'", err.Error())
	}
}

func TestEnsureVenvDanglingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink test not applicable on Windows")
	}

	dir := t.TempDir()
	venv := filepath.Join(dir, ".venv")
	binDir := filepath.Join(venv, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent/python3", filepath.Join(binDir, "python")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = /usr"), 0o644); err != nil {
		t.Fatal(err)
	}

	var calls []call
	run := func(d, c string) error {
		calls = append(calls, call{d, c})
		return nil
	}

	if err := ensureVenv(dir, run); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) == 0 {
		t.Fatal("expected runner to be called for dangling symlink, got 0 calls")
	}
}
