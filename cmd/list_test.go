package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
