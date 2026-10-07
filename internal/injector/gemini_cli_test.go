package injector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mcpm/internal/builder"
)

func registerDemo(t *testing.T, cwd string) error {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	return Register(
		&builder.BuildResult{
			Command: "node",
			Args:    []string{filepath.Join(cwd, ".mcp", "servers", "demo", "index.js")},
		},
		[]TargetTool{TargetGeminiCLI},
		nil,
		false,
	)
}

func TestCommentedJSONReturnsErrorAndFileUnchanged(t *testing.T) {
	cwd := t.TempDir()
	gemDir := filepath.Join(cwd, ".gemini")
	if err := os.MkdirAll(gemDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := []byte("{ // user comment\n\"theme\": \"dark\", \"mcpServers\": {\"keep-me\": {\"type\": \"stdio\", \"command\": \"x\"}} }")
	settingsPath := filepath.Join(gemDir, "settings.json")
	if err := os.WriteFile(settingsPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	err := registerDemo(t, cwd)
	if err == nil {
		t.Fatal("expected error for commented JSON, got nil")
	}
	if !strings.Contains(err.Error(), settingsPath) {
		t.Errorf("error should mention file path %q, got: %s", settingsPath, err)
	}

	after, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(content) {
		t.Errorf("file was modified; want original bytes preserved")
	}
}

func TestValidFilePreservesSettingsAndServers(t *testing.T) {
	cwd := t.TempDir()
	gemDir := filepath.Join(cwd, ".gemini")
	if err := os.MkdirAll(gemDir, 0755); err != nil {
		t.Fatal(err)
	}
	initial := `{"theme": "dark", "mcpServers": {"keep-me": {"type": "stdio", "command": "x"}}}`
	if err := os.WriteFile(filepath.Join(gemDir, "settings.json"), []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	if err := registerDemo(t, cwd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(gemDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if _, ok := result["theme"]; !ok {
		t.Error("theme key was lost")
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(result["mcpServers"], &servers); err != nil {
		t.Fatal(err)
	}
	if _, ok := servers["keep-me"]; !ok {
		t.Error("existing server keep-me was lost")
	}
	if _, ok := servers["demo"]; !ok {
		t.Error("new server demo was not added")
	}
}

func TestNoFileCreatesNew(t *testing.T) {
	cwd := t.TempDir()

	if err := registerDemo(t, cwd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(cwd, ".gemini", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(result["mcpServers"], &servers); err != nil {
		t.Fatal(err)
	}
	if _, ok := servers["demo"]; !ok {
		t.Error("new server demo was not created")
	}
}

func TestUnmarshalJSONInvalidReturnsError(t *testing.T) {
	var cfg GeminiConfig
	err := json.Unmarshal([]byte(`not json`), &cfg)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestNewFileWithEnvMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission test not applicable on Windows")
	}
	cwd := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	err = Register(
		&builder.BuildResult{
			Command: filepath.Join(cwd, ".mcp", "servers", "alpha", "mcp-server"),
		},
		[]TargetTool{TargetGeminiCLI},
		map[string]string{"API_KEY": "sk-secret-123"},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(cwd, ".gemini", "settings.json")
	info, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("got mode %04o, want 0600", mode)
	}
}

func TestExistingFileWithEnvMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission test not applicable on Windows")
	}
	cwd := t.TempDir()
	gemDir := filepath.Join(cwd, ".gemini")
	if err := os.MkdirAll(gemDir, 0755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(gemDir, "settings.json")
	initial := `{"theme": "dark", "mcpServers": {"keep-me": {"type": "stdio", "command": "x"}}}`
	if err := os.WriteFile(settingsPath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	err = Register(
		&builder.BuildResult{
			Command: filepath.Join(cwd, ".mcp", "servers", "alpha", "mcp-server"),
		},
		[]TargetTool{TargetGeminiCLI},
		map[string]string{"API_KEY": "sk-secret-456"},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("got mode %04o, want 0600", mode)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result["theme"]; !ok {
		t.Error("theme key was lost")
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(result["mcpServers"], &servers); err != nil {
		t.Fatal(err)
	}
	if _, ok := servers["keep-me"]; !ok {
		t.Error("existing server keep-me was lost")
	}
}

func TestExistingFileWithoutEnvKeepsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission test not applicable on Windows")
	}
	cwd := t.TempDir()
	gemDir := filepath.Join(cwd, ".gemini")
	if err := os.MkdirAll(gemDir, 0755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(gemDir, "settings.json")
	initial := `{"mcpServers": {}}`
	if err := os.WriteFile(settingsPath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	err = Register(
		&builder.BuildResult{
			Command: filepath.Join(cwd, ".mcp", "servers", "alpha", "mcp-server"),
		},
		[]TargetTool{TargetGeminiCLI},
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0644 {
		t.Errorf("got mode %04o, want 0644", mode)
	}
}
