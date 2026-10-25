package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"mcpm/internal/builder"
	"mcpm/internal/injector"
)

// writeRegistrations writes the Claude Code (local scope of dir) and Gemini
// CLI (project) registrations of servers.
func writeRegistrations(t *testing.T, dir string, servers map[string]registration) {
	t.Helper()
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	claude, err := json.Marshal(map[string]interface{}{
		"projects": map[string]interface{}{dir: map[string]interface{}{"mcpServers": servers}},
	})
	if err != nil {
		t.Fatal(err)
	}
	gemini, err := json.Marshal(map[string]interface{}{"mcpServers": servers})
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, claudeDir, map[string]string{".claude.json": string(claude)})
	writeFiles(t, dir, map[string]string{filepath.Join(".gemini", "settings.json"): string(gemini)})
}

func TestRunDoctorNoProblems(t *testing.T) {
	dir := chdirTemp(t)
	srv := filepath.Join(dir, ".mcp", "servers", "good")
	writeFiles(t, srv, map[string]string{"mcp.json": `{"runCmd":"node","args":["index.js"]}`, "index.js": ""})
	writeRegistrations(t, dir, map[string]registration{
		"good": {Command: "node", Args: []string{filepath.Join(srv, "index.js")}},
	})

	var buf bytes.Buffer
	n, err := runDoctor(&buf, doctorTools(false, false), false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || buf.Len() != 0 {
		t.Fatalf("got %d problems: %q", n, buf.String())
	}
}

func TestRunDoctorReportsProblems(t *testing.T) {
	dir := chdirTemp(t)
	servers := filepath.Join(dir, ".mcp", "servers")
	writeFiles(t, filepath.Join(servers, "broken"), map[string]string{"mcp.json": `{"runCmd":""}`})
	writeFiles(t, filepath.Join(servers, "plain"), map[string]string{"README": ""})
	stale := filepath.Join(servers, "stale")
	writeFiles(t, stale, map[string]string{"mcp.json": `{"runCmd":"node","args":["index.js"]}`, "index.js": ""})
	writeFiles(t, filepath.Join(servers, "unreg"), map[string]string{"mcp.json": `{"runCmd":"node"}`})
	writeRegistrations(t, dir, map[string]registration{
		"stale": {Command: "node", Args: []string{"/old/index.js"}},
	})

	var buf bytes.Buffer
	n, err := runDoctor(&buf, doctorTools(false, false), false)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if n != len(lines) {
		t.Fatalf("count %d, but %d lines: %q", n, len(lines), buf.String())
	}
	want := []string{
		"broken: build failed: invalid mcp.json: runCmd is required",
		"plain: no project detected, nothing to run",
		`stale: Claude Code runs "node /old/index.js", want "node ` + filepath.Join(stale, "index.js") + `"`,
		`stale: Gemini CLI runs "node /old/index.js", want "node ` + filepath.Join(stale, "index.js") + `"`,
		"unreg: not registered in Claude Code",
		"unreg: not registered in Gemini CLI",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %q, want %q", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d: got %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestCheckServerBuildErrors(t *testing.T) {
	dir := chdirTemp(t)
	writeFiles(t, filepath.Join(dir, ".mcp", "servers", "x"), map[string]string{"go.mod": ""})
	writeRegistrations(t, dir, map[string]registration{"x": {Command: "bin"}})
	old := detectAndBuild
	t.Cleanup(func() { detectAndBuild = old })
	detectAndBuild = func(string) (*builder.BuildResult, error) {
		return &builder.BuildResult{Command: "bin", BuildErrors: []error{errors.New("boom")}}, nil
	}

	got := checkServer("x", []injector.TargetTool{injector.TargetClaudeCode}, false)
	if len(got) != 1 || got[0] != "x: build failed: boom" {
		t.Fatalf("got %q", got)
	}
}

func TestCheckServerMissingFolder(t *testing.T) {
	chdirTemp(t)
	got := checkServer("gone", doctorTools(false, false), false)
	if len(got) != 1 || !strings.HasPrefix(got[0], "gone: folder missing: ") {
		t.Fatalf("got %q", got)
	}
}

func TestDoctorTools(t *testing.T) {
	if got := doctorTools(true, false); len(got) != 1 || got[0] != injector.TargetClaudeCode {
		t.Fatalf("--claude: got %v", got)
	}
	if got := doctorTools(false, true); len(got) != 1 || got[0] != injector.TargetGeminiCLI {
		t.Fatalf("--gemini: got %v", got)
	}
	if got := doctorTools(false, false); len(got) != 2 {
		t.Fatalf("default: got %v", got)
	}
}
