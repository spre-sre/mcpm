package injector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mcpm/internal/builder"
)

func TestServerName(t *testing.T) {
	base := filepath.Join(t.TempDir(), "project")

	tests := []struct {
		name    string
		result  *builder.BuildResult
		want    string
		wantErr bool
	}{
		{
			name: "go build with command under .mcp/servers and empty args",
			result: &builder.BuildResult{
				Command: filepath.Join(base, ".mcp", "servers", "alpha", "mcp-server"),
				Args:    []string{},
			},
			want: "alpha",
		},
		{
			name: "python build with args under .mcp/servers",
			result: &builder.BuildResult{
				Command: filepath.Join(base, ".mcp", "servers", "py", ".venv", "bin", "python"),
				Args:    []string{filepath.Join(base, ".mcp", "servers", "py", "main.py")},
			},
			want: "py",
		},
		{
			name: "node build with args under .mcp/servers",
			result: &builder.BuildResult{
				Command: "node",
				Args:    []string{filepath.Join(base, ".mcp", "servers", "web", "dist", "index.js")},
			},
			want: "web",
		},
		{
			name:    "error: command node with args dist/index.js",
			result:  &builder.BuildResult{Command: "node", Args: []string{"dist/index.js"}},
			wantErr: true,
		},
		{
			name:    "error: no .mcp in path",
			result:  &builder.BuildResult{Command: "/home/u/servers/x/bin", Args: []string{}},
			wantErr: true,
		},
		{
			name:    "error: nothing after .mcp/servers",
			result:  &builder.BuildResult{Command: filepath.Join(base, ".mcp", "servers"), Args: []string{}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := serverName(tt.result)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got name %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("serverName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRegisterAlphaBetaGemini(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	alphaCmd := filepath.Join(tmpDir, ".mcp", "servers", "alpha", "mcp-server")
	betaCmd := filepath.Join(tmpDir, ".mcp", "servers", "beta", "mcp-server")

	err = Register(&builder.BuildResult{Command: alphaCmd, Args: []string{}}, []TargetTool{TargetGeminiCLI}, nil, false)
	if err != nil {
		t.Fatalf("register alpha: %v", err)
	}

	err = Register(&builder.BuildResult{Command: betaCmd, Args: []string{}}, []TargetTool{TargetGeminiCLI}, nil, false)
	if err != nil {
		t.Fatalf("register beta: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, ".gemini", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var cfg GeminiConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse settings: %v", err)
	}

	if _, ok := cfg.McpServers["alpha"]; !ok {
		t.Error("alpha server missing from settings")
	}
	if _, ok := cfg.McpServers["beta"]; !ok {
		t.Error("beta server missing from settings")
	}
	if len(cfg.McpServers) != 2 {
		t.Errorf("expected 2 servers, got %d", len(cfg.McpServers))
	}
}
