package fetcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RemoveServer deletes <cwd>/.mcp/servers/<name> and everything under it.
// It refuses unsafe names, symlinks and entries that are not directories.
func RemoveServer(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid server name %q", name)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	serverPath := filepath.Join(cwd, ".mcp", "servers", name)
	info, err := os.Lstat(serverPath)
	if os.IsNotExist(err) {
		return fmt.Errorf("server '%s' not found in .mcp/servers/", name)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("server '%s' is not a directory", name)
	}

	return os.RemoveAll(serverPath)
}
