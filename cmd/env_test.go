package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// envFixture registers srv in Claude Code (local scope of the working
// directory) and in the global Gemini CLI settings, each with secret values,
// and installs it with an mcp.json that requires API_KEY and TOKEN.
func envFixture(t *testing.T) (dir, home string) {
	t.Helper()
	dir = chdirTemp(t)
	home = t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	writeFiles(t, claudeDir, map[string]string{".claude.json": `{"projects": {"` + dir + `": {"mcpServers": {"srv": ` +
		`{"type": "stdio", "command": "node", "args": ["-y", "/s/index.js"], "env": {"API_KEY": "s3cret"}}}}}}`})
	writeFiles(t, home, map[string]string{filepath.Join(".gemini", "settings.json"): `{"theme": "dark", "mcpServers": {"srv": ` +
		`{"type": "stdio", "command": "node", "args": ["/s/index.js"], "env": {"API_KEY": "hunter2", "REGION": "eu"}}}}`})
	writeFiles(t, filepath.Join(dir, ".mcp", "servers", "srv"), map[string]string{
		"mcp.json": `{"runCmd": "node", "args": ["index.js"], "requiredEnv": ["API_KEY", "TOKEN"]}`,
	})
	return dir, home
}

// stubClaude replaces runClaude; fail decides whether a call fails.
func stubClaude(t *testing.T, fail func(args []string) bool) *[][]string {
	t.Helper()
	var calls [][]string
	old := runClaude
	runClaude = func(dir string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if fail != nil && fail(args) {
			return []byte("boom"), errors.New("exit status 1")
		}
		return nil, nil
	}
	t.Cleanup(func() { runClaude = old })
	return &calls
}

func readGeminiServer(t *testing.T, path string) (map[string]interface{}, map[string]interface{}) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg, cfg["mcpServers"].(map[string]interface{})["srv"].(map[string]interface{})
}

func TestEnvListHidesValues(t *testing.T) {
	dir, _ := envFixture(t)
	targets, err := envTargets(dir, "srv")
	if err != nil {
		t.Fatal(err)
	}
	required, err := requiredEnv("srv")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeEnvList(&buf, "srv", targets, required, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "s3cret") || strings.Contains(out, "hunter2") {
		t.Errorf("value printed without --show: %s", out)
	}
	for _, want := range []string{"API_KEY  Claude Code (local)  ****", "REGION   Gemini CLI (global)  ****",
		"TOKEN    Claude Code (local)  (not set, required)", "TOKEN    Gemini CLI (global)  (not set, required)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "REGION   Claude Code") {
		t.Errorf("unrequired variable listed for a registration without it:\n%s", out)
	}

	buf.Reset()
	if err := writeEnvList(&buf, "srv", targets, required, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "s3cret") || !strings.Contains(buf.String(), "hunter2") {
		t.Errorf("values missing with --show:\n%s", buf.String())
	}
}

func TestEnvListNoVariables(t *testing.T) {
	var buf bytes.Buffer
	targets := []envTarget{{Tool: "gemini-cli", Scope: "local", Def: envServerDef{Command: "node"}}}
	if err := writeEnvList(&buf, "srv", targets, nil, false); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "srv has no environment variables\n" {
		t.Errorf("got %q", buf.String())
	}
}

func TestEnvNotRegistered(t *testing.T) {
	dir := chdirTemp(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	_, err := envTargets(dir, "srv")
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("got %v, want not registered", err)
	}
}

func TestEnvSetUpdatesEveryClient(t *testing.T) {
	dir, home := envFixture(t)
	calls := stubClaude(t, nil)
	var out, errOut bytes.Buffer
	if err := changeEnv(&out, &errOut, dir, "srv", setVars(map[string]string{"TOKEN": "t0k"})); err != nil {
		t.Fatalf("%v: %s", err, errOut.String())
	}

	want := [][]string{
		{"mcp", "remove", "--scope", "local", "srv"},
		{"mcp", "add", "--scope", "local", "--transport", "stdio", "srv",
			"--env", "API_KEY=s3cret", "--env", "TOKEN=t0k", "--", "node", "-y", "/s/index.js"},
	}
	if !slices.EqualFunc(*calls, want, slices.Equal[[]string]) {
		t.Errorf("claude calls = %q, want %q", *calls, want)
	}

	path := filepath.Join(home, ".gemini", "settings.json")
	cfg, srv := readGeminiServer(t, path)
	if cfg["theme"] != "dark" {
		t.Errorf("other settings lost: %v", cfg)
	}
	env := srv["env"].(map[string]interface{})
	if env["TOKEN"] != "t0k" || env["API_KEY"] != "hunter2" || env["REGION"] != "eu" {
		t.Errorf("gemini env = %v", env)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("settings mode = %v, %v; want 0600", info, err)
	}
	if !strings.Contains(out.String(), "Updated srv in Claude Code (local)") ||
		!strings.Contains(out.String(), "Updated srv in Gemini CLI (global)") {
		t.Errorf("output = %q", out.String())
	}
}

func TestEnvUnsetSkipsUnchangedClients(t *testing.T) {
	dir, home := envFixture(t)
	calls := stubClaude(t, nil)
	var out, errOut bytes.Buffer
	if err := changeEnv(&out, &errOut, dir, "srv", unsetVars([]string{"REGION"})); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Errorf("claude called although its env has no REGION: %q", *calls)
	}
	_, srv := readGeminiServer(t, filepath.Join(home, ".gemini", "settings.json"))
	if env := srv["env"].(map[string]interface{}); len(env) != 1 || env["API_KEY"] != "hunter2" {
		t.Errorf("gemini env = %v", env)
	}
}

func TestEnvUnsetLastVariableDropsEnv(t *testing.T) {
	dir, home := envFixture(t)
	stubClaude(t, nil)
	var out, errOut bytes.Buffer
	if err := changeEnv(&out, &errOut, dir, "srv", unsetVars([]string{"API_KEY", "REGION"})); err != nil {
		t.Fatal(err)
	}
	_, srv := readGeminiServer(t, filepath.Join(home, ".gemini", "settings.json"))
	if _, ok := srv["env"]; ok {
		t.Errorf("env kept after unsetting every variable: %v", srv)
	}
	if srv["command"] != "node" {
		t.Errorf("registration changed: %v", srv)
	}
}

func TestEnvNothingToChange(t *testing.T) {
	dir, _ := envFixture(t)
	calls := stubClaude(t, nil)
	var out, errOut bytes.Buffer
	if err := changeEnv(&out, &errOut, dir, "srv", unsetVars([]string{"MISSING"})); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 || out.String() != "srv: nothing to change\n" {
		t.Errorf("calls %q, output %q", *calls, out.String())
	}
}

func TestEnvClaudeFailureRestoresRegistration(t *testing.T) {
	dir, home := envFixture(t)
	calls := stubClaude(t, func(args []string) bool {
		return args[1] == "add" && slices.Contains(args, "TOKEN=t0k")
	})
	var out, errOut bytes.Buffer
	err := changeEnv(&out, &errOut, dir, "srv", setVars(map[string]string{"TOKEN": "t0k"}))
	if err == nil || !strings.Contains(err.Error(), "Claude Code (local)") {
		t.Fatalf("got %v, want a Claude Code failure", err)
	}
	if len(*calls) != 3 || !slices.Contains((*calls)[2], "API_KEY=s3cret") || slices.Contains((*calls)[2], "TOKEN=t0k") {
		t.Errorf("old registration not restored: %q", *calls)
	}
	_, srv := readGeminiServer(t, filepath.Join(home, ".gemini", "settings.json"))
	if srv["env"].(map[string]interface{})["TOKEN"] != "t0k" {
		t.Errorf("Gemini CLI not updated after a Claude Code failure: %v", srv)
	}
}

func TestEnvRejectsRemoteServer(t *testing.T) {
	target := envTarget{Tool: "gemini-cli", Scope: "local", Def: envServerDef{Type: "http"}}
	if err := applyEnv(t.TempDir(), "srv", target, map[string]string{"K": "v"}); err == nil {
		t.Fatal("env set on an http server")
	}
}

func TestRequiredEnvNotInstalled(t *testing.T) {
	chdirTemp(t)
	required, err := requiredEnv("srv")
	if err != nil || required != nil {
		t.Errorf("got %v, %v; want nothing", required, err)
	}
}
