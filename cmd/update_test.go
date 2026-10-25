package cmd

import (
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

func TestUpdateCmdAllNoServers(t *testing.T) {
	chdirTemp(t)
	oldAll, oldGlobal := updateAll, updateGlobal
	t.Cleanup(func() { updateAll, updateGlobal = oldAll, oldGlobal })
	updateAll, updateGlobal = true, false

	// With no installed servers, Run returns without exiting.
	updateCmd.Run(updateCmd, nil)
}
