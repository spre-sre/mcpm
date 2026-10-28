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

func TestRunImportListsUnsetEnv(t *testing.T) {
	dir := chdirTemp(t)
	srv := filepath.Join(dir, ".mcp", "servers", "alpha")
	writeFiles(t, srv, map[string]string{"mcp.json": `{"runCmd":"node","requiredEnv":["API_KEY"]}`})
	commit := commitAll(t, srv)
	fakeInstall(t, func(string) (string, error) { return srv, nil })
	writeEnvRegistrations(t, dir, "alpha",
		envServerDef{Command: "node", Env: map[string]string{"TOKEN": "x"}},
		envServerDef{Command: "node"})

	entry := lockEntry{Name: "alpha", Repository: "https://example.com/alpha", Commit: commit, Env: []string{"TOKEN", "REGION"}}
	cases := []struct {
		targets []injector.TargetTool
		want    string
	}{
		{[]injector.TargetTool{injector.TargetClaudeCode}, "  alpha: API_KEY, REGION\n"},
		{[]injector.TargetTool{injector.TargetClaudeCode, injector.TargetGeminiCLI}, "  alpha: API_KEY, REGION, TOKEN\n"},
		{nil, "  alpha: API_KEY, REGION, TOKEN\n"},
	}
	for _, c := range cases {
		entry.Targets = c.targets
		var buf bytes.Buffer
		if err := runImport(&buf, lockFile{Servers: []lockEntry{entry}}, false); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), "mcpm env set") || !strings.HasSuffix(buf.String(), c.want) {
			t.Errorf("targets %v: output %q, want suffix %q", c.targets, buf.String(), c.want)
		}
	}

	entry.Env = []string{"API_KEY", "TOKEN"}
	entry.Targets = []injector.TargetTool{injector.TargetClaudeCode}
	writeEnvRegistrations(t, dir, "alpha",
		envServerDef{Command: "node", Env: map[string]string{"TOKEN": "x", "API_KEY": "y"}},
		envServerDef{Command: "node"})
	var buf bytes.Buffer
	if err := runImport(&buf, lockFile{Servers: []lockEntry{entry}}, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "need environment variables") {
		t.Errorf("all set, but output %q", buf.String())
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

func TestRunImportKeepsPin(t *testing.T) {
	dir := chdirTemp(t)
	srv := filepath.Join(dir, ".mcp", "servers", "alpha")
	writeFiles(t, srv, map[string]string{"mcp.json": `{"runCmd":"node"}`})
	first := commitAll(t, srv)
	commitMore(t, srv, map[string]string{"v2.js": ""})
	fakeInstall(t, func(string) (string, error) { return srv, nil })
	entry := lockEntry{Name: "alpha", Repository: "https://example.com/alpha", Commit: first, Pin: "refs/tags/v1.2.0"}

	var buf bytes.Buffer
	if err := runImport(&buf, lockFile{Servers: []lockEntry{entry}}, false); err != nil {
		t.Fatal(err)
	}
	if pin, err := serverPin(srv); err != nil || pin != "refs/tags/v1.2.0" {
		t.Fatalf("serverPin = %q, %v", pin, err)
	}
	if got := headCommit(srv); got != first {
		t.Fatalf("HEAD = %s, want %s", got, first)
	}
	if want := "Installed alpha at " + first[:12] + " [pinned v1.2.0]\n"; !strings.HasPrefix(buf.String(), want) {
		t.Fatalf("output %q, want prefix %q", buf.String(), want)
	}

	entry.Pin = "refs/heads/stable"
	if err := runImport(&bytes.Buffer{}, lockFile{Servers: []lockEntry{entry}}, false); err != nil {
		t.Fatal(err)
	}
	repo, err := git.PlainOpen(srv)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil || head.Name() != "refs/heads/stable" || head.Hash().String() != first {
		t.Fatalf("HEAD = %v, %v; want branch stable at %s", head, err, first)
	}
	if pin, _ := serverPin(srv); pin != "refs/heads/stable" {
		t.Fatalf("serverPin = %q", pin)
	}

	entry.Pin = ""
	if err := runImport(&bytes.Buffer{}, lockFile{Servers: []lockEntry{entry}}, false); err != nil {
		t.Fatal(err)
	}
	if pin, err := serverPin(srv); err != nil || pin != "" {
		t.Fatalf("unpinned entry kept pin %q, %v", pin, err)
	}
}

func TestValidPin(t *testing.T) {
	for _, pin := range []string{"", "refs/tags/v1.2.0", "refs/heads/stable", "refs/heads/release/1.x"} {
		if !validPin(pin) {
			t.Errorf("validPin(%q) = false", pin)
		}
	}
	for _, pin := range []string{"v1.2.0", "refs/remotes/origin/main", "refs/tags/", "refs/heads/../../x", "refs/heads/a..b",
		"refs/heads/.hidden", "refs/heads/a/", "refs/heads/x.lock", "refs/tags/a b", "refs/tags/a~1", "refs/tags/a@{1}", "refs/tags/a\n"} {
		if validPin(pin) {
			t.Errorf("validPin(%q) = true", pin)
		}
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
		{`{"servers":[{"name":"a","repository":"https://x/a.git","commit":"` + testCommit + `","env":["A-B"]}]}`, "invalid environment variable name"},
		{`{"servers":[{"name":"a","repository":"https://x/a.git","commit":"` + testCommit + `","pin":"refs/heads/../x"}]}`, "invalid pin"},
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
