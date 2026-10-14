package cmd

import (
	"encoding/json"
	"fmt"
	"io"
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
		asJSON, _ := cmd.Flags().GetBool("json")
		if err := writeList(os.Stdout, asJSON); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	},
}

type listEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// writeList writes the installed servers to w, as a JSON array or as text.
func writeList(w io.Writer, asJSON bool) error {
	servers, err := fetcher.ListServers()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	entries := make([]listEntry, 0, len(servers))
	for _, name := range servers {
		entries = append(entries, listEntry{Name: name, Path: filepath.Join(cwd, ".mcp", "servers", name)})
	}

	if asJSON {
		return json.NewEncoder(w).Encode(entries)
	}

	if len(entries) == 0 {
		_, err = fmt.Fprintln(w, "No servers installed in .mcp/servers/")
		return err
	}
	if _, err = fmt.Fprintln(w, "Installed MCP servers:"); err != nil {
		return err
	}
	for _, e := range entries {
		if _, err = fmt.Fprintf(w, "  • %s (%s)\n", e.Name, e.Path); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	listCmd.Flags().Bool("json", false, "Print installed servers as a JSON array")
	rootCmd.AddCommand(listCmd)
}
