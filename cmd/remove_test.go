package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
