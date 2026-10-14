package fetcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
)

// Clone repo into local .mcp/servers directory
func Clone(url string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	// Create local .mcp/servers folder
	baseDir := filepath.Join(cwd, ".mcp", "servers")
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create .mcp directory: %w", err)
	}

	repoName, err := repoNameFromURL(url)
	if err != nil {
		return "", err
	}
	targetPath := filepath.Join(baseDir, repoName)

	if _, err := os.Stat(targetPath); err == nil {
		if err := checkExisting(targetPath, repoName, url); err != nil {
			return "", err
		}
		return targetPath, nil
	}

	_, err = git.PlainClone(targetPath, false, &git.CloneOptions{
		URL:      url,
		Progress: nil,
		Depth:    1,
	})

	if err != nil {
		return "", fmt.Errorf("git clone failed: %w", err)
	}

	return targetPath, nil
}

// checkExisting verifies that the repository at path was cloned from url.
func checkExisting(path, repoName, url string) error {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return fmt.Errorf("server '%s' exists but is not a git repository: %w", repoName, err)
	}
	remote, err := repo.Remote("origin")
	if err != nil {
		return fmt.Errorf("server '%s' has no origin remote: %w", repoName, err)
	}
	urls := remote.Config().URLs
	if len(urls) == 0 {
		return fmt.Errorf("server '%s' has no origin remote URL", repoName)
	}
	if normalizeURL(urls[0]) != normalizeURL(url) {
		return fmt.Errorf("server '%s' is already installed from %s", repoName, urls[0])
	}
	return nil
}

func normalizeURL(url string) string {
	url = strings.TrimSuffix(url, "/")
	return strings.TrimSuffix(url, ".git")
}

// Pull updates from remote for an existing repository
func Pull(repoPath string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}

	err = worktree.Pull(&git.PullOptions{
		RemoteName: "origin",
		Force:      true,
	})

	if err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("git pull failed: %w", err)
	}

	return nil
}

// GetServerPath returns the path to a server by name
func GetServerPath(name string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	serverPath := filepath.Join(cwd, ".mcp", "servers", name)
	if _, err := os.Stat(serverPath); os.IsNotExist(err) {
		return "", fmt.Errorf("server '%s' not found in .mcp/servers/", name)
	}

	return serverPath, nil
}

func repoNameFromURL(url string) (string, error) {
	trimmed := strings.TrimRight(url, "/")
	parts := strings.Split(trimmed, "/")
	name := strings.TrimSuffix(parts[len(parts)-1], ".git")
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("cannot derive repository name from URL %q", url)
	}
	return name, nil
}

// ListServers returns a list of installed server names
func ListServers() ([]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	serversDir := filepath.Join(cwd, ".mcp", "servers")
	entries, err := os.ReadDir(serversDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	var servers []string
	for _, entry := range entries {
		if entry.IsDir() {
			servers = append(servers, entry.Name())
		}
	}

	return servers, nil
}
