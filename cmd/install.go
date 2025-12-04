package cmd

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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

  # Install globally (available in all projects)
  mcpm install @modelcontextprotocol/server-filesystem --global

Schemes:
  @org/repo           GitHub (default)
  gl:@org/repo        GitLab.com
  gl:rh:@org/repo     GitLab Red Hat (gitlab.cee.redhat.com)
  https://...         Direct URL`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		repoRef := args[0]
		url, _ := parseScheme(repoRef)

		// Initialize and run the TUI with alt screen to avoid TTY issues
		p := tea.NewProgram(
			tui.NewInstallModel(url, repoRef, installGlobal),
			tea.WithAltScreen(),
		)
		if _, err := p.Run(); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	},
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

func init() {
	installCmd.Flags().BoolVarP(&installGlobal, "global", "g", false, "Install globally (available in all projects)")
	rootCmd.AddCommand(installCmd)
}
