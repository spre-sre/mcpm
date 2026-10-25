package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	"github.com/spf13/cobra"
	"mcpm/internal/fetcher"
	"mcpm/internal/injector"
)

// lockFileName is the default file that export writes and import reads.
const lockFileName = "mcpm.lock.json"

var (
	exportFile   string
	exportGlobal bool
)

// lockFile is the content of mcpm.lock.json.
type lockFile struct {
	Servers []lockEntry `json:"servers"`
}

// lockEntry records one installed server: where it came from, the commit
// it is at and the clients it is registered in.
type lockEntry struct {
	Name       string                `json:"name"`
	Repository string                `json:"repository"`
	Commit     string                `json:"commit"`
	Targets    []injector.TargetTool `json:"targets"`
}

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Write the installed MCP servers to mcpm.lock.json",
	Long: `Write every server in .mcp/servers/ to mcpm.lock.json: its name, the
repository URL it was cloned from, the commit it is at, and the clients
(Claude Code, Gemini CLI) it is registered in. Use mcpm import to install
the same set elsewhere.

Environment variables are not written, so the file holds no secrets.

Examples:
  mcpm export
  mcpm export --file servers.lock.json
  mcpm export --global`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		lock, err := buildLock(exportGlobal)
		if err != nil {
			return err
		}
		if err := writeLock(exportFile, lock); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Exported %d server(s) to %s\n", len(lock.Servers), exportFile)
		return nil
	},
}

// buildLock describes every installed server. A server that is not a git
// repository with an origin remote cannot be reinstalled, so it is an error.
func buildLock(global bool) (lockFile, error) {
	servers, err := fetcher.ListServers()
	if err != nil {
		return lockFile{}, err
	}
	lock := lockFile{Servers: make([]lockEntry, 0, len(servers))}
	for _, name := range servers {
		entry, err := lockEntryFor(name, global)
		if err != nil {
			return lockFile{}, err
		}
		lock.Servers = append(lock.Servers, entry)
	}
	return lock, nil
}

func lockEntryFor(name string, global bool) (lockEntry, error) {
	path, err := fetcher.GetServerPath(name)
	if err != nil {
		return lockEntry{}, err
	}
	url, commit, err := repoOrigin(path)
	if err != nil {
		return lockEntry{}, fmt.Errorf("server '%s': %w", name, err)
	}
	targets, err := registeredTargets(name, global)
	if err != nil {
		return lockEntry{}, fmt.Errorf("server '%s': %w", name, err)
	}
	return lockEntry{Name: name, Repository: url, Commit: commit, Targets: targets}, nil
}

// repoOrigin returns the origin URL and the HEAD commit of the repository at dir.
func repoOrigin(dir string) (string, string, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return "", "", fmt.Errorf("not a git repository: %w", err)
	}
	remote, err := repo.Remote("origin")
	if err != nil {
		return "", "", fmt.Errorf("no origin remote: %w", err)
	}
	urls := remote.Config().URLs
	if len(urls) == 0 {
		return "", "", errors.New("no origin remote URL")
	}
	head, err := repo.Head()
	if err != nil {
		return "", "", fmt.Errorf("no HEAD commit: %w", err)
	}
	return urls[0], head.Hash().String(), nil
}

// registeredTargets returns the clients that have a registration of name,
// in the scope that install writes to.
func registeredTargets(name string, global bool) ([]injector.TargetTool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	targets := []injector.TargetTool{}
	inClaude, err := inClaudeConfig(cwd, name, global)
	if err != nil {
		return nil, err
	}
	if inClaude {
		targets = append(targets, injector.TargetClaudeCode)
	}
	inGemini, err := inGeminiSettings(cwd, name, global)
	if err != nil {
		return nil, err
	}
	if inGemini {
		targets = append(targets, injector.TargetGeminiCLI)
	}
	return targets, nil
}

// inClaudeConfig reports whether .claude.json ($CLAUDE_CONFIG_DIR or the
// home directory) registers name. A missing file registers nothing.
func inClaudeConfig(cwd, name string, global bool) (bool, error) {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false, fmt.Errorf("could not get home directory: %w", err)
		}
		dir = home
	}
	path := filepath.Join(dir, ".claude.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("could not read %s: %w", path, err)
	}
	var cfg claudeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false, fmt.Errorf("could not parse %s: %w", path, err)
	}
	servers := cfg.McpServers
	if !global {
		servers = cfg.Projects[cwd].McpServers
	}
	_, ok := servers[name]
	return ok, nil
}

// inGeminiSettings reports whether the Gemini CLI settings (project or
// global) register name. A missing file registers nothing.
func inGeminiSettings(cwd, name string, global bool) (bool, error) {
	base := cwd
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return false, fmt.Errorf("could not get home directory: %w", err)
		}
		base = home
	}
	path := filepath.Join(base, ".gemini", "settings.json")
	cfg, err := loadGeminiSettings(path)
	if err != nil {
		return false, err
	}
	existing, exists := cfg["mcpServers"]
	if !exists {
		return false, nil
	}
	servers, ok := existing.(map[string]interface{})
	if !ok {
		return false, fmt.Errorf("%s: mcpServers is not a JSON object", path)
	}
	_, ok = servers[name]
	return ok, nil
}

func writeLock(path string, lock lockFile) error {
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func init() {
	exportCmd.Flags().StringVarP(&exportFile, "file", "f", lockFileName, "Lock file to write")
	exportCmd.Flags().BoolVarP(&exportGlobal, "global", "g", false, "Record the global registrations")
	rootCmd.AddCommand(exportCmd)
}
