package fetcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestClone_TrailingSlash(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	orig, _ := os.Getwd()
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	initWithOrigin(t, filepath.Join(tmp, ".mcp", "servers", "repo"), "https://github.com/org/repo")

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
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	initWithOrigin(t, filepath.Join(tmp, ".mcp", "servers", "repo"), "https://github.com/org/repo")

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
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(tmp, ".mcp", "servers"), 0755); err != nil {
		t.Fatal(err)
	}

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
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(tmp, ".mcp", "servers"), 0755); err != nil {
		t.Fatal(err)
	}

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
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(tmp, ".mcp", "servers"), 0755); err != nil {
		t.Fatal(err)
	}

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
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(tmp, ".mcp", "servers"), 0755); err != nil {
		t.Fatal(err)
	}

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

func initWithOrigin(t *testing.T, dir, url string) {
	t.Helper()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
}

func makeSourceRepo(t *testing.T, dir string) {
	t.Helper()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("hello.txt"); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "Test", Email: "test@test.com", When: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	if _, err := wt.Commit("init", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatal(err)
	}
}

func chdirTemp(t *testing.T) string {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	orig, _ := os.Getwd()
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestClone_SameURLTwice(t *testing.T) {
	src, _ := filepath.EvalSymlinks(t.TempDir())
	makeSourceRepo(t, src)
	chdirTemp(t)

	first, err := Clone("file://" + src)
	if err != nil {
		t.Fatalf("first Clone: %v", err)
	}
	second, err := Clone("file://" + src)
	if err != nil {
		t.Fatalf("second Clone: %v", err)
	}
	if first != second {
		t.Fatalf("paths differ: %q vs %q", first, second)
	}
}

func TestClone_DifferentRepoSameName(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	srcA := filepath.Join(root, "alice", "tools")
	srcB := filepath.Join(root, "bob", "tools")
	for _, d := range []string{srcA, srcB} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		makeSourceRepo(t, d)
	}
	chdirTemp(t)

	first, err := Clone("file://" + srcA)
	if err != nil {
		t.Fatalf("first Clone: %v", err)
	}
	head := func() string {
		r, err := git.PlainOpen(first)
		if err != nil {
			t.Fatal(err)
		}
		h, err := r.Head()
		if err != nil {
			t.Fatal(err)
		}
		return h.Hash().String()
	}
	before := head()

	_, err = Clone("file://" + srcB)
	if err == nil {
		t.Fatal("expected error for different repository with same name")
	}
	want := "server 'tools' is already installed from file://" + srcA
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
	if after := head(); after != before {
		t.Fatalf("existing clone changed: %s -> %s", before, after)
	}
	if _, err := os.Stat(filepath.Join(first, "hello.txt")); err != nil {
		t.Fatalf("existing clone modified: %v", err)
	}
}

func TestClone_ExistingNotGitRepo(t *testing.T) {
	tmp := chdirTemp(t)
	dir := filepath.Join(tmp, ".mcp", "servers", "repo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	_, err := Clone("https://github.com/org/repo")
	if err == nil {
		t.Fatal("expected error for non-git directory")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("existing directory changed: %v", statErr)
	}
}

func TestClone_ExistingNoOrigin(t *testing.T) {
	tmp := chdirTemp(t)
	dir := filepath.Join(tmp, ".mcp", "servers", "repo")
	if _, err := git.PlainInit(dir, false); err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep\n"), 0644); err != nil {
		t.Fatal(err)
	}
	listing := func() []string {
		var names []string
		err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			names = append(names, p)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return names
	}
	before := listing()

	_, err := Clone("file://" + filepath.ToSlash(filepath.Join(tmp, "origin", "repo")))
	if err == nil {
		t.Fatal("expected error for repository without origin")
	}
	if !strings.Contains(err.Error(), "has no origin remote") {
		t.Fatalf("unexpected error: %v", err)
	}

	after := listing()
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("directory changed: %v -> %v", before, after)
	}
	data, err := os.ReadFile(filepath.Join(dir, "keep.txt"))
	if err != nil || string(data) != "keep\n" {
		t.Fatalf("keep.txt changed: %q, %v", data, err)
	}
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	remotes, err := repo.Remotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(remotes) != 0 {
		t.Fatalf("expected no remotes, got %d", len(remotes))
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
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatal(err)
	}

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
