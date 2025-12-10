package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"mcpm/internal/fetcher"
	"mcpm/internal/tui"
)

var (
	updateAll    bool
	updateGlobal bool
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
  mcpm update server-filesystem --global`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if updateAll {
			servers, err := fetcher.ListServers()
			if err != nil {
				fmt.Printf("Error listing servers: %v\n", err)
				os.Exit(1)
			}

			if len(servers) == 0 {
				fmt.Println("No servers installed in .mcp/servers/")
				return
			}

			for _, name := range servers {
				fmt.Printf("Updating %s...\n", name)
				if err := updateServer(name, updateGlobal); err != nil {
					fmt.Printf("  Error: %v\n", err)
				} else {
					fmt.Printf("  Updated successfully\n")
				}
			}
			return
		}

		if len(args) == 0 {
			fmt.Println("Please specify a server name or use --all to update all servers")
			os.Exit(1)
		}

		name := args[0]
		if err := updateServer(name, updateGlobal); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	},
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
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("rebuild failed: %w", err)
	}

	return nil
}

func init() {
	updateCmd.Flags().BoolVarP(&updateAll, "all", "a", false, "Update all installed servers")
	updateCmd.Flags().BoolVarP(&updateGlobal, "global", "g", false, "Re-register globally after update")
	rootCmd.AddCommand(updateCmd)
}
