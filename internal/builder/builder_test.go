package builder

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A directory named mcp.json is a broken manifest, not a missing one: it must
// not fall through to the Node, Python or Go builders.
func TestDetectAndBuildManifestDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "mcp.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := DetectAndBuild(dir)
	if err == nil || !strings.HasPrefix(err.Error(), "invalid mcp.json:") || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("got %v, want an invalid mcp.json error that names the directory", err)
	}
}

func TestDetectAndBuildManifestUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads files regardless of mode")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"runCmd":"node"}`), 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := DetectAndBuild(dir)
	if err == nil || !strings.HasPrefix(err.Error(), "could not read mcp.json:") {
		t.Fatalf("got %v, want a could not read mcp.json error", err)
	}
}

func TestDetectAndBuildUndetectedNamesEveryMarker(t *testing.T) {
	_, err := DetectAndBuild(t.TempDir())
	if err == nil {
		t.Fatal("got nil, want an error")
	}
	for _, marker := range []string{"mcp.json", "package.json", "pyproject.toml", "requirements.txt", "go.mod"} {
		if !strings.Contains(err.Error(), marker) {
			t.Errorf("error %q does not name %s", err, marker)
		}
	}
}

// A path that cannot be made absolute must be reported, not turned into a
// search relative to an empty path.
func TestDetectAndBuildAbsError(t *testing.T) {
	defer func(saved func(string) (string, error)) { resolveAbs = saved }(resolveAbs)
	resolveAbs = func(string) (string, error) { return "", errors.New("getwd: no such file or directory") }
	_, err := DetectAndBuild("repo")
	if err == nil || !strings.HasPrefix(err.Error(), "could not resolve repository path") {
		t.Fatalf("got %v, want a could not resolve repository path error", err)
	}
}
