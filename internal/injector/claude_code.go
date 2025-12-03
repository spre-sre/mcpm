package injector

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"mcpm/internal/builder"
)

// McpServerDef is used for Gemini CLI (includes type field)
type McpServerDef struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

func updateClaudeCode(cwd string, result *builder.BuildResult, env map[string]string) error {
	// Extract server name from path
	// Look for .mcp/servers/<name> pattern
	name := "mcp-server"
	if len(result.Args) > 0 {
		path := result.Args[0]
		// Find "servers" in path and get the next component
		parts := strings.Split(path, string(filepath.Separator))
		for i, part := range parts {
			if part == "servers" && i+1 < len(parts) {
				name = parts[i+1]
				break
			}
		}
	}
	if name == "" || name == "." {
		name = filepath.Base(result.Command)
	}

	// Build command args for claude mcp add
	// Format: claude mcp add [--env KEY=VALUE]... <name> <command> [args...]
	cmdArgs := []string{"mcp", "add"}

	// Add environment variables
	for key, value := range env {
		cmdArgs = append(cmdArgs, "--env", fmt.Sprintf("%s=%s", key, value))
	}

	// Add server name and command
	cmdArgs = append(cmdArgs, name, result.Command)

	// Add server args
	cmdArgs = append(cmdArgs, result.Args...)

	// Run claude mcp add command
	cmd := exec.Command("claude", cmdArgs...)
	cmd.Dir = cwd

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add MCP server: %w: %s", err, string(output))
	}

	return nil
}
