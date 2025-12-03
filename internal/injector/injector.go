package injector

import (
	"fmt"
	"os"

	"mcpm/internal/builder"
)

type TargetTool string

const (
	TargetClaudeCode TargetTool = "claude-code"
	TargetGeminiCLI  TargetTool = "gemini-cli"
)

func Register(result *builder.BuildResult, tools []TargetTool, env map[string]string) error {
	cwd, _ := os.Getwd()

	for _, tool := range tools {
		switch tool {
		case TargetClaudeCode:
			if err := updateClaudeCode(cwd, result, env); err != nil {
				return fmt.Errorf("claude configuration failed: %w", err)
			}
		case TargetGeminiCLI:
			if err := updateGeminiCLI(cwd, result, env); err != nil {
				return fmt.Errorf("gemini configuration failed: %w", err)
			}
		}
	}
	return nil
}
