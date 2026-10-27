package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"mcpm/internal/injector"
)

func chdirTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	return dir
}

func TestWriteListJSONEmpty(t *testing.T) {
	chdirTemp(t)
	var buf bytes.Buffer
	if err := writeList(&buf, true); err != nil {
		t.Fatal(err)
	}
	var got []listEntry
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("want empty array, got %q", buf.String())
	}
}

func TestWriteListJSONServers(t *testing.T) {
	dir := chdirTemp(t)
	servers := filepath.Join(dir, ".mcp", "servers")
	for _, n := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(servers, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(servers, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := writeList(&buf, true); err != nil {
		t.Fatal(err)
	}
	var got []listEntry
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []listEntry{
		{Name: "alpha", Path: filepath.Join(servers, "alpha")},
		{Name: "beta", Path: filepath.Join(servers, "beta")},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func commitAll(t *testing.T, dir string) string {
	t.Helper()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.AddGlob("."); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}
	hash, err := wt.Commit("init", &git.CommitOptions{Author: sig})
	if err != nil {
		t.Fatal(err)
	}
	return hash.String()
}

func TestWriteListJSONDetails(t *testing.T) {
	dir := chdirTemp(t)
	servers := filepath.Join(dir, ".mcp", "servers")
	custom := filepath.Join(servers, "custom")
	goSrv := filepath.Join(servers, "gosrv")
	node := filepath.Join(servers, "node")
	py := filepath.Join(servers, "py")
	writeFiles(t, custom, map[string]string{"mcp.json": `{"runCmd":"bin/server","args":["--port","1"]}`})
	writeFiles(t, goSrv, map[string]string{"go.mod": "module x\n"})
	writeFiles(t, node, map[string]string{"package.json": `{"main":"lib/main.js"}`})
	writeFiles(t, py, map[string]string{"requirements.txt": "", "src/server.py": ""})
	commit := commitAll(t, custom)
	if err := os.MkdirAll(filepath.Join(servers, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := writeList(&buf, true); err != nil {
		t.Fatal(err)
	}
	var got []listEntry
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(py, ".venv", "bin", "python")
	if runtime.GOOS == "windows" {
		python = filepath.Join(py, ".venv", "Scripts", "python.exe")
	}
	want := []listEntry{
		{Name: "custom", Path: custom, BuildType: "custom", RunCommand: "bin/server --port 1", Commit: commit},
		{Name: "gosrv", Path: goSrv, BuildType: "go", RunCommand: filepath.Join(goSrv, goBinName())},
		{Name: "node", Path: node, BuildType: "node", RunCommand: "node " + filepath.Join(node, "lib", "main.js")},
		{Name: "plain", Path: filepath.Join(servers, "plain")},
		{Name: "py", Path: py, BuildType: "python", RunCommand: python + " " + filepath.Join(py, "src", "server.py")},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if !strings.Contains(buf.String(), `"buildType"`) || !strings.Contains(buf.String(), `"runCommand"`) || !strings.Contains(buf.String(), `"commit"`) {
		t.Fatalf("missing keys in %s", buf.String())
	}
}

func TestNodeEntryPrefersDist(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"package.json": `{"main":"lib/main.js"}`, "dist/index.js": ""})
	if got, want := nodeEntry(dir), filepath.Join(dir, "dist", "index.js"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	other := t.TempDir()
	writeFiles(t, other, map[string]string{"package.json": `{}`})
	if got, want := nodeEntry(other), filepath.Join(other, "index.js"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestManifestRunCommandInvalid(t *testing.T) {
	dir := t.TempDir()
	if got := manifestRunCommand(dir); got != "" {
		t.Fatalf("missing manifest: got %q", got)
	}
	writeFiles(t, dir, map[string]string{"mcp.json": `{"runCmd":"  "}`})
	if got := manifestRunCommand(dir); got != "" {
		t.Fatalf("empty runCmd: got %q", got)
	}
	if got := pythonRunCommand(dir); got != "" {
		t.Fatalf("no python entry: got %q", got)
	}
}

func TestWriteListServersIsFile(t *testing.T) {
	dir := chdirTemp(t)
	if err := os.MkdirAll(filepath.Join(dir, ".mcp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mcp", "servers"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, asJSON := range []bool{true, false} {
		var buf bytes.Buffer
		if err := writeList(&buf, asJSON); err == nil {
			t.Fatalf("asJSON=%v: expected error", asJSON)
		}
		if buf.Len() != 0 {
			t.Fatalf("asJSON=%v: expected no output, got %q", asJSON, buf.String())
		}
	}
}

func TestWriteListText(t *testing.T) {
	dir := chdirTemp(t)

	var buf bytes.Buffer
	if err := writeList(&buf, false); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "No servers installed in .mcp/servers/\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	servers := filepath.Join(dir, ".mcp", "servers")
	if err := os.MkdirAll(filepath.Join(servers, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := writeList(&buf, false); err != nil {
		t.Fatal(err)
	}
	want := "Installed MCP servers:\n  • alpha (" + filepath.Join(servers, "alpha") + ")\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
}

func TestWriteListMarksOff(t *testing.T) {
	dir := chdirTemp(t)
	t.Setenv("HOME", t.TempDir())
	servers := filepath.Join(dir, ".mcp", "servers")
	for _, n := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(servers, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	entry := json.RawMessage(`{"command":"node"}`)
	writeOff(t, dir, false, offServers{"alpha": {injector.TargetGeminiCLI: entry, injector.TargetClaudeCode: entry}})
	writeOff(t, dir, true, offServers{"alpha": {injector.TargetGeminiCLI: entry}, "ghost": {injector.TargetClaudeCode: entry}})

	var buf bytes.Buffer
	if err := writeList(&buf, false); err != nil {
		t.Fatal(err)
	}
	want := "Installed MCP servers:\n" +
		"  • alpha (" + filepath.Join(servers, "alpha") + ") [off: Claude Code (local), Gemini CLI (local), Gemini CLI (global)]\n" +
		"  • beta (" + filepath.Join(servers, "beta") + ")\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}

	buf.Reset()
	if err := writeList(&buf, true); err != nil {
		t.Fatal(err)
	}
	var got []listEntry
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Off != "Claude Code (local), Gemini CLI (local), Gemini CLI (global)" || got[1].Off != "" {
		t.Fatalf("got %+v", got)
	}
	if strings.Count(buf.String(), `"off"`) != 1 {
		t.Fatalf("want off only for alpha: %s", buf.String())
	}
}

func TestWriteListBadOffFile(t *testing.T) {
	dir := chdirTemp(t)
	t.Setenv("HOME", t.TempDir())
	writeFiles(t, dir, map[string]string{filepath.Join(".mcp", "disabled.json"): "{"})
	if err := os.MkdirAll(filepath.Join(dir, ".mcp", "servers", "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeList(&buf, false); err == nil || !strings.Contains(err.Error(), "disabled.json") {
		t.Fatalf("err = %v", err)
	}
}

func TestWriteNames(t *testing.T) {
	dir := chdirTemp(t)

	var buf bytes.Buffer
	if err := writeNames(&buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}

	servers := filepath.Join(dir, ".mcp", "servers")
	for _, n := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(servers, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(servers, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := writeNames(&buf); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "alpha\nbeta\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriteNamesServersIsFile(t *testing.T) {
	dir := chdirTemp(t)
	if err := os.MkdirAll(filepath.Join(dir, ".mcp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mcp", "servers"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeNames(&buf); err == nil {
		t.Fatal("expected error")
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
}

func TestListNamesFlag(t *testing.T) {
	f := listCmd.Flags().Lookup("names")
	if f == nil {
		t.Fatal("list has no --names flag")
	}
	if f.DefValue != "false" {
		t.Fatalf("--names default = %q, want false", f.DefValue)
	}
}
