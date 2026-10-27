package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/spf13/cobra"
	"mcpm/internal/builder"
	"mcpm/internal/fetcher"
	"mcpm/internal/injector"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed MCP servers",
	Long: `List all MCP servers installed in the current directory's .mcp/servers/ folder.
A server switched off with mcpm disable is marked with the clients it is off in.

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
	Name       string `json:"name"`
	Path       string `json:"path"`
	BuildType  string `json:"buildType"`
	RunCommand string `json:"runCommand"`
	Commit     string `json:"commit"`
	// Off names the clients and scopes the server is switched off in, e.g.
	// "Claude Code (local), Gemini CLI (global)".
	Off string `json:"off,omitempty"`
}

// markOff fills in Off for each entry switched off locally or globally.
func markOff(cwd string, entries []listEntry) error {
	for _, global := range bothScopes {
		off, _, err := loadOff(cwd, global)
		if err != nil {
			return err
		}
		for i := range entries {
			entries[i].Off = appendOff(entries[i].Off, off, entries[i].Name, global)
		}
	}
	return nil
}

// appendOff adds to labels each client name is switched off in, in off.
func appendOff(labels string, off offServers, name string, global bool) string {
	for _, tool := range []injector.TargetTool{injector.TargetClaudeCode, injector.TargetGeminiCLI} {
		if !off.has(name, tool) {
			continue
		}
		if labels != "" {
			labels += ", "
		}
		labels += scopedLabel(tool, scopeName(global))
	}
	return labels
}

// describeServer fills in the build type, run command and git commit of the
// server at e.Path. It reads files only: nothing is built or run. A value
// that cannot be determined stays empty.
func describeServer(e *listEntry) {
	e.BuildType = detectBuildType(e.Path)
	e.RunCommand = runCommandFor(e.BuildType, e.Path)
	e.Commit = headCommit(e.Path)
}

// detectBuildType names the project kind in the order builder.DetectAndBuild checks it.
func detectBuildType(dir string) string {
	switch {
	case fileExists(filepath.Join(dir, "mcp.json")):
		return "custom"
	case fileExists(filepath.Join(dir, "package.json")):
		return "node"
	case fileExists(filepath.Join(dir, "pyproject.toml")), fileExists(filepath.Join(dir, "requirements.txt")):
		return "python"
	case fileExists(filepath.Join(dir, "go.mod")):
		return "go"
	}
	return ""
}

func runCommandFor(buildType, dir string) string {
	switch buildType {
	case "custom":
		return manifestRunCommand(dir)
	case "node":
		return "node " + nodeEntry(dir)
	case "python":
		return pythonRunCommand(dir)
	case "go":
		return filepath.Join(dir, goBinName())
	}
	return ""
}

// manifestRunCommand returns runCmd and args from mcp.json as written.
func manifestRunCommand(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
	if err != nil {
		return ""
	}
	var m builder.Manifest
	if json.Unmarshal(data, &m) != nil || strings.TrimSpace(m.RunCmd) == "" {
		return ""
	}
	return strings.Join(append([]string{m.RunCmd}, m.Args...), " ")
}

// nodeEntry mirrors the builder's standard entry detection (not its monorepo search).
func nodeEntry(dir string) string {
	for _, rel := range []string{filepath.Join("dist", "index.js"), filepath.Join("build", "index.js")} {
		if fileExists(filepath.Join(dir, rel)) {
			return filepath.Join(dir, rel)
		}
	}
	var pkg struct {
		Main string `json:"main"`
	}
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil && json.Unmarshal(data, &pkg) == nil && pkg.Main != "" {
		return filepath.Join(dir, pkg.Main)
	}
	return filepath.Join(dir, "index.js")
}

func pythonRunCommand(dir string) string {
	python := filepath.Join(dir, ".venv", "bin", "python")
	if runtime.GOOS == "windows" {
		python = filepath.Join(dir, ".venv", "Scripts", "python.exe")
	}
	for _, c := range []string{"main.py", "server.py", "app.py", "src/main.py", "src/server.py"} {
		if fileExists(filepath.Join(dir, c)) {
			return python + " " + filepath.Join(dir, c)
		}
	}
	return ""
}

func goBinName() string {
	if runtime.GOOS == "windows" {
		return "mcp-server.exe"
	}
	return "mcp-server"
}

// headCommit returns the commit hash HEAD points to, or "" if dir is not a git repository.
func headCommit(dir string) string {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return ""
	}
	head, err := repo.Head()
	if err != nil {
		return ""
	}
	return head.Hash().String()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writeList writes the installed servers to w, as a JSON array or as text.
func writeList(w io.Writer, asJSON bool) error {
	entries, err := listEntries()
	if err != nil {
		return err
	}
	if asJSON {
		for i := range entries {
			describeServer(&entries[i])
		}
		return json.NewEncoder(w).Encode(entries)
	}
	return writeListText(w, entries)
}

// listEntries returns one entry per installed server, with Off filled in.
func listEntries() ([]listEntry, error) {
	servers, err := fetcher.ListServers()
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	entries := make([]listEntry, 0, len(servers))
	for _, name := range servers {
		entries = append(entries, listEntry{Name: name, Path: filepath.Join(cwd, ".mcp", "servers", name)})
	}
	if err := markOff(cwd, entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// writeListText writes entries to w as human-readable text.
func writeListText(w io.Writer, entries []listEntry) error {
	if len(entries) == 0 {
		_, err := fmt.Fprintln(w, "No servers installed in .mcp/servers/")
		return err
	}
	if _, err := fmt.Fprintln(w, "Installed MCP servers:"); err != nil {
		return err
	}
	for _, e := range entries {
		if _, err := fmt.Fprintf(w, "  • %s (%s)%s\n", e.Name, e.Path, offSuffix(e.Off)); err != nil {
			return err
		}
	}
	return nil
}

func offSuffix(off string) string {
	if off == "" {
		return ""
	}
	return " [off: " + off + "]"
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
