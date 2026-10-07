package injector

import (
	"fmt"
	"os/exec"

	"mcpm/internal/builder"
)

// McpServerDef is used for Gemini CLI (includes type field)
type McpServerDef struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

func updateClaudeCode(cwd string, result *builder.BuildResult, env map[string]string, global bool) error {
	name, err := serverName(result)
	if err != nil {
		return err
	}

	// Build command args for claude mcp add
	// Format: claude mcp add [--scope SCOPE] [--env KEY=VALUE]... <name> <command> [args...]
	cmdArgs := []string{"mcp", "add"}

	// Add scope (user for global, local for project-specific)
	if global {
		cmdArgs = append(cmdArgs, "--scope", "user")
	} else {
		cmdArgs = append(cmdArgs, "--scope", "local")
	}

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
