package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	removeClaudeCode bool
	removeGeminiCLI  bool
	removeGlobal     bool
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
  mcpm remove myserver --global`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
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

		if removeClaudeCode {
			if err := removeFromClaudeCode(cwd, name, scope); err != nil {
				fmt.Printf("Error removing from Claude Code: %v\n", err)
			} else {
				if removeGlobal {
					fmt.Printf("Removed %s from Claude Code (global)\n", name)
				} else {
					fmt.Printf("Removed %s from Claude Code\n", name)
				}
			}
		}

		if removeGeminiCLI {
			if err := removeFromGeminiCLI(cwd, name, removeGlobal); err != nil {
				fmt.Printf("Error removing from Gemini CLI: %v\n", err)
			} else {
				if removeGlobal {
					fmt.Printf("Removed %s from Gemini CLI (global)\n", name)
				} else {
					fmt.Printf("Removed %s from Gemini CLI\n", name)
				}
			}
		}
	},
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
	rootCmd.AddCommand(removeCmd)
}
