package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseEnvVars(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		wantKey string
		wantVal string
		wantErr bool
	}{
		{"key=value", []string{"API_KEY=secret"}, "API_KEY", "secret", false},
		{"empty value", []string{"KEY="}, "KEY", "", false},
		{"value with equals", []string{"K=a=b"}, "K", "a=b", false},
		{"underscore prefix", []string{"_FOO=bar"}, "_FOO", "bar", false},
		{"missing equals", []string{"API_KEY"}, "", "", true},
		{"empty key", []string{"=v"}, "", "", true},
		{"digit prefix", []string{"1BAD=x"}, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := parseEnvVars(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %v", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if env[tt.wantKey] != tt.wantVal {
				t.Errorf("got %q=%q, want %q=%q", tt.wantKey, env[tt.wantKey], tt.wantKey, tt.wantVal)
			}
		})
	}
}

func TestValidateTransport(t *testing.T) {
	for _, valid := range []string{"stdio", "http", "sse"} {
		if err := validateTransport(valid); err != nil {
			t.Errorf("expected %q to be valid, got error: %v", valid, err)
		}
	}
	for _, invalid := range []string{"foo", "HTTP", "SSE", ""} {
		if err := validateTransport(invalid); err == nil {
			t.Errorf("expected %q to be invalid", invalid)
		}
	}
}

func TestAddGeminiNewFileWithEnvMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission test not applicable on Windows")
	}
	cwd := t.TempDir()

	err := addToGeminiCLI(cwd, "alpha", "node", []string{"/x.js"}, map[string]string{"API_KEY": "sk-secret-123"}, "stdio", false)
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

func TestAddGeminiExistingFileWithEnvMode0600(t *testing.T) {
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

	err := addToGeminiCLI(cwd, "alpha", "node", []string{"/x.js"}, map[string]string{"API_KEY": "sk-secret-456"}, "stdio", false)
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
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result["theme"]; !ok {
		t.Error("theme key was lost")
	}
	mcpServers, ok := result["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatal("mcpServers not found")
	}
	if _, ok := mcpServers["keep-me"]; !ok {
		t.Error("existing server keep-me was lost")
	}
}

func TestAddGeminiExistingFileWithoutEnvKeepsMode(t *testing.T) {
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

	err := addToGeminiCLI(cwd, "alpha", "node", []string{"/x.js"}, map[string]string{}, "stdio", false)
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

func TestAddGeminiCommentedJSONReturnsError(t *testing.T) {
	cwd := t.TempDir()
	gemDir := filepath.Join(cwd, ".gemini")
	if err := os.MkdirAll(gemDir, 0755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(gemDir, "settings.json")
	original := []byte("{\n// user comment\n\"theme\": \"dark\",\n\"mcpServers\": {\"keep-me\": {\"type\": \"stdio\", \"command\": \"x\"}}\n}")
	if err := os.WriteFile(settingsPath, original, 0644); err != nil {
		t.Fatal(err)
	}

	err := addToGeminiCLI(cwd, "srv", "node", []string{"/x.js"}, map[string]string{}, "stdio", false)
	if err == nil {
		t.Fatal("expected error for commented JSON, got nil")
	}
	if !strings.Contains(err.Error(), settingsPath) {
		t.Errorf("error should contain file path %q, got: %v", settingsPath, err)
	}

	after, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(original, after) {
		t.Error("file was modified despite parse error")
	}
}

func TestAddGeminiMcpServersNotObjectReturnsError(t *testing.T) {
	cwd := t.TempDir()
	gemDir := filepath.Join(cwd, ".gemini")
	if err := os.MkdirAll(gemDir, 0755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(gemDir, "settings.json")
	original := []byte(`{"mcpServers": []}`)
	if err := os.WriteFile(settingsPath, original, 0644); err != nil {
		t.Fatal(err)
	}

	err := addToGeminiCLI(cwd, "srv", "node", []string{"/x.js"}, map[string]string{}, "stdio", false)
	if err == nil {
		t.Fatal("expected error for mcpServers as array, got nil")
	}
	if !strings.Contains(err.Error(), settingsPath) {
		t.Errorf("error should contain file path %q, got: %v", settingsPath, err)
	}

	after, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(original, after) {
		t.Error("file was modified despite mcpServers type error")
	}
}

func TestAddGeminiValidFileKeepsExistingSettings(t *testing.T) {
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

	err := addToGeminiCLI(cwd, "new-srv", "node", []string{"/y.js"}, map[string]string{}, "stdio", false)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result["theme"] != "dark" {
		t.Error("theme key was lost")
	}
	mcpServers, ok := result["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatal("mcpServers not found or not an object")
	}
	if _, ok := mcpServers["keep-me"]; !ok {
		t.Error("existing server keep-me was lost")
	}
	if _, ok := mcpServers["new-srv"]; !ok {
		t.Error("new server new-srv was not added")
	}
}

func TestAddGeminiNoFileCreatesOne(t *testing.T) {
	cwd := t.TempDir()

	err := addToGeminiCLI(cwd, "fresh", "node", []string{"/z.js"}, map[string]string{}, "stdio", false)
	if err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(cwd, ".gemini", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	mcpServers, ok := result["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatal("mcpServers not found or not an object")
	}
	if _, ok := mcpServers["fresh"]; !ok {
		t.Error("server fresh was not created")
	}
}
