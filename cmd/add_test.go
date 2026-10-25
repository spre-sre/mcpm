package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func TestClientFailuresNoErrors(t *testing.T) {
	errs := map[string]error{}
	if err := clientFailures("add", errs); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestClientFailuresOneError(t *testing.T) {
	errs := map[string]error{
		"Gemini CLI": fmt.Errorf("could not parse settings"),
	}
	err := clientFailures("add", errs)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Gemini CLI") {
		t.Errorf("expected error to name Gemini CLI, got: %v", err)
	}
}

func TestClientFailuresTwoErrors(t *testing.T) {
	errs := map[string]error{
		"Claude Code": fmt.Errorf("command not found"),
		"Gemini CLI":  fmt.Errorf("could not parse settings"),
	}
	err := clientFailures("remove", errs)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Claude Code") {
		t.Errorf("expected error to name Claude Code, got: %v", err)
	}
	if !strings.Contains(msg, "Gemini CLI") {
		t.Errorf("expected error to name Gemini CLI, got: %v", err)
	}
}

func writeClaudeJSON(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeRegistrationLocalAndGlobal(t *testing.T) {
	writeClaudeJSON(t, `{
  "mcpServers": {"srv": {"type": "stdio", "command": "global-cmd", "args": ["g"]}},
  "projects": {"/proj": {"mcpServers": {"srv": {"type": "stdio", "command": "node", "args": ["/proj/.mcp/servers/srv/index.js"]}}}}
}`)

	local, err := claudeRegistration("/proj", "srv", false)
	if err != nil {
		t.Fatalf("local: %v", err)
	}
	if local.Command != "node" || len(local.Args) != 1 || local.Args[0] != "/proj/.mcp/servers/srv/index.js" {
		t.Errorf("local = %+v", local)
	}

	global, err := claudeRegistration("/proj", "srv", true)
	if err != nil {
		t.Fatalf("global: %v", err)
	}
	if global.Command != "global-cmd" || len(global.Args) != 1 || global.Args[0] != "g" {
		t.Errorf("global = %+v", global)
	}
}

func TestClaudeRegistrationNotFound(t *testing.T) {
	writeClaudeJSON(t, `{"projects": {"/other": {"mcpServers": {"srv": {"command": "x"}}}}}`)

	if _, err := claudeRegistration("/proj", "srv", false); err == nil || !strings.Contains(err.Error(), "srv") {
		t.Errorf("expected not-found error naming srv, got: %v", err)
	}
	if _, err := claudeRegistration("/proj", "srv", true); err == nil {
		t.Error("expected not-found error for global scope, got nil")
	}
}

func TestClaudeRegistrationMissingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := claudeRegistration("/proj", "srv", false); err == nil {
		t.Error("expected error for missing ~/.claude.json, got nil")
	}
}

func TestRegistrationProblem(t *testing.T) {
	want := registration{Command: "node", Args: []string{"/a/index.js"}}
	tests := []struct {
		name    string
		got     registration
		err     error
		problem bool
		substr  string
	}{
		{"match", registration{Command: "node", Args: []string{"/a/index.js"}}, nil, false, ""},
		{"other command", registration{Command: "python", Args: []string{"/a/index.js"}}, nil, true, `runs "python /a/index.js", want "node /a/index.js"`},
		{"other args", registration{Command: "node", Args: []string{"/b/index.js"}}, nil, true, "/b/index.js"},
		{"read error", registration{}, fmt.Errorf("server srv not found"), true, "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := registrationProblem("Claude Code", "srv", want, tt.got, tt.err)
			if (got != "") != tt.problem {
				t.Fatalf("problem = %q, want problem %v", got, tt.problem)
			}
			if !tt.problem {
				return
			}
			if !strings.HasPrefix(got, "srv: Claude Code") || !strings.Contains(got, tt.substr) || strings.Contains(got, "\n") {
				t.Errorf("problem = %q, want one line with %q", got, tt.substr)
			}
		})
	}
}

func TestRegistrationProblemNilAndEmptyArgs(t *testing.T) {
	got := registrationProblem("Gemini CLI", "srv", registration{Command: "bin"}, registration{Command: "bin", Args: []string{}}, nil)
	if got != "" {
		t.Errorf("nil and empty args reported as a problem: %q", got)
	}
}
