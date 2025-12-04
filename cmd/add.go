package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var (
	addTransport string
	addEnvVars   []string
	addClaudeCode bool
	addGeminiCLI  bool
)

var addCmd = &cobra.Command{
	Use:   "add <name> <command-or-url> [args...]",
	Short: "Add an MCP server directly without cloning",
	Long: `Add an existing MCP server to Claude Code and/or Gemini CLI.

This is useful for:
  - HTTP/SSE MCP servers (remote endpoints)
  - Already installed local servers
  - Pre-built binaries

Examples:
  # Add HTTP server
  mcpm add sentry https://mcp.sentry.dev/mcp --transport http

  # Add SSE server
  mcpm add slack https://mcp.slack.com/sse --transport sse

  # Add local stdio server
  mcpm add myserver /usr/local/bin/my-mcp-server

  # Add with environment variables
  mcpm add myserver node /path/to/server.js -e API_KEY=xxx -e SECRET=yyy

  # Add only to Claude Code
  mcpm add myserver /path/to/server --claude

  # Add only to Gemini CLI
  mcpm add myserver /path/to/server --gemini`,
	Args: cobra.MinimumNArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		commandOrURL := args[1]
		serverArgs := args[2:]

		// Default to both if neither specified
		if !addClaudeCode && !addGeminiCLI {
			addClaudeCode = true
			addGeminiCLI = true
		}

		// Parse environment variables
		env := make(map[string]string)
		for _, e := range addEnvVars {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) == 2 {
				env[parts[0]] = parts[1]
			}
		}

		// Detect transport type if not specified
		if addTransport == "" {
			if strings.HasPrefix(commandOrURL, "http://") || strings.HasPrefix(commandOrURL, "https://") {
				addTransport = "http"
			} else {
				addTransport = "stdio"
			}
		}

		cwd, _ := os.Getwd()

		if addClaudeCode {
			if err := addToClaudeCode(cwd, name, commandOrURL, serverArgs, env, addTransport); err != nil {
				fmt.Printf("Error adding to Claude Code: %v\n", err)
			} else {
				fmt.Printf("Added %s to Claude Code\n", name)
			}
		}

		if addGeminiCLI {
			if err := addToGeminiCLI(cwd, name, commandOrURL, serverArgs, env, addTransport); err != nil {
				fmt.Printf("Error adding to Gemini CLI: %v\n", err)
			} else {
				fmt.Printf("Added %s to Gemini CLI\n", name)
			}
		}
	},
}

func addToClaudeCode(cwd, name, commandOrURL string, args []string, env map[string]string, transport string) error {
	// Build command args for claude mcp add
	cmdArgs := []string{"mcp", "add", "--transport", transport}

	// Add environment variables
	for key, value := range env {
		cmdArgs = append(cmdArgs, "--env", fmt.Sprintf("%s=%s", key, value))
	}

	// Add server name and command/URL
	cmdArgs = append(cmdArgs, name, commandOrURL)

	// Add server args
	cmdArgs = append(cmdArgs, args...)

	// Run claude mcp add command
	cmd := exec.Command("claude", cmdArgs...)
	cmd.Dir = cwd

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}

	return nil
}

func addToGeminiCLI(cwd, name, commandOrURL string, args []string, env map[string]string, transport string) error {
	configDir := filepath.Join(cwd, ".gemini")
	configPath := filepath.Join(configDir, "settings.json")

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("could not create .gemini dir: %w", err)
	}

	// Read existing config
	var cfg map[string]interface{}
	if data, err := os.ReadFile(configPath); err == nil {
		json.Unmarshal(data, &cfg)
	}
	if cfg == nil {
		cfg = make(map[string]interface{})
	}

	// Get or create mcpServers
	mcpServers, ok := cfg["mcpServers"].(map[string]interface{})
	if !ok {
		mcpServers = make(map[string]interface{})
	}

	// Create server entry based on transport
	var serverDef map[string]interface{}
	if transport == "http" || transport == "sse" {
		serverDef = map[string]interface{}{
			"type": transport,
			"url":  commandOrURL,
		}
	} else {
		serverDef = map[string]interface{}{
			"type":    "stdio",
			"command": commandOrURL,
			"args":    args,
		}
	}

	if len(env) > 0 {
		serverDef["env"] = env
	}

	mcpServers[name] = serverDef
	cfg["mcpServers"] = mcpServers

	// Write config
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

func init() {
	addCmd.Flags().StringVarP(&addTransport, "transport", "t", "", "Transport type: stdio, http, sse (auto-detected if not specified)")
	addCmd.Flags().StringArrayVarP(&addEnvVars, "env", "e", []string{}, "Environment variables (KEY=VALUE)")
	addCmd.Flags().BoolVar(&addClaudeCode, "claude", false, "Add only to Claude Code")
	addCmd.Flags().BoolVar(&addGeminiCLI, "gemini", false, "Add only to Gemini CLI")
	rootCmd.AddCommand(addCmd)
}
