package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpm/internal/builder"
	"mcpm/internal/injector"
)

// writeRegistrations writes the Claude Code (local scope of dir) and Gemini
// CLI (project) registrations of servers, with no global registrations.
func writeRegistrations(t *testing.T, dir string, servers map[string]registration) {
	t.Helper()
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("HOME", t.TempDir())
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
	n, err := runDoctor(&buf, doctorTools(false, false), doctorScopes(false, false))
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

	assertDoctorLines(t, doctorScopes(false, false), []string{
		"broken: build failed: invalid mcp.json: runCmd is required",
		"plain: no project detected, nothing to run",
		`stale: Claude Code (local) runs "node /old/index.js", want "node ` + filepath.Join(stale, "index.js") + `"`,
		`stale: Gemini CLI (local) runs "node /old/index.js", want "node ` + filepath.Join(stale, "index.js") + `"`,
		"unreg: not registered in Claude Code (local or global)",
		"unreg: not registered in Gemini CLI (local or global)",
	})
}

// assertDoctorLines runs doctor for both clients in scopes and compares its
// output with want, line by line.
func assertDoctorLines(t *testing.T, scopes []bool, want []string) {
	t.Helper()
	var buf bytes.Buffer
	n, err := runDoctor(&buf, doctorTools(false, false), scopes)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	if buf.Len() > 0 {
		lines = strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	}
	if n != len(lines) {
		t.Fatalf("count %d, but %d lines: %q", n, len(lines), buf.String())
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

func TestRunDoctorChecksBothScopes(t *testing.T) {
	dir := chdirTemp(t)
	srv := filepath.Join(dir, ".mcp", "servers", "srv")
	writeFiles(t, srv, map[string]string{"mcp.json": `{"runCmd":"node","args":["index.js"]}`, "index.js": ""})
	good := registration{Command: "node", Args: []string{filepath.Join(srv, "index.js")}}
	writeRegistrations(t, dir, map[string]registration{"srv": good})
	claude, err := json.Marshal(map[string]interface{}{
		"mcpServers": map[string]registration{"srv": {Command: "node", Args: []string{"/old/index.js"}}},
		"projects":   map[string]interface{}{dir: map[string]interface{}{"mcpServers": map[string]registration{"srv": good}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, os.Getenv("CLAUDE_CONFIG_DIR"), map[string]string{".claude.json": string(claude)})

	stale := `srv: Claude Code (global) runs "node /old/index.js", want "node ` + filepath.Join(srv, "index.js") + `"`
	assertDoctorLines(t, doctorScopes(false, false), []string{stale})
	assertDoctorLines(t, doctorScopes(true, true), []string{stale})
	assertDoctorLines(t, doctorScopes(true, false), nil)
	assertDoctorLines(t, doctorScopes(false, true), []string{stale, "srv: not registered in Gemini CLI (global)"})
}

func TestDoctorScopes(t *testing.T) {
	for _, tc := range []struct {
		local, global bool
		want          string
	}{
		{false, false, "[false true]"},
		{true, true, "[false true]"},
		{true, false, "[false]"},
		{false, true, "[true]"},
	} {
		if got := fmt.Sprint(doctorScopes(tc.local, tc.global)); got != tc.want {
			t.Errorf("doctorScopes(%v, %v) = %s, want %s", tc.local, tc.global, got, tc.want)
		}
	}
}

func TestWithScope(t *testing.T) {
	got := withScope("Claude Code: Claude Code: bad", "Claude Code", injector.TargetClaudeCode, injector.ScopeGlobal)
	if want := "Claude Code: Claude Code (global): bad"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := withScope("other: x", "srv", injector.TargetClaudeCode, injector.ScopeLocal); got != "other: x" {
		t.Fatalf("foreign problem changed: %q", got)
	}
}

// writeEnvRegistrations writes the Claude Code (local scope of dir) and
// Gemini CLI (project) registrations of name, each with its own env.
func writeEnvRegistrations(t *testing.T, dir, name string, claude, gemini envServerDef) {
	t.Helper()
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("HOME", t.TempDir())
	claudeData, err := json.Marshal(map[string]interface{}{
		"projects": map[string]interface{}{dir: map[string]interface{}{"mcpServers": map[string]envServerDef{name: claude}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	geminiData, err := json.Marshal(map[string]interface{}{"mcpServers": map[string]envServerDef{name: gemini}})
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, claudeDir, map[string]string{".claude.json": string(claudeData)})
	writeFiles(t, dir, map[string]string{filepath.Join(".gemini", "settings.json"): string(geminiData)})
}

func TestRunDoctorReportsMissingEnv(t *testing.T) {
	dir := chdirTemp(t)
	srv := filepath.Join(dir, ".mcp", "servers", "needy")
	writeFiles(t, srv, map[string]string{"mcp.json": `{"runCmd":"node","args":["index.js"],"requiredEnv":["API_KEY","REGION"]}`, "index.js": ""})
	args := []string{filepath.Join(srv, "index.js")}
	writeEnvRegistrations(t, dir, "needy",
		envServerDef{Command: "node", Args: args, Env: map[string]string{"API_KEY": "s3cret"}},
		envServerDef{Command: "node", Args: args, Env: map[string]string{"API_KEY": "s3cret", "REGION": "eu"}})

	var buf bytes.Buffer
	n, err := runDoctor(&buf, doctorTools(false, false), doctorScopes(false, false))
	if err != nil {
		t.Fatal(err)
	}
	want := "needy: Claude Code (local) is missing environment variable(s) REGION (run mcpm env set needy KEY=VALUE)\n"
	if n != 1 || buf.String() != want {
		t.Fatalf("got %d problems: %q, want %q", n, buf.String(), want)
	}
	if strings.Contains(buf.String(), "s3cret") {
		t.Fatalf("env value printed: %q", buf.String())
	}

	buf.Reset()
	if n, err := runDoctor(&buf, []injector.TargetTool{injector.TargetGeminiCLI}, bothScopes); err != nil || n != 0 {
		t.Fatalf("--gemini: got %d, %v: %q", n, err, buf.String())
	}
}

func TestMissingKeys(t *testing.T) {
	got := missingKeys([]string{"A", "B", "C"}, map[string]string{"B": ""})
	if strings.Join(got, ",") != "A,C" {
		t.Fatalf("got %v", got)
	}
	if got := missingKeys([]string{"A"}, nil); len(got) != 1 {
		t.Fatalf("nil env: got %v", got)
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

	got := checkServer("x", []injector.TargetTool{injector.TargetClaudeCode}, []bool{false})
	if len(got) != 1 || got[0] != "x: build failed: boom" {
		t.Fatalf("got %q", got)
	}
}

func TestCheckServerMissingFolder(t *testing.T) {
	chdirTemp(t)
	got := checkServer("gone", doctorTools(false, false), bothScopes)
	if len(got) != 1 || !strings.HasPrefix(got[0], "gone: folder missing: ") {
		t.Fatalf("got %q", got)
	}
}

// setupInfoServer installs srv with a local Claude Code registration and a
// global Gemini CLI registration that carries a secret env value.
func setupInfoServer(t *testing.T) string {
	t.Helper()
	dir := chdirTemp(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	writeFiles(t, filepath.Join(dir, ".mcp", "servers", "srv"), map[string]string{"index.js": ""})
	writeFiles(t, claudeDir, map[string]string{".claude.json": `{"projects": {"` + dir +
		`": {"mcpServers": {"srv": {"command": "node", "args": ["index.js"]}}}}}`})
	writeFiles(t, home, map[string]string{filepath.Join(".gemini", "settings.json"): `{"mcpServers": {"srv": ` +
		`{"command": "/opt/srv", "env": {"TOKEN": "s3cret", "API_KEY": "hunter2"}}}}`})
	return dir
}

func TestCollectServerInfo(t *testing.T) {
	dir := setupInfoServer(t)
	info, err := collectServerInfo(dir, "srv")
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != filepath.Join(dir, ".mcp", "servers", "srv") || info.Repository != "" || info.Commit != "" {
		t.Fatalf("info = %+v", info)
	}
	if len(info.Registrations) != 2 {
		t.Fatalf("registrations = %+v", info.Registrations)
	}
	claude, gemini := info.Registrations[0], info.Registrations[1]
	if claude.Tool != injector.TargetClaudeCode || claude.Scope != injector.ScopeLocal || claude.Runtime != "node" {
		t.Errorf("claude = %+v", claude)
	}
	if gemini.Tool != injector.TargetGeminiCLI || gemini.Scope != injector.ScopeGlobal || gemini.Runtime != "binary" ||
		strings.Join(gemini.EnvNames, ",") != "API_KEY,TOKEN" {
		t.Errorf("gemini = %+v", gemini)
	}
}

func TestWriteServerInfoHidesEnvValues(t *testing.T) {
	dir := setupInfoServer(t)
	info, err := collectServerInfo(dir, "srv")
	if err != nil {
		t.Fatal(err)
	}
	for _, asJSON := range []bool{false, true} {
		var buf bytes.Buffer
		if err := writeServerInfo(&buf, info, asJSON); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if strings.Contains(out, "s3cret") || strings.Contains(out, "hunter2") {
			t.Errorf("json=%v: env value printed: %s", asJSON, out)
		}
		if !strings.Contains(out, "API_KEY") || !strings.Contains(out, "TOKEN") {
			t.Errorf("json=%v: env names missing: %s", asJSON, out)
		}
	}

	var buf bytes.Buffer
	if err := writeServerInfo(&buf, info, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Name:       srv\n",
		"Repository: (none)\n",
		"  Claude Code (local): node index.js [node]\n",
		"  Gemini CLI (global): /opt/srv [binary]\n    env: API_KEY, TOKEN\n",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("text output missing %q:\n%s", want, buf.String())
		}
	}

	buf.Reset()
	if err := writeServerInfo(&buf, info, true); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	regs, _ := decoded["registrations"].([]interface{})
	if len(regs) != 2 {
		t.Fatalf("registrations = %v", decoded["registrations"])
	}
	first, _ := regs[0].(map[string]interface{})
	if first["tool"] != "claude-code" || first["scope"] != "local" || first["runtime"] != "node" {
		t.Errorf("first registration = %v", first)
	}
}

func TestCollectServerInfoUnregistered(t *testing.T) {
	dir := chdirTemp(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	writeFiles(t, filepath.Join(dir, ".mcp", "servers", "lone"), map[string]string{"README": ""})
	info, err := collectServerInfo(dir, "lone")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeServerInfo(&buf, info, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Registered: nowhere\n") {
		t.Errorf("got %s", buf.String())
	}
	buf.Reset()
	if err := writeServerInfo(&buf, info, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"registrations": []`) {
		t.Errorf("got %s", buf.String())
	}
}

func TestCollectServerInfoNotInstalled(t *testing.T) {
	dir := chdirTemp(t)
	_, err := collectServerInfo(dir, "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
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
