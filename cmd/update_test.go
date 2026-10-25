package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateServerMissing(t *testing.T) {
	chdirTemp(t)
	err := updateServer("ghost", false)
	if err == nil {
		t.Fatal("expected error for missing server, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error naming 'ghost', got: %v", err)
	}
}

func TestUpdateServerNotGitRepo(t *testing.T) {
	dir := chdirTemp(t)
	if err := os.MkdirAll(filepath.Join(dir, ".mcp", "servers", "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := updateServer("plain", true)
	if err == nil {
		t.Fatal("expected error for non-git server directory, got nil")
	}
	if !strings.Contains(err.Error(), "failed to pull") {
		t.Errorf("expected 'failed to pull' in error, got: %v", err)
	}
}

func TestUpdateCmdRegistered(t *testing.T) {
	found := false
	for _, c := range rootCmd.Commands() {
		if c == updateCmd {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("updateCmd is not registered on rootCmd")
	}
	if got := updateCmd.Name(); got != "update" {
		t.Errorf("Name() = %q, want %q", got, "update")
	}
	if updateCmd.ValidArgsFunction == nil {
		t.Error("expected ValidArgsFunction to be set")
	}
}

func TestUpdateCmdFlags(t *testing.T) {
	for _, tc := range []struct{ name, short string }{
		{"all", "a"},
		{"global", "g"},
	} {
		f := updateCmd.Flags().Lookup(tc.name)
		if f == nil {
			t.Fatalf("flag --%s not defined", tc.name)
		}
		if f.Shorthand != tc.short {
			t.Errorf("--%s shorthand = %q, want %q", tc.name, f.Shorthand, tc.short)
		}
		if f.DefValue != "false" {
			t.Errorf("--%s default = %q, want %q", tc.name, f.DefValue, "false")
		}
	}
}

func TestUpdateCmdArgs(t *testing.T) {
	if err := updateCmd.Args(updateCmd, nil); err != nil {
		t.Errorf("zero args: unexpected error %v", err)
	}
	if err := updateCmd.Args(updateCmd, []string{"one"}); err != nil {
		t.Errorf("one arg: unexpected error %v", err)
	}
	if err := updateCmd.Args(updateCmd, []string{"one", "two"}); err == nil {
		t.Error("two args: expected error, got nil")
	}
}

func TestDryRunUpdateMissing(t *testing.T) {
	chdirTemp(t)
	err := dryRunUpdate("ghost", false)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got: %v", err)
	}
}

func TestDryRunUpdateChangesNothing(t *testing.T) {
	dir := chdirTemp(t)
	server := filepath.Join(dir, ".mcp", "servers", "plain")
	if err := os.MkdirAll(server, 0o755); err != nil {
		t.Fatal(err)
	}
	// "plain" is not a git repository, so a real update would fail to pull.
	if err := dryRunUpdate("plain", true); err != nil {
		t.Fatalf("dryRunUpdate: %v", err)
	}
	entries, err := os.ReadDir(server)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("server directory changed: %d entries", len(entries))
	}
}

func TestUpdateAllServersDryRun(t *testing.T) {
	dir := chdirTemp(t)
	for _, name := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(dir, ".mcp", "servers", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldDry := updateDryRun
	t.Cleanup(func() { updateDryRun = oldDry })
	updateDryRun = true

	if !updateAllServers(dryRunUpdate) {
		t.Fatal("updateAllServers reported a failure")
	}
}

// captureStdout returns what fn writes to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	return <-done
}

func TestUpdateAllServersDryRunOutput(t *testing.T) {
	dir := chdirTemp(t)
	if err := os.MkdirAll(filepath.Join(dir, ".mcp", "servers", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldAll, oldGlobal, oldDry := updateAll, updateGlobal, updateDryRun
	t.Cleanup(func() { updateAll, updateGlobal, updateDryRun = oldAll, oldGlobal, oldDry })
	updateAll, updateGlobal, updateDryRun = true, false, true

	out := captureStdout(t, func() { updateCmd.Run(updateCmd, nil) })
	for _, want := range []string{"Would update a...", "Would pull latest changes into", "re-register it (local)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"Updating a", "Updated successfully", "Pulling"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("dry-run output contains %q:\n%s", unwanted, out)
		}
	}
}

func TestUpdateCmdDryRunFlag(t *testing.T) {
	f := updateCmd.Flags().Lookup("dry-run")
	if f == nil {
		t.Fatal("flag --dry-run not defined")
	}
	if f.DefValue != "false" {
		t.Errorf("--dry-run default = %q, want %q", f.DefValue, "false")
	}
}

func TestUpdateCmdAllNoServers(t *testing.T) {
	chdirTemp(t)
	oldAll, oldGlobal := updateAll, updateGlobal
	t.Cleanup(func() { updateAll, updateGlobal = oldAll, oldGlobal })
	updateAll, updateGlobal = true, false

	// With no installed servers, Run returns without exiting.
	updateCmd.Run(updateCmd, nil)
}
