package cmd

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-git/go-git/v5/plumbing"
)

type fakeModel struct {
	err error
}

func (m fakeModel) Init() tea.Cmd                       { return nil }
func (m fakeModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m fakeModel) View() string                        { return "" }
func (m fakeModel) Err() error                          { return m.err }

type fakeModelNoErr struct{}

func (m fakeModelNoErr) Init() tea.Cmd                       { return nil }
func (m fakeModelNoErr) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m fakeModelNoErr) View() string                        { return "" }

func TestTuiErrorRunErr(t *testing.T) {
	runErr := errors.New("program crashed")
	got := tuiError(fakeModel{}, runErr)
	if got != runErr {
		t.Fatalf("expected %v, got %v", runErr, got)
	}
}

func TestTuiErrorModelErr(t *testing.T) {
	modelErr := errors.New("clone failed")
	got := tuiError(fakeModel{err: modelErr}, nil)
	if got != modelErr {
		t.Fatalf("expected %v, got %v", modelErr, got)
	}
}

func TestTuiErrorBothNil(t *testing.T) {
	got := tuiError(fakeModel{}, nil)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestTuiErrorNoErrMethod(t *testing.T) {
	got := tuiError(fakeModelNoErr{}, nil)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestSplitVersion(t *testing.T) {
	cases := []struct {
		in, scheme, version string
	}{
		{"@owner/repo", "@owner/repo", ""},
		{"@owner/repo#v1.2.0", "@owner/repo", "v1.2.0"},
		{"gl:@org/repo#stable", "gl:@org/repo", "stable"},
		{"gl:rh:@sp-ai/lumino/lumino-mcp-server#release/1.0", "gl:rh:@sp-ai/lumino/lumino-mcp-server", "release/1.0"},
		{"https://github.com/user/repo.git#v2", "https://github.com/user/repo.git", "v2"},
	}
	for _, c := range cases {
		scheme, version, err := splitVersion(c.in)
		if err != nil || scheme != c.scheme || version != c.version {
			t.Errorf("splitVersion(%q) = %q, %q, %v; want %q, %q", c.in, scheme, version, err, c.scheme, c.version)
		}
	}
}

func TestSplitVersionInvalid(t *testing.T) {
	for _, in := range []string{"@owner/repo#", "@owner/repo#-x", "@owner/repo#a..b", "@owner/repo#a b"} {
		if _, _, err := splitVersion(in); err == nil {
			t.Errorf("splitVersion(%q): expected an error", in)
		}
	}
}

func TestPrepareInstallWithoutVersion(t *testing.T) {
	stubListRemoteRefs(t, func(string) ([]*plumbing.Reference, error) {
		t.Fatal("refs listed for an unpinned install")
		return nil, nil
	})
	url, err := prepareInstall("@owner/repo")
	if err != nil || url != "https://github.com/owner/repo.git" {
		t.Fatalf("got %q, %v", url, err)
	}
}

func TestResolvePinnedRef(t *testing.T) {
	hash := plumbing.ZeroHash
	refs := []*plumbing.Reference{
		plumbing.NewHashReference("refs/heads/main", hash),
		plumbing.NewHashReference("refs/heads/v1", hash),
		plumbing.NewHashReference("refs/tags/v1", hash),
		plumbing.NewHashReference("refs/heads/dev", hash),
	}
	if got, ok := resolvePinnedRef(refs, "v1"); !ok || got != "refs/tags/v1" {
		t.Errorf("v1: got %q, %v; want the tag", got, ok)
	}
	if got, ok := resolvePinnedRef(refs, "dev"); !ok || got != "refs/heads/dev" {
		t.Errorf("dev: got %q, %v; want the branch", got, ok)
	}
	if _, ok := resolvePinnedRef(refs, "v9"); ok {
		t.Error("v9: expected not found")
	}
}

func TestPrepareInstallUnknownVersion(t *testing.T) {
	stubListRemoteRefs(t, func(string) ([]*plumbing.Reference, error) {
		return []*plumbing.Reference{plumbing.NewHashReference("refs/heads/main", plumbing.ZeroHash)}, nil
	})
	_, err := prepareInstall("@owner/mcpm-test-no-such-repo#v9.9.9")
	if err == nil || !strings.Contains(err.Error(), `version "v9.9.9" not found`) {
		t.Fatalf("expected a version-not-found error, got %v", err)
	}
}

func TestPrepareInstallListError(t *testing.T) {
	stubListRemoteRefs(t, func(string) ([]*plumbing.Reference, error) {
		return nil, errors.New("network down")
	})
	_, err := prepareInstall("@owner/mcpm-test-no-such-repo#v1")
	if err == nil || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("expected the list error, got %v", err)
	}
}

func stubListRemoteRefs(t *testing.T, fn func(string) ([]*plumbing.Reference, error)) {
	orig := listRemoteRefs
	listRemoteRefs = fn
	t.Cleanup(func() { listRemoteRefs = orig })
}
