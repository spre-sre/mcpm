package fetcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

	// Derive folder name from URL (e.g., server-filesystem)
	parts := strings.Split(url, "/")
	repoName := strings.TrimSuffix(parts[len(parts)-1], ".git")
	// Add timestamp to avoid collisions or simple overwrites for now
	// Ideally we check if it exists and pull, but for safety lets use a unique-ish name
	// actually for a manager, we usually want one instance.
	targetPath := filepath.Join(baseDir, repoName)

	if _, err := os.Stat(targetPath); err == nil {
		// Directory exists, try to pull? For now, let's just error or return existing
		// Returning existing path assuming it's already there
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

	// Give the filesystem a moment to settle
	time.Sleep(500 * time.Millisecond)

	return targetPath, nil
}
