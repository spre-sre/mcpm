package cmd

import (
	"fmt"
	"os"

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
  mcpm update --all --check`,
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

	ok := true
	for _, name := range servers {
		fmt.Printf("%s %s...\n", verb, name)
		if err := update(name, updateGlobal); err != nil {
			fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
			ok = false
		} else if reportSuccess {
			fmt.Printf("  Updated successfully\n")
		}
	}
	return ok
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

	local, remote, err := fetchHeads(serverPath)
	if err != nil {
		return err
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
	fmt.Printf("  Would pull latest changes into %s\n", serverPath)
	fmt.Printf("  Would rebuild %s and re-register it (%s)\n", name, scope)
	return nil
}

func updateServer(name string, global bool) error {
	// Get server path
	serverPath, err := fetcher.GetServerPath(name)
	if err != nil {
		return err
	}

	// Pull latest changes
	fmt.Printf("  Pulling latest changes...\n")
	if err := fetcher.Pull(serverPath); err != nil {
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
