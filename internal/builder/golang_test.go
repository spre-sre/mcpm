package builder

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func goBinName() string {
	if runtime.GOOS == "windows" {
		return "mcp-server.exe"
	}
	return "mcp-server"
}

func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	t.Setenv("GOWORK", "off")
}

func TestBuildGoSuccess(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.18\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := buildGo(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(dir, goBinName())
	if res.Command != want {
		t.Fatalf("got Command=%q, want %q", res.Command, want)
	}
	if res.Args == nil || len(res.Args) != 0 {
		t.Fatalf("got Args=%v, want empty non-nil slice", res.Args)
	}
	if res.EnvNeeds == nil || len(res.EnvNeeds) != 0 {
		t.Fatalf("got EnvNeeds=%v, want empty non-nil slice", res.EnvNeeds)
	}
	if !exists(want) {
		t.Fatalf("binary %q was not built", want)
	}
}

func TestBuildGoFailure(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.18\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := buildGo(dir)
	if err == nil {
		t.Fatal("expected error for broken source")
	}
	if res != nil {
		t.Fatalf("got result %+v, want nil", res)
	}
	if exists(filepath.Join(dir, goBinName())) {
		t.Fatal("binary should not exist")
	}
}
