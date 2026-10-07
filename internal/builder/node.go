package builder

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type PackageJSON struct {
	Name    string            `json:"name"`
	Scripts map[string]string `json:"scripts"`
	Main    string            `json:"main"`
	Bin     interface{}       `json:"bin"`
}

func readPackageJSON(path string) (PackageJSON, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PackageJSON{}, fmt.Errorf("could not read %s: %w", path, err)
	}
	var pkg PackageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return PackageJSON{}, fmt.Errorf("invalid package.json at %s: %w", path, err)
	}
	return pkg, nil
}

func commandExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// findMonorepoEntry searches for MCP server entry points in a monorepo
func findMonorepoEntry(path string) string {
	packagesDir := filepath.Join(path, "packages")

	// Common MCP package names to look for
	mcpDirs := []string{"mcp", "server", "mcp-server"}

	for _, dir := range mcpDirs {
		pkgPath := filepath.Join(packagesDir, dir)
		if !exists(pkgPath) {
			continue
		}

		// Check for dist/index.js (TypeScript compiled output)
		distEntry := filepath.Join(pkgPath, "dist", "index.js")
		if exists(distEntry) {
			return distEntry
		}

		// Check package.json for bin or main
		pkg, err := readPackageJSON(filepath.Join(pkgPath, "package.json"))
		if err == nil {
			if pkg.Main != "" {
				mainPath := filepath.Join(pkgPath, pkg.Main)
				if exists(mainPath) {
					return mainPath
				}
			}
		}

		// Check for src/index.js
		srcEntry := filepath.Join(pkgPath, "src", "index.js")
		if exists(srcEntry) {
			return srcEntry
		}
	}

	// Scan all packages for MCP-related ones
	entries, _ := os.ReadDir(packagesDir)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pkgPath := filepath.Join(packagesDir, entry.Name())

		pkg, err := readPackageJSON(filepath.Join(pkgPath, "package.json"))
		if err == nil {
			if strings.Contains(pkg.Name, "mcp") || pkg.Bin != nil {
				distEntry := filepath.Join(pkgPath, "dist", "index.js")
				if exists(distEntry) {
					return distEntry
				}
			}
		}
	}

	return ""
}

func findStandardEntry(path string, pkg PackageJSON) string {
	entryFile := "index.js"
	if pkg.Main != "" {
		entryFile = pkg.Main
	}
	if exists(filepath.Join(path, "dist", "index.js")) {
		entryFile = filepath.Join("dist", "index.js")
	} else if exists(filepath.Join(path, "build", "index.js")) {
		entryFile = filepath.Join("build", "index.js")
	}
	return filepath.Join(path, entryFile)
}

func buildNode(path string) (*BuildResult, error) {
	pkg, err := readPackageJSON(filepath.Join(path, "package.json"))
	if err != nil {
		return nil, err
	}

	mgr := "npm"
	if exists(filepath.Join(path, "yarn.lock")) && commandExists("yarn") {
		mgr = "yarn"
	}
	if exists(filepath.Join(path, "pnpm-lock.yaml")) && commandExists("pnpm") {
		mgr = "pnpm"
	}

	// Install
	if err := runShellCmd(path, mgr+" install"); err != nil {
		return nil, err
	}

	if _, hasBuild := pkg.Scripts["build"]; hasBuild {
		if err := runShellCmd(path, mgr+" run build"); err != nil {
			return nil, err
		}
	}

	// Determine Entry - check for monorepo structure first
	var absEntry string

	// Check for monorepo with packages/ directory
	if exists(filepath.Join(path, "packages")) {
		absEntry = findMonorepoEntry(path)
	}

	// If not found in monorepo, use standard detection
	if absEntry == "" {
		absEntry = findStandardEntry(path, pkg)
	}

	return &BuildResult{
		Command:  "node",
		Args:     []string{absEntry},
		EnvNeeds: []string{}, // Node specific ENV extraction is complex, skipping for MVP
	}, nil
}
