package fetcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupServers(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func TestRemoveServerDeletesTree(t *testing.T) {
	dir := setupServers(t)
	sub := filepath.Join(dir, ".mcp", "servers", "x", "a", "b")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "f.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveServer("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".mcp", "servers", "x")); !os.IsNotExist(err) {
		t.Fatalf("server directory still exists: %v", err)
	}
}

func TestRemoveServerRefusesBadNames(t *testing.T) {
	dir := setupServers(t)
	sibling := filepath.Join(dir, ".mcp", "servers", "keep")
	if err := os.MkdirAll(sibling, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "../keep"} {
		if err := RemoveServer(name); err == nil {
			t.Errorf("RemoveServer(%q) succeeded, want error", name)
		}
		if _, err := os.Stat(sibling); err != nil {
			t.Fatalf("sibling gone after RemoveServer(%q): %v", name, err)
		}
	}
}

func TestRemoveServerRefusesFile(t *testing.T) {
	dir := setupServers(t)
	servers := filepath.Join(dir, ".mcp", "servers")
	if err := os.MkdirAll(servers, 0755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(servers, "file")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveServer("file"); err == nil {
		t.Fatal("want error for non-directory")
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("file removed: %v", err)
	}
}

func TestRemoveServerRefusesSymlink(t *testing.T) {
	dir := setupServers(t)
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(marker, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	servers := filepath.Join(dir, ".mcp", "servers")
	if err := os.MkdirAll(servers, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(servers, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := RemoveServer("link"); err == nil {
		t.Fatal("want error for symlink")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("symlink target was touched: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("symlink removed: %v", err)
	}
}

func TestServerDirToRemoveChangesNothing(t *testing.T) {
	dir := setupServers(t)
	server := filepath.Join(dir, ".mcp", "servers", "x")
	if err := os.MkdirAll(server, 0755); err != nil {
		t.Fatal(err)
	}
	got, err := ServerDirToRemove("x")
	if err != nil {
		t.Fatal(err)
	}
	if mustEval(t, got) != mustEval(t, server) {
		t.Errorf("got %q, want %q", got, server)
	}
	if _, err := os.Stat(server); err != nil {
		t.Fatalf("server directory touched: %v", err)
	}
	if _, err := ServerDirToRemove("nope"); err == nil {
		t.Error("want error for missing server")
	}
	if _, err := ServerDirToRemove("../x"); err == nil {
		t.Error("want error for invalid name")
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRemoveServerNotFound(t *testing.T) {
	setupServers(t)
	err := RemoveServer("nope")
	if err == nil {
		t.Fatal("want error")
	}
	want := "server 'nope' not found in .mcp/servers/"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}
