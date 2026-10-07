package injector

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcpm/internal/builder"
)

type TargetTool string

const (
	TargetClaudeCode TargetTool = "claude-code"
	TargetGeminiCLI  TargetTool = "gemini-cli"
)

func Register(result *builder.BuildResult, tools []TargetTool, env map[string]string, global bool) error {
	cwd, _ := os.Getwd()

	for _, tool := range tools {
		switch tool {
		case TargetClaudeCode:
			if err := updateClaudeCode(cwd, result, env, global); err != nil {
				return fmt.Errorf("claude configuration failed: %w", err)
			}
		case TargetGeminiCLI:
			if err := updateGeminiCLI(cwd, result, env, global); err != nil {
				return fmt.Errorf("gemini configuration failed: %w", err)
			}
		}
	}
	return nil
}

func extractName(path string) string {
	cleaned := filepath.Clean(path)
	parts := strings.Split(cleaned, string(filepath.Separator))
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == ".mcp" && parts[i+1] == "servers" {
			name := parts[i+2]
			if name != "" && name != "." && name != ".." {
				return name
			}
		}
	}
	return ""
}

func serverName(result *builder.BuildResult) (string, error) {
	if len(result.Args) > 0 {
		if name := extractName(result.Args[0]); name != "" {
			return name, nil
		}
	}
	if name := extractName(result.Command); name != "" {
		return name, nil
	}
	return "", fmt.Errorf(
		"cannot derive a server name: neither the first argument nor the command is under .mcp/servers/<name>/ (command %q, args %v)",
		result.Command, result.Args,
	)
}
