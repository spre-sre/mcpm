package cmd

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

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
and register it in the recorded clients. A server exported with a pinned
version stays pinned to it, so mcpm update keeps it there.

A server that is already installed from the same repository is checked out
at the recorded commit and rebuilt. The lock file holds the names of the
environment variables but not their values; the variables each server still
needs are listed after the import (set them with mcpm env set).

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
	if !validPin(e.Pin) {
		return fmt.Errorf("server '%s': invalid pin %q", e.Name, e.Pin)
	}
	return validateLockLists(e)
}

// validateLockLists rejects an entry with an unknown target or an invalid
// environment variable name.
func validateLockLists(e lockEntry) error {
	for _, t := range e.Targets {
		if t != injector.TargetClaudeCode && t != injector.TargetGeminiCLI {
			return fmt.Errorf("server '%s': unknown target %q", e.Name, t)
		}
	}
	for _, key := range e.Env {
		if !envKeyRe.MatchString(key) {
			return fmt.Errorf("server '%s': invalid environment variable name %q", e.Name, key)
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

// validPin reports whether pin is empty or a tag or branch reference that
// git accepts. Import writes a pinned branch as a ref file, so a name such as
// "refs/heads/../../x" must not pass.
func validPin(pin string) bool {
	if pin == "" {
		return true
	}
	ref := plumbing.ReferenceName(pin)
	if !ref.IsTag() && !ref.IsBranch() {
		return false
	}
	return validRefPath(strings.TrimPrefix(strings.TrimPrefix(pin, "refs/tags/"), "refs/heads/"))
}

// validRefPath applies git check-ref-format rules to the short name of a
// reference.
func validRefPath(short string) bool {
	for _, part := range strings.Split(short, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	if strings.Contains(short, "..") || strings.Contains(short, "@{") || strings.HasSuffix(short, ".") {
		return false
	}
	return !strings.ContainsAny(short, " ~^:?*[\\") && strings.IndexFunc(short, unicode.IsControl) < 0
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
		fmt.Fprintf(w, "Installed %s at %s%s\n", e.Name, e.Commit[:12], pinSuffix(e.Pin))
		if missing := unsetEnv(e, envNeeds, global); len(missing) > 0 {
			needsEnv = append(needsEnv, fmt.Sprintf("  %s: %s", e.Name, strings.Join(missing, ", ")))
		}
	}
	if len(needsEnv) > 0 {
		fmt.Fprintln(w, "These servers need environment variables; set them with mcpm env set <name> KEY=VALUE:")
		fmt.Fprintln(w, strings.Join(needsEnv, "\n"))
	}
	return nil
}

// unsetEnv returns the sorted variables, recorded in e or required by the
// built server, that a registration of e in its targets does not set yet.
// When the registrations cannot be read, every variable is returned.
func unsetEnv(e lockEntry, envNeeds []string, global bool) []string {
	seen := make(map[string]bool)
	for _, key := range append(slices.Clone(e.Env), envNeeds...) {
		seen[key] = true
	}
	needed := sortedEnvKeys(seen)
	cwd, err := os.Getwd()
	if err != nil || len(e.Targets) == 0 {
		return needed
	}
	defs, err := scopedEnvDefs(cwd, e.Name, global)
	if err != nil {
		return needed
	}
	var missing []string
	for _, key := range needed {
		if !setInAll(defs, e.Targets, key) {
			missing = append(missing, key)
		}
	}
	return missing
}

// setInAll reports whether the registration of every client in targets sets
// key.
func setInAll(defs map[injector.TargetTool]envServerDef, targets []injector.TargetTool, key string) bool {
	for _, t := range targets {
		if _, ok := defs[t].Env[key]; !ok {
			return false
		}
	}
	return true
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
	if err := keepPin(path, e); err != nil {
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

// keepPin records the pin of e in the repository at dir, as install does, so
// update keeps the server on it. A pinned branch is checked out at e.Commit
// so update can pull it. An entry without a pin clears an earlier pin of a
// server that was already installed.
func keepPin(dir string, e lockEntry) error {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	pin := plumbing.ReferenceName(e.Pin)
	if pin == "" {
		return clearPin(repo)
	}
	if pin.IsBranch() {
		if err := checkoutBranchAt(repo, pin, e.Commit); err != nil {
			return err
		}
	}
	if err := recordPin(repo, pin); err != nil {
		return fmt.Errorf("failed to record pin %s: %w", pin.Short(), err)
	}
	return nil
}

func clearPin(repo *git.Repository) error {
	cfg, err := repo.Config()
	if err != nil {
		return err
	}
	if cfg.Raw.Section(pinSection).Option(pinOption) == "" {
		return nil
	}
	cfg.Raw.Section(pinSection).RemoveOption(pinOption)
	return repo.SetConfig(cfg)
}

// checkoutBranchAt points branch at commit and checks it out.
func checkoutBranchAt(repo *git.Repository, branch plumbing.ReferenceName, commit string) error {
	ref := plumbing.NewHashReference(branch, plumbing.NewHash(commit))
	if err := repo.Storer.SetReference(ref); err != nil {
		return fmt.Errorf("failed to set branch %s: %w", branch.Short(), err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{Branch: branch, Force: true}); err != nil {
		return fmt.Errorf("failed to check out branch %s: %w", branch.Short(), err)
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
