package injector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"mcpm/internal/builder"
)

// installFakeClaude puts a stub `claude` executable first on PATH. The stub
// records its working directory and each argument on its own line in a log
// file, then exits with exitCode after printing output.
func installFakeClaude(t *testing.T, exitCode int, output string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub not supported on Windows")
	}
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "claude.log")
	script := "#!/bin/sh\n" +
		"pwd > \"" + logPath + "\"\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> \"" + logPath + "\"; done\n" +
		"printf '%s' '" + output + "'\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func readFakeClaudeLog(t *testing.T, logPath string) (string, []string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("claude stub was not invoked: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	return lines[0], lines[1:]
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestUpdateClaudeCodeLocalScope(t *testing.T) {
	logPath := installFakeClaude(t, 0, "")
	cwd := t.TempDir()
	script := filepath.Join(cwd, ".mcp", "servers", "demo", "index.js")

	err := updateClaudeCode(cwd, &builder.BuildResult{
		Command: "node",
		Args:    []string{script, "--verbose"},
	}, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dir, args := readFakeClaudeLog(t, logPath)
	wantDir, _ := filepath.EvalSymlinks(cwd)
	gotDir, _ := filepath.EvalSymlinks(dir)
	if gotDir != wantDir {
		t.Errorf("claude ran in %q, want %q", gotDir, wantDir)
	}
	want := []string{"mcp", "add", "--scope", "local", "demo", "node", script, "--verbose"}
	if !equalArgs(args, want) {
		t.Errorf("args = %q, want %q", args, want)
	}
}

func TestUpdateClaudeCodeGlobalScopeWithEnv(t *testing.T) {
	logPath := installFakeClaude(t, 0, "")
	cwd := t.TempDir()
	command := filepath.Join(cwd, ".mcp", "servers", "alpha", "mcp-server")

	err := updateClaudeCode(cwd, &builder.BuildResult{Command: command},
		map[string]string{"API_KEY": "sk-123"}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, args := readFakeClaudeLog(t, logPath)
	want := []string{"mcp", "add", "--scope", "user", "--env", "API_KEY=sk-123", "alpha", command}
	if !equalArgs(args, want) {
		t.Errorf("args = %q, want %q", args, want)
	}
}

func TestUpdateClaudeCodeMultipleEnv(t *testing.T) {
	logPath := installFakeClaude(t, 0, "")
	cwd := t.TempDir()
	command := filepath.Join(cwd, ".mcp", "servers", "alpha", "mcp-server")
	env := map[string]string{"A": "1", "B": "2"}

	if err := updateClaudeCode(cwd, &builder.BuildResult{Command: command}, env, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, args := readFakeClaudeLog(t, logPath)
	if len(args) != 10 {
		t.Fatalf("args = %q, want 10 entries", args)
	}
	got := map[string]bool{}
	for i := 4; i < 8; i += 2 {
		if args[i] != "--env" {
			t.Errorf("args[%d] = %q, want --env", i, args[i])
		}
		got[args[i+1]] = true
	}
	if !got["A=1"] || !got["B=2"] {
		t.Errorf("env args = %v, want A=1 and B=2", got)
	}
	if args[8] != "alpha" || args[9] != command {
		t.Errorf("trailing args = %q, want [alpha %s]", args[8:], command)
	}
}

func TestUpdateClaudeCodeCommandFailure(t *testing.T) {
	installFakeClaude(t, 1, "already exists")
	cwd := t.TempDir()

	err := updateClaudeCode(cwd, &builder.BuildResult{
		Command: filepath.Join(cwd, ".mcp", "servers", "alpha", "mcp-server"),
	}, nil, false)
	if err == nil {
		t.Fatal("expected error when claude exits non-zero, got nil")
	}
	if !strings.Contains(err.Error(), "failed to add MCP server") {
		t.Errorf("error missing prefix: %s", err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should include command output: %s", err)
	}
}

func TestUpdateClaudeCodeNoServerName(t *testing.T) {
	logPath := installFakeClaude(t, 0, "")

	err := updateClaudeCode(t.TempDir(), &builder.BuildResult{
		Command: "node",
		Args:    []string{"index.js"},
	}, nil, false)
	if err == nil {
		t.Fatal("expected error for underivable server name, got nil")
	}
	if !strings.Contains(err.Error(), "cannot derive a server name") {
		t.Errorf("unexpected error: %s", err)
	}
	if _, statErr := os.Stat(logPath); !os.IsNotExist(statErr) {
		t.Error("claude should not be invoked when the server name cannot be derived")
	}
}

func TestRegisterClaudeCodeWrapsError(t *testing.T) {
	installFakeClaude(t, 1, "boom")
	cwd := t.TempDir()

	err := Register(&builder.BuildResult{
		Command: filepath.Join(cwd, ".mcp", "servers", "alpha", "mcp-server"),
	}, []TargetTool{TargetClaudeCode}, nil, false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.HasPrefix(err.Error(), "claude configuration failed:") {
		t.Errorf("error not wrapped: %s", err)
	}
}

// writeClaudeConfig points CLAUDE_CONFIG_DIR at a temp dir holding content as
// .claude.json and returns the file path.
func writeClaudeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLookupClaudeCodeScopes(t *testing.T) {
	cwd := "/work/project"
	writeClaudeConfig(t, `{
		"mcpServers": {"alpha": {"type": "stdio", "command": "/user/alpha", "args": []}},
		"projects": {"/work/project": {"mcpServers": {"alpha": {"type": "stdio", "command": "node", "args": ["/local/index.js"]}}}}
	}`)

	def, ok, err := lookupClaudeCode(cwd, "alpha", true)
	if err != nil || !ok || def.Command != "/user/alpha" {
		t.Errorf("user scope = %+v, %v, %v", def, ok, err)
	}
	def, ok, err = lookupClaudeCode(cwd, "alpha", false)
	if err != nil || !ok || def.Command != "node" || !equalArgs(def.Args, []string{"/local/index.js"}) {
		t.Errorf("local scope = %+v, %v, %v", def, ok, err)
	}
	if _, ok, err := lookupClaudeCode("/other", "alpha", false); err != nil || ok {
		t.Errorf("other project: ok=%v err=%v, want not registered", ok, err)
	}
	if _, ok, err := lookupClaudeCode(cwd, "beta", true); err != nil || ok {
		t.Errorf("beta: ok=%v err=%v, want not registered", ok, err)
	}
}

func TestLookupClaudeCodeMissingFile(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if _, ok, err := lookupClaudeCode(t.TempDir(), "alpha", false); err != nil || ok {
		t.Errorf("ok=%v err=%v, want not registered without error", ok, err)
	}
}

func TestLookupClaudeCodeInvalidJSON(t *testing.T) {
	path := writeClaudeConfig(t, "not json")
	_, _, err := lookupClaudeCode(t.TempDir(), "alpha", true)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("err = %v, want parse error naming %s", err, path)
	}
}

func TestClaudeCodeRegistrationsBothScopes(t *testing.T) {
	cwd := "/work/project"
	writeClaudeConfig(t, `{
		"mcpServers": {"alpha": {"type": "stdio", "command": "/user/alpha", "env": {"TOKEN": "secret-1", "API_KEY": "secret-2"}}},
		"projects": {"/work/project": {"mcpServers": {"alpha": {"type": "stdio", "command": "node", "args": ["/local/index.js"]}}}}
	}`)

	regs, err := ClaudeCodeRegistrations(cwd, "alpha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(regs) != 2 {
		t.Fatalf("regs = %+v, want local and global", regs)
	}
	local, global := regs[0], regs[1]
	if local.Tool != TargetClaudeCode || local.Scope != ScopeLocal || local.Command != "node" ||
		!equalArgs(local.Args, []string{"/local/index.js"}) || len(local.EnvNames) != 0 {
		t.Errorf("local = %+v", local)
	}
	if global.Scope != ScopeGlobal || global.Command != "/user/alpha" ||
		!equalArgs(global.EnvNames, []string{"API_KEY", "TOKEN"}) {
		t.Errorf("global = %+v", global)
	}

	data, err := json.Marshal(regs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") {
		t.Errorf("JSON leaks env values: %s", data)
	}
	if !strings.Contains(string(data), `"args":[]`) {
		t.Errorf("missing args should encode as []: %s", data)
	}
}

func TestClaudeCodeRegistrationsNotRegistered(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	regs, err := ClaudeCodeRegistrations(t.TempDir(), "alpha")
	if err != nil || len(regs) != 0 {
		t.Errorf("regs=%+v err=%v, want none without error", regs, err)
	}
}

func TestClaudeCodeRegistrationsInvalidJSON(t *testing.T) {
	path := writeClaudeConfig(t, "not json")
	if _, err := ClaudeCodeRegistrations(t.TempDir(), "alpha"); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("err = %v, want parse error naming %s", err, path)
	}
}

func TestMcpServerDefJSON(t *testing.T) {
	def := McpServerDef{Type: "stdio", Command: "node", Args: []string{"index.js"}}
	data, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"stdio","command":"node","args":["index.js"]}`
	if string(data) != want {
		t.Errorf("got %s, want %s", data, want)
	}

	def.Env = map[string]string{"K": "V"}
	data, err = json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	var back McpServerDef
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Env["K"] != "V" || back.Type != "stdio" || back.Command != "node" || !equalArgs(back.Args, def.Args) {
		t.Errorf("round trip mismatch: %+v", back)
	}
}
