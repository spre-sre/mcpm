package cmd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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

func TestUpdateCmdCheckFlag(t *testing.T) {
	f := updateCmd.Flags().Lookup("check")
	if f == nil {
		t.Fatal("flag --check not defined")
	}
	if f.DefValue != "false" {
		t.Errorf("--check default = %q, want %q", f.DefValue, "false")
	}
}

func TestCheckServerRemoteErrors(t *testing.T) {
	dir := chdirTemp(t)
	if err := checkServerRemote("ghost", false); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing server: got %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".mcp", "servers", "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkServerRemote("plain", false); err == nil || !strings.Contains(err.Error(), "failed to open repository") {
		t.Fatalf("non-git server: got %v", err)
	}
}

func TestUpdateCheckReportsNewCommits(t *testing.T) {
	dir := chdirTemp(t)
	upstream := filepath.Join(dir, "upstream")
	writeFiles(t, upstream, map[string]string{"README": "v1"})
	commitAll(t, upstream)
	srv := filepath.Join(dir, ".mcp", "servers", "alpha")
	if _, err := git.PlainClone(srv, false, &git.CloneOptions{URL: upstream}); err != nil {
		t.Fatal(err)
	}
	before := headCommit(srv)

	oldAll, oldGlobal, oldDry, oldCheck := updateAll, updateGlobal, updateDryRun, updateCheck
	t.Cleanup(func() { updateAll, updateGlobal, updateDryRun, updateCheck = oldAll, oldGlobal, oldDry, oldCheck })
	updateAll, updateGlobal, updateDryRun, updateCheck = true, false, false, true

	out := captureStdout(t, func() { updateCmd.Run(updateCmd, nil) })
	if !strings.Contains(out, "Checking alpha...") || !strings.Contains(out, "Up to date") {
		t.Fatalf("unchanged remote: output %q", out)
	}

	after := commitMore(t, upstream, map[string]string{"README": "v2"})
	out = captureStdout(t, func() { updateCmd.Run(updateCmd, nil) })
	want := "New commits available (" + before[:7] + " -> " + after[:7] + ")"
	if !strings.Contains(out, want) {
		t.Fatalf("output missing %q:\n%s", want, out)
	}
	for _, unwanted := range []string{"Updated successfully", "Pulling"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("check output contains %q:\n%s", unwanted, out)
		}
	}
	if got := headCommit(srv); got != before {
		t.Fatalf("HEAD moved to %s, want %s", got, before)
	}
	if data, err := os.ReadFile(filepath.Join(srv, "README")); err != nil || string(data) != "v1" {
		t.Fatalf("working tree changed: %q, %v", data, err)
	}
}

// clonePinnedServer clones ref of upstream into .mcp/servers/<name> under dir
// and records the pin as clonePinned does.
func clonePinnedServer(t *testing.T, dir, upstream, name string, ref plumbing.ReferenceName) string {
	t.Helper()
	srv := filepath.Join(dir, ".mcp", "servers", name)
	repo, err := git.PlainClone(srv, false, &git.CloneOptions{URL: upstream, ReferenceName: ref, SingleBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := recordPin(repo, ref); err != nil {
		t.Fatal(err)
	}
	return srv
}

func setUpstreamRef(t *testing.T, upstream string, ref plumbing.ReferenceName, hash string) {
	t.Helper()
	repo, err := git.PlainOpen(upstream)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(ref, plumbing.NewHash(hash))); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateKeepsPinnedTag(t *testing.T) {
	dir := chdirTemp(t)
	upstream := filepath.Join(dir, "upstream")
	writeFiles(t, upstream, map[string]string{"README": "v1"})
	pinned := commitAll(t, upstream)
	setUpstreamRef(t, upstream, "refs/tags/v1.2.0", pinned)
	srv := clonePinnedServer(t, dir, upstream, "alpha", "refs/tags/v1.2.0")
	commitMore(t, upstream, map[string]string{"README": "v2"})

	if pin, err := serverPin(srv); err != nil || pin.Short() != "v1.2.0" {
		t.Fatalf("serverPin = %q, %v; want v1.2.0", pin, err)
	}
	out := captureStdout(t, func() {
		if err := checkServerRemote("alpha", false); err != nil {
			t.Errorf("checkServerRemote: %v", err)
		}
	})
	if want := "Pinned to v1.2.0 (" + pinned[:7] + ")"; !strings.Contains(out, want) || strings.Contains(out, "New commits") {
		t.Fatalf("check output %q, want %q", out, want)
	}
	out = captureStdout(t, func() {
		if err := pullServer(srv, "refs/tags/v1.2.0"); err != nil {
			t.Errorf("pullServer: %v", err)
		}
	})
	if !strings.Contains(out, "Pinned to v1.2.0") || strings.Contains(out, "Pulling latest changes...") {
		t.Errorf("pull output %q", out)
	}
	if got := headCommit(srv); got != pinned {
		t.Fatalf("HEAD moved to %s, want pinned %s", got, pinned)
	}
}

func TestUpdateKeepsPinnedBranch(t *testing.T) {
	dir := chdirTemp(t)
	upstream := filepath.Join(dir, "upstream")
	writeFiles(t, upstream, map[string]string{"README": "v1"})
	first := commitAll(t, upstream)
	setUpstreamRef(t, upstream, "refs/heads/stable", first)
	srv := clonePinnedServer(t, dir, upstream, "alpha", "refs/heads/stable")
	second := commitMore(t, upstream, map[string]string{"README": "v2"})

	out := captureStdout(t, func() {
		if err := checkServerRemote("alpha", false); err != nil {
			t.Errorf("checkServerRemote: %v", err)
		}
	})
	if !strings.Contains(out, "Pinned to branch stable") || !strings.Contains(out, "Up to date") {
		t.Fatalf("check output %q", out)
	}
	captureStdout(t, func() {
		if err := pullServer(srv, "refs/heads/stable"); err != nil {
			t.Errorf("pullServer: %v", err)
		}
	})
	if got := headCommit(srv); got != first {
		t.Fatalf("default branch commit pulled: HEAD %s, want %s", got, first)
	}

	setUpstreamRef(t, upstream, "refs/heads/stable", second)
	captureStdout(t, func() {
		if err := pullServer(srv, "refs/heads/stable"); err != nil {
			t.Errorf("pullServer: %v", err)
		}
	})
	if got := headCommit(srv); got != second {
		t.Fatalf("pinned branch not pulled: HEAD %s, want %s", got, second)
	}
}

func TestDryRunUpdatePinned(t *testing.T) {
	dir := chdirTemp(t)
	upstream := filepath.Join(dir, "upstream")
	writeFiles(t, upstream, map[string]string{"README": "v1"})
	setUpstreamRef(t, upstream, "refs/tags/v1", commitAll(t, upstream))
	clonePinnedServer(t, dir, upstream, "alpha", "refs/tags/v1")

	out := captureStdout(t, func() {
		if err := dryRunUpdate("alpha", false); err != nil {
			t.Errorf("dryRunUpdate: %v", err)
		}
	})
	if !strings.Contains(out, "Would keep pinned version v1") {
		t.Fatalf("dry-run output %q", out)
	}
}

func TestUpdateAllServersSummary(t *testing.T) {
	dir := chdirTemp(t)
	upstream := filepath.Join(dir, "upstream")
	writeFiles(t, upstream, map[string]string{"README": "v1"})
	setUpstreamRef(t, upstream, "refs/tags/v1.2.0", commitAll(t, upstream))
	clonePinnedServer(t, dir, upstream, "pinned", "refs/tags/v1.2.0")
	servers := filepath.Join(dir, ".mcp", "servers")
	for _, name := range []string{"current", "moved"} {
		if _, err := git.PlainClone(filepath.Join(servers, name), false, &git.CloneOptions{URL: upstream}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(servers, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldDry, oldCheck := updateDryRun, updateCheck
	t.Cleanup(func() { updateDryRun, updateCheck = oldDry, oldCheck })
	updateDryRun, updateCheck = false, false

	update := func(name string, _ bool) error {
		switch name {
		case "broken":
			return errors.New("failed to pull: not a repository")
		case "moved":
			commitMore(t, filepath.Join(servers, name), map[string]string{"README": "v2"})
		}
		return nil
	}
	var ok bool
	out := captureStdout(t, func() { ok = updateAllServers(update) })
	if ok {
		t.Error("updateAllServers reported success with a failed server")
	}
	want := "Summary:\n" +
		"  Updated: moved\n" +
		"  Already current: current\n" +
		"  Kept on pin: pinned (v1.2.0)\n" +
		"  Failed: broken (failed to pull: not a repository)\n"
	if !strings.HasSuffix(out, want) {
		t.Fatalf("output %q, want suffix %q", out, want)
	}

	out = captureStdout(t, func() {
		ok = updateAllServers(func(name string, _ bool) error { return nil })
	})
	if !ok || strings.Contains(out, "Failed:") {
		t.Fatalf("all succeeded: ok %v, output %q", ok, out)
	}
}

func TestUpdateAllServersDryRunNoSummary(t *testing.T) {
	dir := chdirTemp(t)
	if err := os.MkdirAll(filepath.Join(dir, ".mcp", "servers", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldDry := updateDryRun
	t.Cleanup(func() { updateDryRun = oldDry })
	updateDryRun = true
	out := captureStdout(t, func() { updateAllServers(dryRunUpdate) })
	if strings.Contains(out, "Summary:") {
		t.Fatalf("dry-run printed a summary: %q", out)
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
