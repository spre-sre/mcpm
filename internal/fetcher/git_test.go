package fetcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
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

func TestClone_LocalRepo_Fast(t *testing.T) {
	srcDir, _ := filepath.EvalSymlinks(t.TempDir())
	repo, err := git.PlainInit(srcDir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}

	testFile := filepath.Join(srcDir, "hello.txt")
	if err := os.WriteFile(testFile, []byte("hello\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if _, err := wt.Add("hello.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	sig := &object.Signature{
		Name:  "Test",
		Email: "test@test.com",
		When:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if _, err := wt.Commit("init", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	cwdDir, _ := filepath.EvalSymlinks(t.TempDir())
	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	os.Chdir(cwdDir)

	start := time.Now()
	got, err := Clone("file://" + srcDir)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Clone returned error: %v", err)
	}

	evalCwd, _ := filepath.EvalSymlinks(cwdDir)
	repoName, _ := repoNameFromURL("file://" + srcDir)
	want := filepath.Join(evalCwd, ".mcp", "servers", repoName)
	if got != want {
		t.Fatalf("path: got %q, want %q", got, want)
	}

	if _, err := os.Stat(filepath.Join(got, "hello.txt")); err != nil {
		t.Fatalf("committed file not found in clone: %v", err)
	}

	if elapsed >= 450*time.Millisecond {
		t.Fatalf("Clone took %v, expected < 450ms", elapsed)
	}
}
