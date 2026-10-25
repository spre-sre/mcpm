package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
)

// chdirTemp switches into a fresh temp dir so fetcher.Clone writes its
// .mcp/servers tree there, and restores the working directory afterwards.
func chdirTemp(t *testing.T) string {
	t.Helper()
	// Resolve symlinks (e.g. /var -> /private/var on macOS) so paths match
	// what os.Getwd reports after the chdir.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return dir
}

func TestFetchRepoCmdInvalidURL(t *testing.T) {
	chdirTemp(t)
	msg := fetchRepoCmd("https://example.com/..")()
	if _, ok := msg.(msgError); !ok {
		t.Fatalf("expected msgError, got %T", msg)
	}
}

func TestFetchRepoCmdExistingRepo(t *testing.T) {
	dir := chdirTemp(t)
	url := "https://example.com/owner/repo.git"
	target := filepath.Join(dir, ".mcp", "servers", "repo")
	repo, err := git.PlainInit(target, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}}); err != nil {
		t.Fatal(err)
	}

	msg := fetchRepoCmd(url)()
	got, ok := msg.(msgRepoFetched)
	if !ok {
		t.Fatalf("expected msgRepoFetched, got %T (%v)", msg, msg)
	}
	if got.path != target {
		t.Fatalf("expected path %q, got %q", target, got.path)
	}
}

func TestFetchRepoCmdExistingRepoOtherOrigin(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, ".mcp", "servers", "repo")
	repo, err := git.PlainInit(target, false)
	if err != nil {
		t.Fatal(err)
	}
	other := "https://example.com/someone-else/repo.git"
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{other}}); err != nil {
		t.Fatal(err)
	}

	msg := fetchRepoCmd("https://example.com/owner/repo.git")()
	if _, ok := msg.(msgError); !ok {
		t.Fatalf("expected msgError, got %T", msg)
	}
}

func TestBuildRepoCmdManifest(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"runCmd": "echo", "args": ["hello"]}`
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	msg := buildRepoCmd(dir)()
	got, ok := msg.(msgBuilt)
	if !ok {
		t.Fatalf("expected msgBuilt, got %T (%v)", msg, msg)
	}
	if got.result == nil || got.result.Command == "" {
		t.Fatalf("expected a build result with a command, got %+v", got.result)
	}
}

func TestBuildRepoCmdUnknownProject(t *testing.T) {
	msg := buildRepoCmd(t.TempDir())()
	got, ok := msg.(msgBuilt)
	if !ok {
		t.Fatalf("expected msgBuilt, got %T (%v)", msg, msg)
	}
	if got.result == nil || got.result.Command != "" || len(got.result.Args) != 0 {
		t.Fatalf("expected an empty build result, got %+v", got.result)
	}
}

func TestBuildRepoCmdInvalidManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(`{"runCmd": ""}`), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := buildRepoCmd(dir)()
	if _, ok := msg.(msgError); !ok {
		t.Fatalf("expected msgError, got %T", msg)
	}
}
