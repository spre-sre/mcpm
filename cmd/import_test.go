package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"mcpm/internal/builder"
	"mcpm/internal/injector"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

// commitMore commits the given files on top of the repository at dir.
func commitMore(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	writeFiles(t, dir, files)
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.AddGlob("."); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}
	hash, err := wt.Commit("more", &git.CommitOptions{Author: sig})
	if err != nil {
		t.Fatal(err)
	}
	return hash.String()
}

type registerCall struct {
	result *builder.BuildResult
	tools  []injector.TargetTool
	global bool
}

// fakeInstall replaces cloneRepo with a function that runs clone and
// registerServer with one that records its calls.
func fakeInstall(t *testing.T, clone func(url string) (string, error)) *[]registerCall {
	t.Helper()
	oldClone, oldRegister := cloneRepo, registerServer
	t.Cleanup(func() { cloneRepo, registerServer = oldClone, oldRegister })
	var calls []registerCall
	cloneRepo = clone
	registerServer = func(r *builder.BuildResult, tools []injector.TargetTool, env map[string]string, global bool) error {
		if env != nil {
			t.Errorf("import must not pass environment variables, got %v", env)
		}
		calls = append(calls, registerCall{r, tools, global})
		return nil
	}
	return &calls
}

func TestRunImportChecksOutCommit(t *testing.T) {
	dir := chdirTemp(t)
	srv := filepath.Join(dir, ".mcp", "servers", "alpha")
	writeFiles(t, srv, map[string]string{"mcp.json": `{"runCmd":"node","args":["index.js"],"requiredEnv":["API_KEY"]}`, "index.js": ""})
	first := commitAll(t, srv)
	commitMore(t, srv, map[string]string{"mcp.json": `{"runCmd":"node","args":["v2.js"]}`, "v2.js": ""})
	calls := fakeInstall(t, func(url string) (string, error) {
		if url != "https://example.com/org/alpha.git" {
			t.Errorf("clone url %q", url)
		}
		return srv, nil
	})

	lock := lockFile{Servers: []lockEntry{{
		Name: "alpha", Repository: "https://example.com/org/alpha.git", Commit: first,
		Targets: []injector.TargetTool{injector.TargetGeminiCLI},
	}}}
	var buf bytes.Buffer
	if err := runImport(&buf, lock, true); err != nil {
		t.Fatal(err)
	}

	if got := headCommit(srv); got != first {
		t.Fatalf("HEAD = %s, want %s", got, first)
	}
	if _, err := os.Stat(filepath.Join(srv, "v2.js")); !os.IsNotExist(err) {
		t.Fatalf("v2.js should be gone after checkout: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("register calls: %+v", *calls)
	}
	c := (*calls)[0]
	if c.result.Command != "node" || !slices.Equal(c.result.Args, []string{filepath.Join(srv, "index.js")}) {
		t.Errorf("registered %q %v", c.result.Command, c.result.Args)
	}
	if !slices.Equal(c.tools, []injector.TargetTool{injector.TargetGeminiCLI}) || !c.global {
		t.Errorf("registered tools %v global %v", c.tools, c.global)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "Installed alpha at "+first[:12]+"\n") || !strings.Contains(out, "  alpha: API_KEY\n") {
		t.Fatalf("output %q", out)
	}
}

func TestRunImportNoTargetsSkipsRegister(t *testing.T) {
	dir := chdirTemp(t)
	srv := filepath.Join(dir, ".mcp", "servers", "alpha")
	writeFiles(t, srv, map[string]string{"mcp.json": `{"runCmd":"node"}`})
	commit := commitAll(t, srv)
	calls := fakeInstall(t, func(string) (string, error) { return srv, nil })

	lock := lockFile{Servers: []lockEntry{{Name: "alpha", Repository: "https://example.com/alpha", Commit: commit}}}
	var buf bytes.Buffer
	if err := runImport(&buf, lock, false); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("register called: %+v", *calls)
	}
}

func TestRunImportErrors(t *testing.T) {
	dir := chdirTemp(t)
	srv := filepath.Join(dir, ".mcp", "servers", "alpha")
	writeFiles(t, srv, map[string]string{"README": ""})
	commit := commitAll(t, srv)
	entry := lockEntry{Name: "alpha", Repository: "https://example.com/alpha", Commit: commit}
	lock := lockFile{Servers: []lockEntry{entry}}

	fakeInstall(t, func(string) (string, error) { return "", errors.New("offline") })
	if err := runImport(&bytes.Buffer{}, lock, false); err == nil || err.Error() != "server 'alpha': offline" {
		t.Fatalf("clone failure: got %v", err)
	}

	cloneRepo = func(string) (string, error) { return filepath.Join(dir, "other"), nil }
	if err := runImport(&bytes.Buffer{}, lock, false); err == nil || !strings.Contains(err.Error(), "want .mcp/servers/alpha") {
		t.Fatalf("wrong folder: got %v", err)
	}

	cloneRepo = func(string) (string, error) { return srv, nil }
	if err := runImport(&bytes.Buffer{}, lock, false); err == nil || !strings.Contains(err.Error(), "no project detected") {
		t.Fatalf("undetected project: got %v", err)
	}
}

func TestReadLockValidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, lockFileName)
	if _, err := readLock(path); err == nil || !strings.HasPrefix(err.Error(), "could not read ") {
		t.Fatalf("missing file: got %v", err)
	}
	cases := []struct{ content, want string }{
		{`{`, "could not parse"},
		{`{"servers":[{"name":"a","commit":"` + testCommit + `"}]}`, "has no repository"},
		{`{"servers":[{"name":"a","repository":"https://x/b.git","commit":"` + testCommit + `"}]}`, "installs as 'b'"},
		{`{"servers":[{"name":"a","repository":"https://x/a.git","commit":"main"}]}`, "invalid commit"},
		{`{"servers":[{"name":"a","repository":"https://x/a/","commit":"` + testCommit + `","targets":["vim"]}]}`, "unknown target"},
	}
	for _, c := range cases {
		writeFiles(t, dir, map[string]string{lockFileName: c.content})
		if _, err := readLock(path); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.content, err, c.want)
		}
	}
	writeFiles(t, dir, map[string]string{lockFileName: `{"servers":[{"name":"a","repository":"https://x/a.git","commit":"` + testCommit + `","targets":["claude-code"]}]}`})
	lock, err := readLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Servers) != 1 || lock.Servers[0].Targets[0] != injector.TargetClaudeCode {
		t.Fatalf("got %+v", lock)
	}
}

func TestImportFlags(t *testing.T) {
	f := importCmd.Flags().Lookup("file")
	if f == nil || f.DefValue != lockFileName {
		t.Fatalf("--file: got %+v", f)
	}
	if importCmd.Flags().Lookup("global") == nil {
		t.Fatal("import has no --global flag")
	}
}
