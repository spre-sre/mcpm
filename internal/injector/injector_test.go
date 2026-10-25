package injector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// chdirTemp changes into a new temp dir for the test and returns the working
// directory as os.Getwd reports it.
func chdirTemp(t *testing.T) string {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return cwd
}

func TestCheckRegisteredMatches(t *testing.T) {
	cwd := chdirTemp(t)
	command := filepath.Join(cwd, ".mcp", "servers", "alpha", "mcp-server")
	result := &builder.BuildResult{Command: command}
	if err := Register(result, []TargetTool{TargetGeminiCLI}, nil, false); err != nil {
		t.Fatal(err)
	}
	claude, _ := json.Marshal(map[string]any{
		"projects": map[string]any{
			cwd: map[string]any{"mcpServers": map[string]McpServerDef{
				"alpha": {Type: "stdio", Command: command, Args: []string{}},
			}},
		},
	})
	writeClaudeConfig(t, string(claude))

	problems := Check("alpha", result, []TargetTool{TargetClaudeCode, TargetGeminiCLI}, false)
	if len(problems) != 0 {
		t.Errorf("problems = %q, want none", problems)
	}
}

func TestCheckReportsMissingAndMismatch(t *testing.T) {
	cwd := chdirTemp(t)
	writeClaudeConfig(t, `{"mcpServers": {}}`)
	stale := &builder.BuildResult{Command: filepath.Join(cwd, ".mcp", "servers", "alpha", "old")}
	if err := Register(stale, []TargetTool{TargetGeminiCLI}, nil, false); err != nil {
		t.Fatal(err)
	}
	current := &builder.BuildResult{Command: filepath.Join(cwd, ".mcp", "servers", "alpha", "new")}

	problems := Check("alpha", current, []TargetTool{TargetClaudeCode, TargetGeminiCLI}, false)
	if len(problems) != 2 {
		t.Fatalf("problems = %q, want 2", problems)
	}
	if problems[0] != "alpha: not registered in Claude Code" {
		t.Errorf("problems[0] = %q", problems[0])
	}
	if !strings.HasPrefix(problems[1], "alpha: Gemini CLI runs ") || !strings.Contains(problems[1], current.Command) {
		t.Errorf("problems[1] = %q, want mismatch naming %s", problems[1], current.Command)
	}
}

func TestCheckReportsUnreadableConfigAndUnknownTool(t *testing.T) {
	chdirTemp(t)
	writeClaudeConfig(t, "not json")
	result := &builder.BuildResult{Command: "x"}

	problems := Check("alpha", result, []TargetTool{TargetClaudeCode, "vim"}, true)
	if len(problems) != 2 {
		t.Fatalf("problems = %q, want 2", problems)
	}
	if !strings.HasPrefix(problems[0], "alpha: Claude Code: could not parse ") {
		t.Errorf("problems[0] = %q", problems[0])
	}
	if problems[1] != `alpha: unknown tool "vim"` {
		t.Errorf("problems[1] = %q", problems[1])
	}
}

func TestSameArgsNilEqualsEmpty(t *testing.T) {
	if !sameArgs(nil, []string{}) {
		t.Error("nil and empty args should be equal")
	}
	if sameArgs([]string{"a"}, []string{"b"}) {
		t.Error("different args should not be equal")
	}
}

func TestRegisterAlphaBetaGemini(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
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
