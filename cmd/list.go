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
  mcpm list
  mcpm list --names`,
	Run: func(cmd *cobra.Command, args []string) {
		asJSON, _ := cmd.Flags().GetBool("json")
		names, _ := cmd.Flags().GetBool("names")
		var err error
		if names {
			err = writeNames(os.Stdout)
		} else {
			err = writeList(os.Stdout, asJSON)
		}
		if err != nil {
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

// writeNames writes the installed server names to w, one per line.
func writeNames(w io.Writer) error {
	servers, err := fetcher.ListServers()
	if err != nil {
		return err
	}
	for _, name := range servers {
		if _, err = fmt.Fprintln(w, name); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	listCmd.Flags().Bool("json", false, "Print installed servers as a JSON array")
	listCmd.Flags().Bool("names", false, "Print only the installed server names, one per line")
	listCmd.MarkFlagsMutuallyExclusive("json", "names")
	rootCmd.AddCommand(listCmd)
}
