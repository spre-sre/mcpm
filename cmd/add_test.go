package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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
