package cmd

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/spf13/cobra"
	"mcpm/internal/fetcher"
	"mcpm/internal/injector"
)

var (
	importFile   string
	importGlobal bool
)

// cloneRepo and registerServer are fetcher.Clone and injector.Register;
// tests replace them to avoid the network and the claude binary.
var (
	cloneRepo      = fetcher.Clone
	registerServer = injector.Register
)

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "Install the MCP servers listed in mcpm.lock.json",
	Long: `Install every server in mcpm.lock.json (written by mcpm export): clone
its repository into .mcp/servers/, check out the recorded commit, build it
and register it in the recorded clients.

A server that is already installed from the same repository is checked out
at the recorded commit and rebuilt. Environment variables are not in the
lock file; servers that need them are listed after the import.

Examples:
  mcpm import
  mcpm import --file servers.lock.json
  mcpm import --global`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		lock, err := readLock(importFile)
		if err != nil {
			return err
		}
		return runImport(cmd.OutOrStdout(), lock, importGlobal)
	},
}

func readLock(path string) (lockFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return lockFile{}, fmt.Errorf("could not read %s: %w", path, err)
	}
	var lock lockFile
	if err := json.Unmarshal(data, &lock); err != nil {
		return lockFile{}, fmt.Errorf("could not parse %s: %w", path, err)
	}
	for _, e := range lock.Servers {
		if err := validateLockEntry(e); err != nil {
			return lockFile{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	return lock, nil
}

// validateLockEntry rejects an entry that import could not install as
// recorded, before anything is cloned.
func validateLockEntry(e lockEntry) error {
	if e.Repository == "" {
		return fmt.Errorf("server '%s' has no repository", e.Name)
	}
	if got := lockRepoName(e.Repository); got != e.Name {
		return fmt.Errorf("server '%s': repository %s installs as '%s'", e.Name, e.Repository, got)
	}
	if !isCommitHash(e.Commit) {
		return fmt.Errorf("server '%s': invalid commit %q", e.Name, e.Commit)
	}
	for _, t := range e.Targets {
		if t != injector.TargetClaudeCode && t != injector.TargetGeminiCLI {
			return fmt.Errorf("server '%s': unknown target %q", e.Name, t)
		}
	}
	return nil
}

// lockRepoName is the folder name fetcher.Clone uses for url.
func lockRepoName(url string) string {
	trimmed := strings.TrimRight(url, "/")
	return strings.TrimSuffix(trimmed[strings.LastIndex(trimmed, "/")+1:], ".git")
}

func isCommitHash(s string) bool {
	_, err := hex.DecodeString(s)
	return len(s) == 40 && err == nil
}

// runImport installs each server of lock in order and stops at the first
// failure. It writes one line per installed server to w.
func runImport(w io.Writer, lock lockFile, global bool) error {
	var needsEnv []string
	for _, e := range lock.Servers {
		if err := validateLockEntry(e); err != nil {
			return err
		}
		envNeeds, err := importServer(e, global)
		if err != nil {
			return fmt.Errorf("server '%s': %w", e.Name, err)
		}
		fmt.Fprintf(w, "Installed %s at %s\n", e.Name, e.Commit[:12])
		if len(envNeeds) > 0 {
			needsEnv = append(needsEnv, fmt.Sprintf("  %s: %s", e.Name, strings.Join(envNeeds, ", ")))
		}
	}
	if len(needsEnv) > 0 {
		fmt.Fprintln(w, "These servers need environment variables; set them in the client config:")
		fmt.Fprintln(w, strings.Join(needsEnv, "\n"))
	}
	return nil
}

// importServer clones, checks out, builds and registers one server. It
// returns the environment variables the server needs.
func importServer(e lockEntry, global bool) ([]string, error) {
	path, err := cloneRepo(e.Repository)
	if err != nil {
		return nil, err
	}
	if filepath.Base(path) != e.Name {
		return nil, fmt.Errorf("installed into %s, want .mcp/servers/%s", path, e.Name)
	}
	if err := checkoutCommit(path, e.Commit); err != nil {
		return nil, err
	}
	result, err := detectAndBuild(path)
	if err != nil {
		return nil, err
	}
	if result.Command == "" {
		return nil, errors.New("no project detected, nothing to run")
	}
	if len(e.Targets) > 0 {
		if err := registerServer(result, e.Targets, nil, global); err != nil {
			return nil, err
		}
	}
	return result.EnvNeeds, nil
}

// checkoutCommit checks out commit in the repository at dir, fetching it
// from origin first when the (shallow) clone does not have it.
func checkoutCommit(dir, commit string) error {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	hash := plumbing.NewHash(commit)
	if _, err := repo.CommitObject(hash); err != nil {
		if err := fetchCommit(repo, commit); err != nil {
			return err
		}
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{Hash: hash, Force: true}); err != nil {
		return fmt.Errorf("failed to check out %s: %w", commit, err)
	}
	return nil
}

func fetchCommit(repo *git.Repository, commit string) error {
	err := repo.Fetch(&git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec(commit + ":refs/mcpm/lock")},
		Depth:      1,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("failed to fetch commit %s: %w", commit, err)
	}
	return nil
}

func init() {
	importCmd.Flags().StringVarP(&importFile, "file", "f", lockFileName, "Lock file to read")
	importCmd.Flags().BoolVarP(&importGlobal, "global", "g", false, "Register the servers globally")
	rootCmd.AddCommand(importCmd)
}
