package fetcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClone_TrailingSlash(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	os.Chdir(tmp)

	os.MkdirAll(filepath.Join(tmp, ".mcp", "servers", "repo"), 0755)

	got, err := Clone("https://github.com/org/repo/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(tmp, ".mcp", "servers", "repo")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestClone_TrailingSlashDotGit(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	os.Chdir(tmp)

	os.MkdirAll(filepath.Join(tmp, ".mcp", "servers", "repo"), 0755)

	got, err := Clone("https://github.com/org/repo.git/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(tmp, ".mcp", "servers", "repo")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestClone_EmptyURL(t *testing.T) {
	tmp := t.TempDir()
	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	os.Chdir(tmp)

	os.MkdirAll(filepath.Join(tmp, ".mcp", "servers"), 0755)

	_, err := Clone("")
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
	if !strings.Contains(err.Error(), "cannot derive repository name from URL") {
		t.Fatalf("unexpected error message: %v", err)
	}

	entries, _ := os.ReadDir(filepath.Join(tmp, ".mcp", "servers"))
	if len(entries) != 0 {
		t.Fatalf("expected no entries under .mcp/servers, got %d", len(entries))
	}
}

func TestClone_SlashOnly(t *testing.T) {
	tmp := t.TempDir()
	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	os.Chdir(tmp)

	os.MkdirAll(filepath.Join(tmp, ".mcp", "servers"), 0755)

	_, err := Clone("/")
	if err == nil {
		t.Fatal("expected error for /")
	}
	if !strings.Contains(err.Error(), "cannot derive repository name from URL") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestClone_DotGitOnly(t *testing.T) {
	tmp := t.TempDir()
	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	os.Chdir(tmp)

	os.MkdirAll(filepath.Join(tmp, ".mcp", "servers"), 0755)

	_, err := Clone(".git")
	if err == nil {
		t.Fatal("expected error for .git")
	}
	if !strings.Contains(err.Error(), "cannot derive repository name from URL") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestClone_DotDotTraversal(t *testing.T) {
	tmp := t.TempDir()
	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	os.Chdir(tmp)

	os.MkdirAll(filepath.Join(tmp, ".mcp", "servers"), 0755)

	_, err := Clone("https://host/org/..")
	if err == nil {
		t.Fatal("expected error for .. name")
	}
	if !strings.Contains(err.Error(), "cannot derive repository name from URL") {
		t.Fatalf("unexpected error message: %v", err)
	}

	entries, _ := os.ReadDir(filepath.Join(tmp, ".mcp", "servers"))
	if len(entries) != 0 {
		t.Fatalf("expected no entries under .mcp/servers, got %d", len(entries))
	}
}
