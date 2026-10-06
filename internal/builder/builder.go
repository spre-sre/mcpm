package builder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	// Validate before buildCmd runs, so an unusable manifest builds nothing.
	if strings.TrimSpace(m.RunCmd) == "" {
		return nil, fmt.Errorf("invalid mcp.json: runCmd is required")
	}

	if m.BuildCmd != "" {
		if err := runShellCmd(repoPath, m.BuildCmd); err != nil {
			return nil, err
		}
	}

	// Clients start the server from another working directory, so paths in
	// the manifest are made absolute against the repo. Resolve after the
	// build, which may create the files the args name.
	return &BuildResult{
		Command:  resolveCommand(repoPath, m.RunCmd),
		Args:     resolveArgs(repoPath, m.Args),
		EnvNeeds: m.RequiredEnv,
	}, nil
}

// resolveCommand makes a relative runCmd that names a path ("bin/server",
// "./server") absolute against repoPath. A bare name ("node") stays a PATH lookup.
func resolveCommand(repoPath, command string) string {
	if filepath.IsAbs(command) || !strings.ContainsAny(command, "/"+string(filepath.Separator)) {
		return command
	}
	return filepath.Join(repoPath, command)
}

// resolveArgs makes each relative arg that names an existing file or directory
// in repoPath absolute, in place. Empty args, flags ("-x", "--port") and
// absolute paths skip the file system; other values and package names that
// name nothing stay as written.
func resolveArgs(repoPath string, args []string) []string {
	for i, arg := range args {
		if arg == "" || arg[0] == '-' || filepath.IsAbs(arg) {
			continue
		}
		if path := filepath.Join(repoPath, arg); exists(path) {
			args[i] = path
		}
	}
	return args
}
