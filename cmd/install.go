package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/spf13/cobra"
	"mcpm/internal/tui"
)

var installGlobal bool

var installCmd = &cobra.Command{
	Use:   "install [scheme]",
	Short: "Install an MCP server from a repository",
	Long: `Install an MCP server using the @org/repo syntax.

Examples:
  mcpm install @modelcontextprotocol/server-filesystem
  mcpm install gl:@gitlab-org/my-server
  mcpm install gl:rh:@sp-ai/lumino/lumino-mcp-server
  mcpm install https://github.com/user/repo.git

  # Pin a release tag or branch (works with every scheme)
  mcpm install @owner/repo#v1.2.0
  mcpm install gl:@gitlab-org/my-server#stable

  # Install globally (available in all projects)
  mcpm install @modelcontextprotocol/server-filesystem --global

Schemes:
  @org/repo           GitHub (default)
  gl:@org/repo        GitLab.com
  gl:rh:@org/repo     GitLab Red Hat (gitlab.cee.redhat.com)
  https://...         Direct URL
  <scheme>#<version>  Pin a tag or branch instead of the default branch`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		repoRef := args[0]
		url, err := prepareInstall(repoRef)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Initialize and run the TUI with alt screen to avoid TTY issues
		p := tea.NewProgram(
			tui.NewInstallModel(url, repoRef, installGlobal),
			tea.WithAltScreen(),
		)
		final, runErr := p.Run()
		if err := tuiError(final, runErr); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	},
}

type tuiErrModel interface {
	Err() error
}

func tuiError(final tea.Model, runErr error) error {
	if runErr != nil {
		return runErr
	}
	if m, ok := final.(tuiErrModel); ok && m.Err() != nil {
		return m.Err()
	}
	return nil
}

func parseScheme(input string) (string, string) {
	// GitLab Red Hat (gitlab.cee.redhat.com)
	if strings.HasPrefix(input, "gl:rh:@") {
		return "https://gitlab.cee.redhat.com/" + input[7:] + ".git", "GitLab Red Hat"
	}
	// GitLab.com
	if strings.HasPrefix(input, "gl:@") {
		return "https://gitlab.com/" + input[4:] + ".git", "GitLab"
	}
	// GitHub shorthand
	if strings.HasPrefix(input, "@") {
		return "https://github.com/" + input[1:] + ".git", "GitHub"
	}
	// Direct URL
	if strings.HasPrefix(input, "http") {
		return input, "Custom URL"
	}
	return input, "Unknown"
}

// prepareInstall returns the clone URL for repoRef. When repoRef pins a
// version ("@owner/repo#v1.2.0") it clones that version into
// .mcp/servers/<name> first; fetcher.Clone then reuses that checkout.
func prepareInstall(repoRef string) (string, error) {
	scheme, version, err := splitVersion(repoRef)
	if err != nil {
		return "", err
	}
	url, _ := parseScheme(scheme)
	if version == "" {
		return url, nil
	}
	if err := clonePinned(url, version); err != nil {
		return "", err
	}
	return url, nil
}

// splitVersion splits "@owner/repo#v1.2.0" into "@owner/repo" and "v1.2.0".
// Without a '#' the version is empty.
func splitVersion(input string) (string, string, error) {
	i := strings.LastIndex(input, "#")
	if i < 0 {
		return input, "", nil
	}
	version := input[i+1:]
	if err := validateVersion(version); err != nil {
		return "", "", err
	}
	return input[:i], version, nil
}

func validateVersion(version string) error {
	if version == "" {
		return fmt.Errorf("empty version after '#': use <scheme>#<tag-or-branch>")
	}
	if strings.HasPrefix(version, "-") || strings.Contains(version, "..") ||
		strings.ContainsAny(version, " \t\r\n~^:?*[\\") {
		return fmt.Errorf("invalid version %q: not a valid tag or branch name", version)
	}
	return nil
}

// resolvePinnedRef returns the reference in refs that names version: a tag
// (release) first, then a branch.
func resolvePinnedRef(refs []*plumbing.Reference, version string) (plumbing.ReferenceName, bool) {
	names := make(map[plumbing.ReferenceName]bool, len(refs))
	for _, r := range refs {
		names[r.Name()] = true
	}
	for _, name := range []plumbing.ReferenceName{
		plumbing.NewTagReferenceName(version),
		plumbing.NewBranchReferenceName(version),
	} {
		if names[name] {
			return name, true
		}
	}
	return "", false
}

// listRemoteRefs lists the references of the repository at url; replaced in tests.
var listRemoteRefs = func(url string) ([]*plumbing.Reference, error) {
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{url}})
	return remote.List(&git.ListOptions{})
}

// clonePinned clones version of url into .mcp/servers/<name>. A tag is
// checked out detached and a branch is cloned as the only branch, so a later
// pull stays on the pinned version instead of the default branch.
func clonePinned(url, version string) error {
	target, err := pinnedTarget(url, version)
	if err != nil {
		return err
	}
	refs, err := listRemoteRefs(url)
	if err != nil {
		return fmt.Errorf("cannot list versions of %s: %w", url, err)
	}
	ref, ok := resolvePinnedRef(refs, version)
	if !ok {
		return fmt.Errorf("version %q not found in %s: no tag or branch with that name", version, url)
	}
	_, err = git.PlainClone(target, false, &git.CloneOptions{
		URL:           url,
		ReferenceName: ref,
		SingleBranch:  true,
		Depth:         1,
	})
	if err != nil {
		os.RemoveAll(target)
		return fmt.Errorf("git clone of %s at %s failed: %w", url, version, err)
	}
	return nil
}

// pinnedTarget returns the not yet existing server directory for url.
func pinnedTarget(url, version string) (string, error) {
	name := lockRepoName(url)
	if name == "" || name == "." || name == ".." || strings.Contains(name, "\\") {
		return "", fmt.Errorf("cannot derive repository name from URL %q", url)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	target := filepath.Join(cwd, ".mcp", "servers", name)
	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("server '%s' is already installed; remove it before installing version %s", name, version)
	}
	return target, nil
}

func init() {
	installCmd.Flags().BoolVarP(&installGlobal, "global", "g", false, "Install globally (available in all projects)")
	rootCmd.AddCommand(installCmd)
}
