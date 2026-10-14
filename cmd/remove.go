package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"mcpm/internal/fetcher"

	"github.com/spf13/cobra"
)

var (
	removeClaudeCode bool
	removeGeminiCLI  bool
	removeGlobal     bool
	removePurge      bool
)

var removeCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove an MCP server from Claude Code and/or Gemini CLI",
	Long: `Remove an MCP server configuration from Claude Code and/or Gemini CLI.

Examples:
  # Remove from both (current directory)
  mcpm remove myserver

  # Remove only from Claude Code
  mcpm remove myserver --claude

  # Remove only from Gemini CLI
  mcpm remove myserver --gemini

  # Remove from global configuration
  mcpm remove myserver --global

  # Also delete the cloned server from .mcp/servers
  mcpm remove myserver --purge`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		// Default to both if neither specified
		if !removeClaudeCode && !removeGeminiCLI {
			removeClaudeCode = true
			removeGeminiCLI = true
		}

		cwd, _ := os.Getwd()

		scope := "local"
		if removeGlobal {
			scope = "user"
		}

		cmd.SilenceUsage = true
		errs := make(map[string]error)

		if removeClaudeCode {
			reportRemove("Claude Code", name, removeFromClaudeCode(cwd, name, scope), errs)
		}

		if removeGeminiCLI {
			reportRemove("Gemini CLI", name, removeFromGeminiCLI(cwd, name, removeGlobal), errs)
		}

		clientErr := clientFailures("remove", errs)

		if removePurge {
			return purgeServer(name, clientErr)
		}

		return clientErr
	},
}

// reportRemove prints the outcome of one client removal and records failures.
func reportRemove(client, name string, err error, errs map[string]error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error removing from %s: %v\n", client, err)
		errs[client] = err
		return
	}
	if removeGlobal {
		fmt.Printf("Removed %s from %s (global)\n", name, client)
	} else {
		fmt.Printf("Removed %s from %s\n", name, client)
	}
}

// purgeServer deletes the server directory and combines any purge failure
// with the earlier client failure.
func purgeServer(name string, clientErr error) error {
	if err := fetcher.RemoveServer(name); err != nil {
		fmt.Fprintf(os.Stderr, "Error purging server: %v\n", err)
		if clientErr != nil {
			return fmt.Errorf("%w; purge failed: %v", clientErr, err)
		}
		return fmt.Errorf("failed to purge server: %w", err)
	}
	fmt.Printf("Deleted .mcp/servers/%s\n", name)
	return clientErr
}

func removeFromClaudeCode(cwd, name, scope string) error {
	// Build command args for claude mcp remove
	cmdArgs := []string{"mcp", "remove", "--scope", scope, name}

	// Run claude mcp remove command
	cmd := exec.Command("claude", cmdArgs...)
	cmd.Dir = cwd

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}

	return nil
}

func removeFromGeminiCLI(cwd, name string, global bool) error {
	var configPath string

	if global {
		// Global config in ~/.gemini/settings.json
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("could not get home directory: %w", err)
		}
		configPath = filepath.Join(home, ".gemini", "settings.json")
	} else {
		// Project-level config in ./.gemini/settings.json
		configPath = filepath.Join(cwd, ".gemini", "settings.json")
	}

	// Read existing config
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("config file not found: %s", configPath)
		}
		return err
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}

	// Get mcpServers
	mcpServers, ok := cfg["mcpServers"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("server %s not found", name)
	}

	// Check if server exists
	if _, exists := mcpServers[name]; !exists {
		return fmt.Errorf("server %s not found", name)
	}

	// Remove server
	delete(mcpServers, name)
	cfg["mcpServers"] = mcpServers

	// Write config back
	newData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, newData, 0644)
}

func init() {
	removeCmd.Flags().BoolVar(&removeClaudeCode, "claude", false, "Remove only from Claude Code")
	removeCmd.Flags().BoolVar(&removeGeminiCLI, "gemini", false, "Remove only from Gemini CLI")
	removeCmd.Flags().BoolVarP(&removeGlobal, "global", "g", false, "Remove from global configuration")
	removeCmd.Flags().BoolVar(&removePurge, "purge", false, "Also delete the server directory from .mcp/servers")
	rootCmd.AddCommand(removeCmd)
}
