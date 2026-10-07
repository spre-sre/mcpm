package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPackageJSONValid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "package.json")
	if err := os.WriteFile(p, []byte(`{"name":"demo","main":"server.js"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, err := readPackageJSON(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pkg.Main != "server.js" {
		t.Fatalf("got Main=%q, want server.js", pkg.Main)
	}
	if pkg.Name != "demo" {
		t.Fatalf("got Name=%q, want demo", pkg.Name)
	}
}

func TestReadPackageJSONMissing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "package.json")
	_, err := readPackageJSON(p)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.HasPrefix(err.Error(), "could not read ") {
		t.Fatalf("got %q, want prefix 'could not read '", err.Error())
	}
}

func TestReadPackageJSONBadType(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "package.json")
	if err := os.WriteFile(p, []byte(`{"main":5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readPackageJSON(p)
	if err == nil {
		t.Fatal("expected error for bad main type")
	}
	if !strings.HasPrefix(err.Error(), "invalid package.json at ") {
		t.Fatalf("got %q, want prefix 'invalid package.json at '", err.Error())
	}
}

func TestDetectAndBuildBadPackageJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"main":5}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := DetectAndBuild(dir)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), "invalid package.json at ") {
		t.Fatalf("got %q, want prefix 'invalid package.json at '", err.Error())
	}

	if exists(filepath.Join(dir, "node_modules")) {
		t.Fatal("node_modules should not exist")
	}
	if exists(filepath.Join(dir, "package-lock.json")) {
		t.Fatal("package-lock.json should not exist")
	}
}

func TestFindMonorepoEntryBadPackageJSON(t *testing.T) {
	dir := t.TempDir()
	mcpDir := filepath.Join(dir, "packages", "mcp")
	srcDir := filepath.Join(mcpDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mcpDir, "package.json"), []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(srcDir, "index.js")
	if err := os.WriteFile(indexPath, []byte(`// entry`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := findMonorepoEntry(dir)
	want := filepath.Join(dir, "packages", "mcp", "src", "index.js")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
