package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/spf13/cobra"
	"mcpm/internal/fetcher"
	"mcpm/internal/tui"
)

var (
	updateAll    bool
	updateGlobal bool
	updateDryRun bool
	updateCheck  bool
)

var updateCmd = &cobra.Command{
	Use:   "update [name]",
	Short: "Update an installed MCP server from its remote repository",
	Long: `Pull the latest changes from the remote repository and rebuild the MCP server.

Examples:
  # Update a specific server
  mcpm update server-filesystem

  # Update all installed servers
  mcpm update --all

  # Update and re-register globally
  mcpm update server-filesystem --global

  # Show what would be updated without changing anything
  mcpm update --all --dry-run

  # Fetch every remote and report which servers have new commits
  mcpm update --all --check

A server installed at a pinned version (mcpm install @owner/repo#v1.2.0)
stays pinned: a pinned tag is kept as is and a pinned branch only pulls
new commits of that branch.

update --all ends with a summary of the servers it updated, those already
current, those kept on their pin and those that failed, with the reason.
It exits with status 1 when any server failed.`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeServerNames,
	Run: func(cmd *cobra.Command, args []string) {
		update, _ := updateAction()

		if updateAll {
			if !updateAllServers(update) {
				os.Exit(1)
			}
			return
		}

		if len(args) == 0 {
			fmt.Println("Please specify a server name or use --all to update all servers")
			os.Exit(1)
		}

		name := args[0]
		if err := update(name, updateGlobal); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	},
}

// updateAllServers runs update for every installed server and reports
// whether all of them succeeded.
func updateAllServers(update func(name string, global bool) error) bool {
	servers, err := fetcher.ListServers()
	if err != nil {
		fmt.Printf("Error listing servers: %v\n", err)
		return false
	}

	if len(servers) == 0 {
		fmt.Println("No servers installed in .mcp/servers/")
		return true
	}

	_, verb := updateAction()
	reportSuccess := !updateDryRun && !updateCheck

	var summary updateSummary
	for _, name := range servers {
		fmt.Printf("%s %s...\n", verb, name)
		before := readServerState(name)
		err := update(name, updateGlobal)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		} else if reportSuccess {
			fmt.Printf("  Updated successfully\n")
		}
		summary.record(name, before, err)
	}
	if reportSuccess {
		summary.write(os.Stdout)
	}
	return len(summary.failed) == 0
}

// serverState is what update --all reads of a server before updating it.
type serverState struct {
	path string
	head string
	pin  plumbing.ReferenceName
}

// readServerState returns the path, HEAD commit and pin of name. A value that
// cannot be read stays empty; update reports the error.
func readServerState(name string) serverState {
	path, err := fetcher.GetServerPath(name)
	if err != nil {
		return serverState{}
	}
	pin, _ := serverPin(path)
	return serverState{path: path, head: headCommit(path), pin: pin}
}

// updateSummary sorts the servers of update --all by outcome.
type updateSummary struct {
	updated, current, pinned, failed []string
}

// record files name under its outcome: failed with err, kept on a pinned tag,
// already current when HEAD did not move, or updated.
func (s *updateSummary) record(name string, before serverState, err error) {
	switch {
	case err != nil:
		s.failed = append(s.failed, fmt.Sprintf("%s (%v)", name, err))
	case before.pin.IsTag():
		s.pinned = append(s.pinned, fmt.Sprintf("%s (%s)", name, before.pin.Short()))
	case headCommit(before.path) == before.head:
		s.current = append(s.current, name)
	default:
		s.updated = append(s.updated, name)
	}
}

// write prints one line per outcome that has servers. Each failure gets its
// own line, so its reason stays readable.
func (s *updateSummary) write(w io.Writer) {
	fmt.Fprintln(w, "Summary:")
	writeSummaryLine(w, "Updated", s.updated)
	writeSummaryLine(w, "Already current", s.current)
	writeSummaryLine(w, "Kept on pin", s.pinned)
	for _, f := range s.failed {
		fmt.Fprintf(w, "  Failed: %s\n", f)
	}
}

func writeSummaryLine(w io.Writer, label string, names []string) {
	if len(names) > 0 {
		fmt.Fprintf(w, "  %s: %s\n", label, strings.Join(names, ", "))
	}
}

// updateAction returns the per-server function and progress verb selected
// by the --check and --dry-run flags.
func updateAction() (func(name string, global bool) error, string) {
	switch {
	case updateCheck:
		return checkServerRemote, "Checking"
	case updateDryRun:
		return dryRunUpdate, "Would update"
	}
	return updateServer, "Updating"
}

// checkServerRemote fetches the server's origin remote and reports whether
// the remote branch has commits that HEAD does not. It changes no branch or
// working tree.
func checkServerRemote(name string, _ bool) error {
	serverPath, err := fetcher.GetServerPath(name)
	if err != nil {
		return err
	}

	pin, err := serverPin(serverPath)
	if err != nil {
		return err
	}
	if pin.IsTag() {
		fmt.Printf("  Pinned to %s (%.7s); not updated\n", pin.Short(), headCommit(serverPath))
		return nil
	}

	local, remote, err := fetchHeads(serverPath)
	if err != nil {
		return err
	}

	if pin != "" {
		fmt.Printf("  Pinned to branch %s\n", pin.Short())
	}
	if local == remote {
		fmt.Printf("  Up to date (%s)\n", local.String()[:7])
		return nil
	}
	fmt.Printf("  New commits available (%s -> %s)\n", local.String()[:7], remote.String()[:7])
	return nil
}

// fetchHeads fetches origin for the repository at path and returns the hash
// of HEAD and of the matching origin branch.
func fetchHeads(path string) (plumbing.Hash, plumbing.Hash, error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("failed to open repository: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("failed to read HEAD: %w", err)
	}
	if !head.Name().IsBranch() {
		return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("HEAD is detached at %s", head.Hash().String()[:7])
	}

	err = repo.Fetch(&git.FetchOptions{RemoteName: "origin"})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("failed to fetch: %w", err)
	}

	remoteName := plumbing.NewRemoteReferenceName("origin", head.Name().Short())
	remote, err := repo.Reference(remoteName, true)
	if err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("failed to read %s: %w", remoteName.Short(), err)
	}
	return head.Hash(), remote.Hash(), nil
}

// dryRunUpdate prints what updateServer would do and changes nothing.
func dryRunUpdate(name string, global bool) error {
	serverPath, err := fetcher.GetServerPath(name)
	if err != nil {
		return err
	}

	scope := "local"
	if global {
		scope = "global"
	}
	pin, _ := serverPin(serverPath)
	switch {
	case pin.IsTag():
		fmt.Printf("  Would keep pinned version %s in %s\n", pin.Short(), serverPath)
	case pin != "":
		fmt.Printf("  Would pull latest changes of pinned branch %s into %s\n", pin.Short(), serverPath)
	default:
		fmt.Printf("  Would pull latest changes into %s\n", serverPath)
	}
	fmt.Printf("  Would rebuild %s and re-register it (%s)\n", name, scope)
	return nil
}

// serverPin opens the server repository at path and returns the reference it
// was pinned to at install, or "" when it is not pinned.
func serverPin(path string) (plumbing.ReferenceName, error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}
	return readPin(repo)
}

// pullServer updates the checkout at path. An unpinned server pulls its
// default branch, a server pinned to a branch pulls only that branch and a
// server pinned to a tag stays where it is.
func pullServer(path string, pin plumbing.ReferenceName) error {
	switch {
	case pin == "":
		fmt.Printf("  Pulling latest changes...\n")
		return fetcher.Pull(path)
	case pin.IsTag():
		fmt.Printf("  Pinned to %s; keeping it\n", pin.Short())
		return nil
	}
	fmt.Printf("  Pulling latest changes of pinned branch %s...\n", pin.Short())
	return pullBranch(path, pin)
}

// pullBranch pulls branch from origin into the repository at path.
func pullBranch(path string, branch plumbing.ReferenceName) error {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}
	err = worktree.Pull(&git.PullOptions{
		RemoteName:    "origin",
		ReferenceName: branch,
		SingleBranch:  true,
		Force:         true,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("git pull failed: %w", err)
	}
	return nil
}

func updateServer(name string, global bool) error {
	// Get server path
	serverPath, err := fetcher.GetServerPath(name)
	if err != nil {
		return err
	}

	// Pull latest changes, staying on a pinned version
	pin, err := serverPin(serverPath)
	if err == nil {
		err = pullServer(serverPath, pin)
	}
	if err != nil {
		return fmt.Errorf("failed to pull: %w", err)
	}

	// Rebuild using TUI
	fmt.Printf("  Rebuilding...\n")
	p := tea.NewProgram(
		tui.NewUpdateModel(serverPath, name, global),
		tea.WithAltScreen(),
	)
	final, runErr := p.Run()
	if err := tuiError(final, runErr); err != nil {
		return fmt.Errorf("rebuild failed: %w", err)
	}

	return nil
}

func init() {
	updateCmd.Flags().BoolVarP(&updateAll, "all", "a", false, "Update all installed servers")
	updateCmd.Flags().BoolVarP(&updateGlobal, "global", "g", false, "Re-register globally after update")
	updateCmd.Flags().BoolVar(&updateDryRun, "dry-run", false, "Print what would be updated and change nothing")
	updateCmd.Flags().BoolVar(&updateCheck, "check", false, "Fetch each remote and report servers with new commits, without pulling")
	rootCmd.AddCommand(updateCmd)
}
