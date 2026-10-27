package injector

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

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

// claudeConfig is the part of Claude Code's .claude.json that holds MCP
// servers: user scope at the top level, local scope per project directory.
type claudeConfig struct {
	McpServers map[string]McpServerDef `json:"mcpServers"`
	Projects   map[string]struct {
		McpServers map[string]McpServerDef `json:"mcpServers"`
	} `json:"projects"`
}

// claudeConfigPath returns $CLAUDE_CONFIG_DIR/.claude.json when set, else
// ~/.claude.json.
func claudeConfigPath() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not get home directory: %w", err)
	}
	return filepath.Join(home, ".claude.json"), nil
}

// lookupClaudeCode returns the Claude Code registration of name in the scope
// that updateClaudeCode writes to. A missing config file means the server is
// not registered.
func lookupClaudeCode(cwd, name string, global bool) (McpServerDef, bool, error) {
	path, err := claudeConfigPath()
	if err != nil {
		return McpServerDef{}, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return McpServerDef{}, false, nil
	}
	if err != nil {
		return McpServerDef{}, false, fmt.Errorf("could not read %s: %w", path, err)
	}
	var cfg claudeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return McpServerDef{}, false, fmt.Errorf("could not parse %s: %w", path, err)
	}
	servers := cfg.McpServers
	if !global {
		servers = cfg.Projects[cwd].McpServers
	}
	def, ok := servers[name]
	return def, ok, nil
}

// Registration describes how one tool starts a server. EnvNames holds the
// names of the environment variables only, never their values.
type Registration struct {
	Tool     TargetTool `json:"tool"`
	Scope    string     `json:"scope"`
	Command  string     `json:"command"`
	Args     []string   `json:"args"`
	EnvNames []string   `json:"envNames"`
}

// Registration scopes, matching the --global flag of mcpm install.
const (
	ScopeLocal  = "local"
	ScopeGlobal = "global"
)

func newRegistration(tool TargetTool, global bool, def McpServerDef) Registration {
	scope := ScopeLocal
	if global {
		scope = ScopeGlobal
	}
	envNames := make([]string, 0, len(def.Env))
	for key := range def.Env {
		envNames = append(envNames, key)
	}
	sort.Strings(envNames)
	args := def.Args
	if args == nil {
		args = []string{}
	}
	return Registration{Tool: tool, Scope: scope, Command: def.Command, Args: args, EnvNames: envNames}
}

// registrationsIn returns the local and then the global registration of name
// that lookup finds. A tool where name is not registered yields no entries.
func registrationsIn(tool TargetTool, cwd, name string, lookup lookupFunc) ([]Registration, error) {
	var regs []Registration
	for _, global := range []bool{false, true} {
		def, ok, err := lookup(cwd, name, global)
		if err != nil {
			return nil, err
		}
		if ok {
			regs = append(regs, newRegistration(tool, global, def))
		}
	}
	return regs, nil
}

// ClaudeCodeRegistrations returns the Claude Code registrations of name in
// the local scope of cwd and in the user (global) scope.
func ClaudeCodeRegistrations(cwd, name string) ([]Registration, error) {
	return registrationsIn(TargetClaudeCode, cwd, name, lookupClaudeCode)
}
