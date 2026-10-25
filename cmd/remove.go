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
	removeDryRun     bool
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
  mcpm remove myserver --purge

  # Show what would be removed without changing anything
  mcpm remove myserver --purge --dry-run`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeServerNames,
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
		if removeDryRun {
			return dryRunRemove(cwd, name, scope)
		}
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

// dryRunRemove prints what remove would change and changes nothing. It
// returns the errors the real run would hit where they can be known
// without changing anything.
func dryRunRemove(cwd, name, scope string) error {
	errs := make(map[string]error)

	if removeClaudeCode {
		fmt.Printf("Would run: claude mcp remove --scope %s %s\n", scope, name)
	}

	if removeGeminiCLI {
		configPath, err := geminiSettingsPath(cwd, removeGlobal)
		if err == nil {
			_, _, err = loadGeminiServer(configPath, name)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error removing from Gemini CLI: %v\n", err)
			errs["Gemini CLI"] = err
		} else {
			fmt.Printf("Would remove %s from Gemini CLI (%s)\n", name, configPath)
		}
	}

	clientErr := clientFailures("remove", errs)
	if !removePurge {
		return clientErr
	}

	if _, err := fetcher.ServerDirToRemove(name); err != nil {
		return purgeFailure(err, clientErr)
	}
	fmt.Printf("Would delete .mcp/servers/%s\n", name)
	return clientErr
}

// purgeServer deletes the server directory and combines any purge failure
// with the earlier client failure.
func purgeServer(name string, clientErr error) error {
	if err := fetcher.RemoveServer(name); err != nil {
		return purgeFailure(err, clientErr)
	}
	fmt.Printf("Deleted .mcp/servers/%s\n", name)
	return clientErr
}

// purgeFailure reports a purge error and combines it with the earlier
// client failure.
func purgeFailure(err, clientErr error) error {
	fmt.Fprintf(os.Stderr, "Error purging server: %v\n", err)
	if clientErr != nil {
		return fmt.Errorf("%w; purge failed: %v", clientErr, err)
	}
	return fmt.Errorf("failed to purge server: %w", err)
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

// geminiSettingsPath returns the Gemini CLI settings file that remove edits.
func geminiSettingsPath(cwd string, global bool) (string, error) {
	if global {
		// Global config in ~/.gemini/settings.json
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not get home directory: %w", err)
		}
		return filepath.Join(home, ".gemini", "settings.json"), nil
	}
	// Project-level config in ./.gemini/settings.json
	return filepath.Join(cwd, ".gemini", "settings.json"), nil
}

// loadGeminiServer reads configPath and checks that it lists the server name.
// It returns the whole config and its mcpServers map.
func loadGeminiServer(configPath, name string) (map[string]interface{}, map[string]interface{}, error) {
	// Read existing config
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("config file not found: %s", configPath)
		}
		return nil, nil, err
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, nil, err
	}

	// Get mcpServers
	mcpServers, ok := cfg["mcpServers"].(map[string]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("server %s not found", name)
	}

	// Check if server exists
	if _, exists := mcpServers[name]; !exists {
		return nil, nil, fmt.Errorf("server %s not found", name)
	}
	return cfg, mcpServers, nil
}

// geminiRegistration reads the Gemini CLI registration of name from the
// project settings, or the global settings when global.
func geminiRegistration(cwd, name string, global bool) (registration, error) {
	configPath, err := geminiSettingsPath(cwd, global)
	if err != nil {
		return registration{}, err
	}
	_, mcpServers, err := loadGeminiServer(configPath, name)
	if err != nil {
		return registration{}, err
	}
	data, err := json.Marshal(mcpServers[name])
	if err != nil {
		return registration{}, err
	}
	var reg registration
	if err := json.Unmarshal(data, &reg); err != nil {
		return registration{}, fmt.Errorf("%s: server %s: %w", configPath, name, err)
	}
	return reg, nil
}

func removeFromGeminiCLI(cwd, name string, global bool) error {
	configPath, err := geminiSettingsPath(cwd, global)
	if err != nil {
		return err
	}
	cfg, mcpServers, err := loadGeminiServer(configPath, name)
	if err != nil {
		return err
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
	removeCmd.Flags().BoolVar(&removeDryRun, "dry-run", false, "Print what would be removed and change nothing")
	rootCmd.AddCommand(removeCmd)
}
