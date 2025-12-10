package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"mcpm/internal/fetcher"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed MCP servers",
	Long: `List all MCP servers installed in the current directory's .mcp/servers/ folder.

Examples:
  mcpm list`,
	Run: func(cmd *cobra.Command, args []string) {
		servers, err := fetcher.ListServers()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}

		if len(servers) == 0 {
			fmt.Println("No servers installed in .mcp/servers/")
			return
		}

		cwd, _ := os.Getwd()
		fmt.Println("Installed MCP servers:")
		for _, name := range servers {
			serverPath := filepath.Join(cwd, ".mcp", "servers", name)
			fmt.Printf("  • %s (%s)\n", name, serverPath)
		}
	},
}

func init() {
	rootCmd.AddCommand(listCmd)
}
