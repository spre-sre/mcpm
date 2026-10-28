package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"mcpm/internal/injector"
)

// gitServer makes dir a git repository with one commit and the given origin
// URL, and returns the commit hash.
func gitServer(t *testing.T, dir, origin string, files map[string]string) string {
	t.Helper()
	writeFiles(t, dir, files)
	commit := commitAll(t, dir)
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{origin}}); err != nil {
		t.Fatal(err)
	}
	return commit
}

func TestBuildLock(t *testing.T) {
	dir := chdirTemp(t)
	servers := filepath.Join(dir, ".mcp", "servers")
	alpha := gitServer(t, filepath.Join(servers, "alpha"), "https://example.com/org/alpha.git", map[string]string{"go.mod": "module a\n"})
	beta := gitServer(t, filepath.Join(servers, "beta"), "https://example.com/org/beta", map[string]string{"go.mod": "module b\n"})
	writeRegistrations(t, dir, map[string]registration{"alpha": {Command: "x"}})

	lock, err := buildLock(false)
	if err != nil {
		t.Fatal(err)
	}
	want := []lockEntry{
		{Name: "alpha", Repository: "https://example.com/org/alpha.git", Commit: alpha,
			Targets: []injector.TargetTool{injector.TargetClaudeCode, injector.TargetGeminiCLI}},
		{Name: "beta", Repository: "https://example.com/org/beta", Commit: beta, Targets: []injector.TargetTool{}},
	}
	if len(lock.Servers) != len(want) {
		t.Fatalf("got %+v, want %+v", lock.Servers, want)
	}
	for i, w := range want {
		got := lock.Servers[i]
		if got.Name != w.Name || got.Repository != w.Repository || got.Commit != w.Commit || !slices.Equal(got.Targets, w.Targets) {
			t.Errorf("entry %d: got %+v, want %+v", i, got, w)
		}
	}

	path := filepath.Join(dir, lockFileName)
	if err := writeLock(path, lock); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"targets": []`) {
		t.Fatalf("beta targets should be an empty array: %s", data)
	}
	back, err := readLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Servers) != 2 || back.Servers[0].Commit != alpha || back.Servers[1].Name != "beta" {
		t.Fatalf("round trip: got %+v", back.Servers)
	}
}

func TestBuildLockRecordsUV(t *testing.T) {
	dir := chdirTemp(t)
	servers := filepath.Join(dir, ".mcp", "servers")
	gitServer(t, filepath.Join(servers, "locked"), "https://example.com/org/locked", map[string]string{"pyproject.toml": "[project]\n", "uv.lock": ""})
	gitServer(t, filepath.Join(servers, "pip"), "https://example.com/org/pip", map[string]string{"pyproject.toml": "[project]\n"})
	gitServer(t, filepath.Join(servers, "stray"), "https://example.com/org/stray", map[string]string{"requirements.txt": "", "uv.lock": ""})

	lock, err := buildLock(false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"locked": installerUV, "pip": "", "stray": ""}
	for _, e := range lock.Servers {
		if e.Installer != want[e.Name] {
			t.Errorf("%s: installer = %q, want %q", e.Name, e.Installer, want[e.Name])
		}
	}

	path := filepath.Join(dir, lockFileName)
	if err := writeLock(path, lock); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), `"installer"`); n != 1 {
		t.Fatalf("want installer only on the uv server, found %d: %s", n, data)
	}
}

func TestBuildLockRecordsPin(t *testing.T) {
	dir := chdirTemp(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	servers := filepath.Join(dir, ".mcp", "servers")
	gitServer(t, filepath.Join(servers, "pinned"), "https://example.com/org/pinned", map[string]string{"go.mod": "module p\n"})
	gitServer(t, filepath.Join(servers, "plain"), "https://example.com/org/plain", map[string]string{"go.mod": "module q\n"})
	repo, err := git.PlainOpen(filepath.Join(servers, "pinned"))
	if err != nil {
		t.Fatal(err)
	}
	if err := recordPin(repo, "refs/tags/v1.2.0"); err != nil {
		t.Fatal(err)
	}

	lock, err := buildLock(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Servers) != 2 || lock.Servers[0].Pin != "refs/tags/v1.2.0" || lock.Servers[1].Pin != "" {
		t.Fatalf("got %+v", lock.Servers)
	}
	path := filepath.Join(dir, lockFileName)
	if err := writeLock(path, lock); err != nil {
		t.Fatal(err)
	}
	back, err := readLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Servers[0].Pin != "refs/tags/v1.2.0" {
		t.Fatalf("round trip lost the pin: %+v", back.Servers[0])
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), `"pin"`); n != 1 {
		t.Fatalf("want pin only on the pinned server, found %d: %s", n, data)
	}
}

func TestBuildLockRecordsEnvNames(t *testing.T) {
	dir := chdirTemp(t)
	servers := filepath.Join(dir, ".mcp", "servers")
	gitServer(t, filepath.Join(servers, "alpha"), "https://example.com/org/alpha", map[string]string{"mcp.json": `{"runCmd":"node","requiredEnv":["API_KEY"]}`})
	gitServer(t, filepath.Join(servers, "beta"), "https://example.com/org/beta", map[string]string{"go.mod": "module b\n"})
	writeEnvRegistrations(t, dir, "alpha",
		envServerDef{Command: "node", Env: map[string]string{"TOKEN": "s3cret"}},
		envServerDef{Command: "node", Env: map[string]string{"API_KEY": "hunter2"}})

	lock, err := buildLock(false)
	if err != nil {
		t.Fatal(err)
	}
	if got := lock.Servers[0].Env; !slices.Equal(got, []string{"API_KEY", "TOKEN"}) {
		t.Errorf("alpha env = %v", got)
	}
	if got := lock.Servers[1].Env; got != nil {
		t.Errorf("beta env = %v", got)
	}
	if global, err := buildLock(true); err != nil || !slices.Equal(global.Servers[0].Env, []string{"API_KEY"}) {
		t.Errorf("global: got %+v, %v", global.Servers, err)
	}

	path := filepath.Join(dir, lockFileName)
	if err := writeLock(path, lock); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "s3cret") || strings.Contains(string(data), "hunter2") {
		t.Fatalf("env value written: %s", data)
	}
	if n := strings.Count(string(data), `"env"`); n != 1 {
		t.Fatalf("want env only on alpha, found %d: %s", n, data)
	}
	back, err := readLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(back.Servers[0].Env, []string{"API_KEY", "TOKEN"}) {
		t.Fatalf("round trip: got %+v", back.Servers)
	}
}

func TestBuildLockEmpty(t *testing.T) {
	chdirTemp(t)
	lock, err := buildLock(false)
	if err != nil {
		t.Fatal(err)
	}
	if lock.Servers == nil || len(lock.Servers) != 0 {
		t.Fatalf("want empty list, got %+v", lock.Servers)
	}
}

func TestBuildLockRejectsUntrackedServers(t *testing.T) {
	dir := chdirTemp(t)
	servers := filepath.Join(dir, ".mcp", "servers")
	writeFiles(t, filepath.Join(servers, "plain"), map[string]string{"go.mod": ""})
	if _, err := buildLock(false); err == nil || !strings.Contains(err.Error(), "server 'plain': not a git repository") {
		t.Fatalf("plain folder: got %v", err)
	}

	if err := os.RemoveAll(filepath.Join(servers, "plain")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, filepath.Join(servers, "local"), map[string]string{"go.mod": ""})
	commitAll(t, filepath.Join(servers, "local"))
	if _, err := buildLock(false); err == nil || !strings.Contains(err.Error(), "server 'local': no origin remote") {
		t.Fatalf("no origin: got %v", err)
	}
}

func TestRegisteredTargetsBadConfig(t *testing.T) {
	dir := chdirTemp(t)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	writeFiles(t, dir, map[string]string{filepath.Join(".gemini", "settings.json"): `{"mcpServers":[]}`})
	if _, err := registeredTargets("x", false); err == nil || !strings.Contains(err.Error(), "mcpServers is not a JSON object") {
		t.Fatalf("gemini: got %v", err)
	}
	writeFiles(t, claudeDir, map[string]string{".claude.json": "{"})
	if _, err := registeredTargets("x", false); err == nil || !strings.Contains(err.Error(), "could not parse") {
		t.Fatalf("claude: got %v", err)
	}
}

func TestRegisteredTargetsGlobal(t *testing.T) {
	chdirTemp(t)
	claudeDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("HOME", home)
	writeFiles(t, claudeDir, map[string]string{".claude.json": `{"mcpServers":{"x":{"command":"c"}}}`})
	writeFiles(t, home, map[string]string{filepath.Join(".gemini", "settings.json"): `{"mcpServers":{"x":{}}}`})

	got, err := registeredTargets("x", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []injector.TargetTool{injector.TargetClaudeCode, injector.TargetGeminiCLI}; !slices.Equal(got, want) {
		t.Fatalf("global: got %v, want %v", got, want)
	}
	if got, err := registeredTargets("x", false); err != nil || len(got) != 0 {
		t.Fatalf("local: got %v, %v", got, err)
	}
}

func TestExportFlags(t *testing.T) {
	f := exportCmd.Flags().Lookup("file")
	if f == nil || f.DefValue != lockFileName {
		t.Fatalf("--file: got %+v", f)
	}
	if exportCmd.Flags().Lookup("global") == nil {
		t.Fatal("export has no --global flag")
	}
}
