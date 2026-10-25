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

type lookupFunc func(cwd, name string, global bool) (McpServerDef, bool, error)

var checkers = map[TargetTool]struct {
	label  string
	lookup lookupFunc
}{
	TargetClaudeCode: {"Claude Code", lookupClaudeCode},
	TargetGeminiCLI:  {"Gemini CLI", lookupGeminiCLI},
}

// Check compares the registration of server name in each tool with the
// command and args in result. It returns one line per problem: the server is
// not registered, the tool's config cannot be read, or the registration runs
// a different command.
func Check(name string, result *builder.BuildResult, tools []TargetTool, global bool) []string {
	cwd, _ := os.Getwd()
	var problems []string
	for _, tool := range tools {
		if problem := checkTool(cwd, name, result, tool, global); problem != "" {
			problems = append(problems, problem)
		}
	}
	return problems
}

func checkTool(cwd, name string, result *builder.BuildResult, tool TargetTool, global bool) string {
	checker, known := checkers[tool]
	if !known {
		return fmt.Sprintf("%s: unknown tool %q", name, tool)
	}
	def, ok, err := checker.lookup(cwd, name, global)
	switch {
	case err != nil:
		return fmt.Sprintf("%s: %s: %v", name, checker.label, err)
	case !ok:
		return fmt.Sprintf("%s: not registered in %s", name, checker.label)
	case def.Command != result.Command || !sameArgs(def.Args, result.Args):
		return fmt.Sprintf("%s: %s runs %q, want %q", name, checker.label,
			formatCommand(def.Command, def.Args), formatCommand(result.Command, result.Args))
	}
	return ""
}

// sameArgs treats nil and empty lists as equal.
func sameArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func formatCommand(command string, args []string) string {
	return strings.Join(append([]string{command}, args...), " ")
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
