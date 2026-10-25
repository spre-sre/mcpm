package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"mcpm/internal/builder"
	"mcpm/internal/fetcher"
	"mcpm/internal/injector"
)

var (
	doctorClaudeCode bool
	doctorGeminiCLI  bool
	doctorGlobal     bool
)

// detectAndBuild is builder.DetectAndBuild; tests replace it to avoid real builds.
var detectAndBuild = builder.DetectAndBuild

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check installed MCP servers for problems",
	Long: `Check every server in .mcp/servers/: its folder exists, it still builds,
and its registration in Claude Code and Gemini CLI runs the built command.

Prints one line per problem and exits with status 1 if it finds any.

Examples:
  mcpm doctor
  mcpm doctor --claude
  mcpm doctor --global`,
	Args: cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		problems, err := runDoctor(os.Stdout, doctorTools(doctorClaudeCode, doctorGeminiCLI), doctorGlobal)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if problems > 0 {
			os.Exit(1)
		}
	},
}

// doctorTools returns the clients to check; both when neither is selected.
func doctorTools(claude, gemini bool) []injector.TargetTool {
	if !claude && !gemini {
		claude, gemini = true, true
	}
	var tools []injector.TargetTool
	if claude {
		tools = append(tools, injector.TargetClaudeCode)
	}
	if gemini {
		tools = append(tools, injector.TargetGeminiCLI)
	}
	return tools
}

// runDoctor checks every installed server, writes one line per problem to w
// and returns the number of problems.
func runDoctor(w io.Writer, tools []injector.TargetTool, global bool) (int, error) {
	servers, err := fetcher.ListServers()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, name := range servers {
		for _, problem := range checkServer(name, tools, global) {
			if _, err := fmt.Fprintln(w, problem); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

// checkServer returns the problems of one installed server. Registration is
// checked only when the server builds to a command.
func checkServer(name string, tools []injector.TargetTool, global bool) []string {
	path, err := fetcher.GetServerPath(name)
	if err != nil {
		return []string{fmt.Sprintf("%s: folder missing: %v", name, err)}
	}
	result, err := detectAndBuild(path)
	if err != nil {
		return []string{fmt.Sprintf("%s: build failed: %v", name, err)}
	}
	var problems []string
	for _, buildErr := range result.BuildErrors {
		problems = append(problems, fmt.Sprintf("%s: build failed: %v", name, buildErr))
	}
	if result.Command == "" {
		return append(problems, fmt.Sprintf("%s: no project detected, nothing to run", name))
	}
	return append(problems, injector.Check(name, result, tools, global)...)
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorClaudeCode, "claude", false, "Check only Claude Code")
	doctorCmd.Flags().BoolVar(&doctorGeminiCLI, "gemini", false, "Check only Gemini CLI")
	doctorCmd.Flags().BoolVarP(&doctorGlobal, "global", "g", false, "Check the global registrations")
	rootCmd.AddCommand(doctorCmd)
}
