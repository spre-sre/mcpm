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
