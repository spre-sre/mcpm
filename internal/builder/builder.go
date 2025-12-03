package builder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func DetectAndBuild(repoPath string) (*BuildResult, error) {
	absPath, _ := filepath.Abs(repoPath)

	// 1. Check for explicit mcp.json
	manifestPath := filepath.Join(absPath, "mcp.json")
	if _, err := os.Stat(manifestPath); err == nil {
		return buildFromManifest(absPath, manifestPath)
	}

	// 2. Heuristics
	if exists(filepath.Join(absPath, "package.json")) {
		return buildNode(absPath)
	}
	if exists(filepath.Join(absPath, "pyproject.toml")) || exists(filepath.Join(absPath, "requirements.txt")) {
		return buildPython(absPath)
	}
	if exists(filepath.Join(absPath, "go.mod")) {
		return buildGo(absPath)
	}

	return nil, fmt.Errorf("could not detect project type (no mcp.json, package.json, requirements.txt, or go.mod)")
}

func buildFromManifest(repoPath, manifestPath string) (*BuildResult, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid mcp.json: %w", err)
	}

	if m.BuildCmd != "" {
		if err := runShellCmd(repoPath, m.BuildCmd); err != nil {
			return nil, err
		}
	}

	// Ensure RunCmd is absolute or resolved?
	// For manifest, we assume the user knows what they are doing, but if it is "python", we might want the venv python.
	// For MVP, take literally.
	return &BuildResult{
		Command:  m.RunCmd,
		Args:     m.Args,
		EnvNeeds: m.RequiredEnv,
	}, nil
}
