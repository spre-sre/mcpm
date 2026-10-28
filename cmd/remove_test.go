package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mcpm/internal/injector"

	"github.com/spf13/cobra"
)

func TestRemoveFromGeminiCLIMissingFile(t *testing.T) {
	cwd := t.TempDir()
	err := removeFromGeminiCLI(cwd, "ghost", false)
	if err == nil {
		t.Fatal("expected error for missing settings file, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestInstalledServerDir(t *testing.T) {
	dir := chdirTemp(t)
	server := filepath.Join(dir, ".mcp", "servers", "srv")
	if err := os.MkdirAll(server, 0755); err != nil {
		t.Fatal(err)
	}
	got, err := installedServerDir("srv")
	if err != nil || got != server {
		t.Fatalf("installedServerDir = %q, %v", got, err)
	}
	for _, name := range []string{"ghost", "../srv", ""} {
		if _, err := installedServerDir(name); err == nil || !strings.Contains(err.Error(), "mcpm list") {
			t.Errorf("%q: err = %v", name, err)
		}
	}
}

func TestServerRegistrationsBadConfig(t *testing.T) {
	dir := chdirTemp(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	writeFiles(t, dir, map[string]string{filepath.Join(".gemini", "settings.json"): "{"})
	if _, err := serverRegistrations(dir, "srv"); err == nil || !strings.HasPrefix(err.Error(), "Gemini CLI: ") {
		t.Fatalf("err = %v", err)
	}
}

func setRemoveFlags(t *testing.T, claude, gemini, global, purge bool) {
	t.Helper()
	oldC, oldG, oldGl, oldP := removeClaudeCode, removeGeminiCLI, removeGlobal, removePurge
	t.Cleanup(func() {
		removeClaudeCode, removeGeminiCLI, removeGlobal, removePurge = oldC, oldG, oldGl, oldP
	})
	removeClaudeCode, removeGeminiCLI, removeGlobal, removePurge = claude, gemini, global, purge
}

func TestDryRunRemoveChangesNothing(t *testing.T) {
	dir := chdirTemp(t)
	gemDir := filepath.Join(dir, ".gemini")
	if err := os.MkdirAll(gemDir, 0755); err != nil {
		t.Fatal(err)
	}
	settings := []byte(`{"mcpServers": {"srv": {"command": "x"}}}`)
	settingsPath := filepath.Join(gemDir, "settings.json")
	if err := os.WriteFile(settingsPath, settings, 0644); err != nil {
		t.Fatal(err)
	}
	server := filepath.Join(dir, ".mcp", "servers", "srv")
	if err := os.MkdirAll(server, 0755); err != nil {
		t.Fatal(err)
	}
	setRemoveFlags(t, true, true, false, true)

	if err := dryRunRemove(dir, "srv", "local"); err != nil {
		t.Fatalf("dryRunRemove: %v", err)
	}
	got, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(settings) {
		t.Errorf("settings changed: %s", got)
	}
	if _, err := os.Stat(server); err != nil {
		t.Errorf("server directory removed: %v", err)
	}
}

func TestDryRunRemoveReportsFailures(t *testing.T) {
	dir := chdirTemp(t)
	setRemoveFlags(t, false, true, false, true)

	err := dryRunRemove(dir, "ghost", "local")
	if err == nil {
		t.Fatal("expected error for missing settings and server, got nil")
	}
	if !strings.Contains(err.Error(), "Gemini CLI") || !strings.Contains(err.Error(), "purge failed") {
		t.Errorf("expected Gemini and purge failures, got: %v", err)
	}
}

func TestRemoveCmdDryRunOutput(t *testing.T) {
	dir := chdirTemp(t)
	gemDir := filepath.Join(dir, ".gemini")
	if err := os.MkdirAll(gemDir, 0755); err != nil {
		t.Fatal(err)
	}
	settings := []byte(`{"mcpServers": {"srv": {"command": "x"}}}`)
	settingsPath := filepath.Join(gemDir, "settings.json")
	if err := os.WriteFile(settingsPath, settings, 0644); err != nil {
		t.Fatal(err)
	}
	server := filepath.Join(dir, ".mcp", "servers", "srv")
	if err := os.MkdirAll(server, 0755); err != nil {
		t.Fatal(err)
	}
	setRemoveFlags(t, true, true, false, true)
	oldDry := removeDryRun
	oldSilence := removeCmd.SilenceUsage
	t.Cleanup(func() {
		removeDryRun = oldDry
		removeCmd.SilenceUsage = oldSilence
	})
	removeDryRun = true

	var runErr error
	out := captureStdout(t, func() { runErr = removeCmd.RunE(removeCmd, []string{"srv"}) })
	if runErr != nil {
		t.Fatalf("remove --dry-run: %v", runErr)
	}
	for _, want := range []string{
		"Would run: claude mcp remove --scope local srv",
		"Would remove srv from Gemini CLI (" + settingsPath + ")",
		"Would delete .mcp/servers/srv",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Removed") || strings.Contains(out, "Deleted") {
		t.Errorf("dry-run output reports a change:\n%s", out)
	}
	got, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(settings) {
		t.Errorf("settings changed: %s", got)
	}
	if _, err := os.Stat(server); err != nil {
		t.Errorf("server directory removed: %v", err)
	}
}

func TestRemoveCmdDryRunFlag(t *testing.T) {
	f := removeCmd.Flags().Lookup("dry-run")
	if f == nil {
		t.Fatal("flag --dry-run not defined")
	}
	if f.DefValue != "false" {
		t.Errorf("--dry-run default = %q, want %q", f.DefValue, "false")
	}
}

func TestRemoveFromGeminiCLIMissingServer(t *testing.T) {
	cwd := t.TempDir()
	gemDir := filepath.Join(cwd, ".gemini")
	if err := os.MkdirAll(gemDir, 0755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(gemDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"mcpServers": {}}`), 0644); err != nil {
		t.Fatal(err)
	}

	err := removeFromGeminiCLI(cwd, "ghost", false)
	if err == nil {
		t.Fatal("expected error for missing server, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("expected error to mention server name 'ghost', got: %v", err)
	}
}

func TestGeminiRegistration(t *testing.T) {
	cwd := t.TempDir()
	gemDir := filepath.Join(cwd, ".gemini")
	if err := os.MkdirAll(gemDir, 0755); err != nil {
		t.Fatal(err)
	}
	settings := `{"theme": "dark", "mcpServers": {"srv": {"type": "stdio", "command": "node", "args": ["/s/index.js"], "env": {"K": "v"}}}}`
	if err := os.WriteFile(filepath.Join(gemDir, "settings.json"), []byte(settings), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := geminiRegistration(cwd, "srv", false)
	if err != nil {
		t.Fatalf("geminiRegistration: %v", err)
	}
	if reg.Command != "node" || len(reg.Args) != 1 || reg.Args[0] != "/s/index.js" {
		t.Errorf("registration = %+v", reg)
	}

	if _, err := geminiRegistration(cwd, "ghost", false); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("expected error naming ghost, got: %v", err)
	}
}

func TestGeminiRegistrationMissingFile(t *testing.T) {
	if _, err := geminiRegistration(t.TempDir(), "srv", false); err == nil {
		t.Error("expected error for missing settings file, got nil")
	}
}

// switchFixture changes to a project dir with empty client configs.
func switchFixture(t *testing.T) string {
	t.Helper()
	dir := chdirTemp(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	return dir
}

func runSwitch(t *testing.T, dir string, tools []injector.TargetTool, global, on bool) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := switchServer(&out, &errOut, dir, "srv", tools, global, on)
	return out.String() + errOut.String(), err
}

func loadOffT(t *testing.T, dir string, global bool) offServers {
	t.Helper()
	off, _, err := loadOff(dir, global)
	if err != nil {
		t.Fatal(err)
	}
	return off
}

func TestSwitchGeminiOffAndOn(t *testing.T) {
	dir := switchFixture(t)
	gemini := []injector.TargetTool{injector.TargetGeminiCLI}
	settingsPath := filepath.Join(dir, ".gemini", "settings.json")
	writeFiles(t, dir, map[string]string{filepath.Join(".gemini", "settings.json"): `{"theme": "dark", "mcpServers": {"srv": {"command": "node", "args": ["/s/index.js"], "env": {"K": "v"}, "trust": true}, "other": {"command": "x"}}}`})

	out, err := runSwitch(t, dir, gemini, false, false)
	if err != nil || out != "Switched srv off in Gemini CLI (local)\n" {
		t.Fatalf("disable: %q, %v", out, err)
	}
	cfg, err := loadGeminiSettings(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	servers := cfg["mcpServers"].(map[string]interface{})
	if _, ok := servers["srv"]; ok || servers["other"] == nil || cfg["theme"] != "dark" {
		t.Fatalf("settings after disable: %v", cfg)
	}
	if !loadOffT(t, dir, false).has("srv", injector.TargetGeminiCLI) {
		t.Fatal("registration not kept")
	}
	path, _ := offPath(dir, false)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("off file mode = %v, %v; want 0600", info, err)
	}
	if out, err := runSwitch(t, dir, gemini, false, false); err != nil || out != "srv is already off in Gemini CLI (local)\n" {
		t.Fatalf("disable again: %q, %v", out, err)
	}

	if out, err := runSwitch(t, dir, gemini, false, true); err != nil || out != "Switched srv on in Gemini CLI (local)\n" {
		t.Fatalf("enable: %q, %v", out, err)
	}
	_, srv := readGeminiServer(t, settingsPath)
	if srv["trust"] != true || srv["env"].(map[string]interface{})["K"] != "v" || srv["args"].([]interface{})[0] != "/s/index.js" {
		t.Fatalf("restored entry = %v", srv)
	}
	if len(loadOffT(t, dir, false)) != 0 {
		t.Fatal("kept registration not forgotten")
	}
	if out, err := runSwitch(t, dir, gemini, false, true); err != nil || out != "srv is already on in Gemini CLI (local)\n" {
		t.Fatalf("enable again: %q, %v", out, err)
	}
}

func TestSwitchClaudeOffAndOn(t *testing.T) {
	dir := switchFixture(t)
	claude := []injector.TargetTool{injector.TargetClaudeCode}
	claudePath := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".claude.json")
	entry := `{"type":"stdio","command":"node","args":["x"],"env":{"K":"v"}}`
	writeFiles(t, filepath.Dir(claudePath), map[string]string{".claude.json": `{"mcpServers": {"srv": ` + entry + `}}`})
	calls := stubClaude(t, nil)

	if out, err := runSwitch(t, dir, claude, true, false); err != nil || out != "Switched srv off in Claude Code (global)\n" {
		t.Fatalf("disable: %q, %v", out, err)
	}
	// The stub does not edit .claude.json; do what claude mcp remove would.
	writeFiles(t, filepath.Dir(claudePath), map[string]string{".claude.json": `{"mcpServers": {}}`})
	if out, err := runSwitch(t, dir, claude, true, true); err != nil || out != "Switched srv on in Claude Code (global)\n" {
		t.Fatalf("enable: %q, %v", out, err)
	}
	want := [][]string{
		{"mcp", "remove", "--scope", "user", "srv"},
		{"mcp", "add-json", "--scope", "user", "srv", entry},
	}
	if !slices.EqualFunc(*calls, want, slices.Equal[[]string]) {
		t.Errorf("claude calls = %q, want %q", *calls, want)
	}
	if len(loadOffT(t, dir, true)) != 0 {
		t.Fatal("kept registration not forgotten")
	}
}

func TestSwitchOffFailureForgetsEntry(t *testing.T) {
	dir := switchFixture(t)
	cfg, err := json.Marshal(map[string]interface{}{
		"projects": map[string]interface{}{dir: map[string]interface{}{"mcpServers": map[string]registration{"srv": {Command: "node"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, os.Getenv("CLAUDE_CONFIG_DIR"), map[string]string{".claude.json": string(cfg)})
	stubClaude(t, func(args []string) bool { return args[1] == "remove" })

	out, err := runSwitch(t, dir, []injector.TargetTool{injector.TargetClaudeCode}, false, false)
	if err == nil || !strings.Contains(err.Error(), "switch off") || !strings.Contains(out, "Error switching srv off in Claude Code (local)") {
		t.Fatalf("got %q, %v", out, err)
	}
	if len(loadOffT(t, dir, false)) != 0 {
		t.Fatal("registration kept although it was not removed")
	}
}

func TestSwitchUnknownServer(t *testing.T) {
	dir := switchFixture(t)
	for _, on := range []bool{false, true} {
		if _, err := runSwitch(t, dir, doctorTools(false, false), false, on); err == nil {
			t.Errorf("on=%v: expected error for unregistered server", on)
		}
	}
	if out, _ := runSwitch(t, dir, []injector.TargetTool{injector.TargetGeminiCLI}, false, true); !strings.Contains(out, "not switched off") {
		t.Errorf("enable: %q", out)
	}
}

func TestSwitchSkipsClientsWithoutServer(t *testing.T) {
	dir := switchFixture(t)
	writeFiles(t, dir, map[string]string{filepath.Join(".gemini", "settings.json"): `{"mcpServers": {"srv": {"command": "node"}}}`})
	stubClaude(t, nil)
	both := doctorTools(false, false)

	out, err := runSwitch(t, dir, both, false, false)
	if err != nil {
		t.Fatalf("disable: %q, %v", out, err)
	}
	if !strings.Contains(out, "Switched srv off in Gemini CLI (local)") || !strings.Contains(out, "Skipped Claude Code (local): server srv not found") || strings.Contains(out, "Error") {
		t.Errorf("disable output = %q", out)
	}
	if !loadOffT(t, dir, false).has("srv", injector.TargetGeminiCLI) {
		t.Fatal("registration not kept")
	}

	out, err = runSwitch(t, dir, both, false, true)
	if err != nil {
		t.Fatalf("enable: %q, %v", out, err)
	}
	if !strings.Contains(out, "Switched srv on in Gemini CLI (local)") || !strings.Contains(out, "Skipped Claude Code (local): server srv is not switched off") || strings.Contains(out, "Error") {
		t.Errorf("enable output = %q", out)
	}
	if len(loadOffT(t, dir, false)) != 0 {
		t.Fatal("kept registration not forgotten")
	}
}

func TestRemoveForgetsSwitchedOff(t *testing.T) {
	dir := switchFixture(t)
	writeOff(t, dir, false, offServers{"srv": {injector.TargetGeminiCLI: json.RawMessage(`{"command":"x"}`)}})
	setRemoveFlags(t, false, true, false, false)

	out := captureStdout(t, func() {
		if err := dryRunRemove(dir, "srv", "local"); err != nil {
			t.Errorf("dryRunRemove: %v", err)
		}
	})
	if !strings.Contains(out, "Would forget switched-off srv in Gemini CLI") {
		t.Errorf("dry-run output = %q", out)
	}
	if !loadOffT(t, dir, false).has("srv", injector.TargetGeminiCLI) {
		t.Fatal("dry run changed the kept registrations")
	}

	if err := removeOrForget(dir, "srv", injector.TargetGeminiCLI, false); err != nil {
		t.Fatalf("removeOrForget: %v", err)
	}
	if len(loadOffT(t, dir, false)) != 0 {
		t.Fatal("kept registration not forgotten")
	}
}

func TestSwitchCmdsRegistered(t *testing.T) {
	for _, c := range []*cobra.Command{disableCmd, enableCmd} {
		found := false
		for _, r := range rootCmd.Commands() {
			found = found || r == c
		}
		if !found {
			t.Errorf("%s is not registered on rootCmd", c.Name())
		}
		for _, flag := range []string{"claude", "gemini", "global"} {
			if c.Flags().Lookup(flag) == nil {
				t.Errorf("%s has no --%s flag", c.Name(), flag)
			}
		}
	}
}

func TestGeminiRegistrationBadArgs(t *testing.T) {
	cwd := t.TempDir()
	gemDir := filepath.Join(cwd, ".gemini")
	if err := os.MkdirAll(gemDir, 0755); err != nil {
		t.Fatal(err)
	}
	settings := `{"mcpServers": {"srv": {"command": "node", "args": "not-a-list"}}}`
	if err := os.WriteFile(filepath.Join(gemDir, "settings.json"), []byte(settings), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := geminiRegistration(cwd, "srv", false); err == nil {
		t.Error("expected error for args that are not a list, got nil")
	}
}
