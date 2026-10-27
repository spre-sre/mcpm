package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"text/tabwriter"

	"mcpm/internal/builder"
	"mcpm/internal/fetcher"
	"mcpm/internal/injector"

	"github.com/spf13/cobra"
)

var envShow bool

// hiddenValue replaces an environment value unless --show is given.
const hiddenValue = "****"

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Manage the environment variables of a registered MCP server",
	Long: `List, set and unset the environment variables of an MCP server.

Every change is applied to each registration of the server: Claude Code and
Gemini CLI, local and global.

Examples:
  mcpm env list myserver
  mcpm env list myserver --show
  mcpm env set myserver API_KEY=xxx REGION=eu
  mcpm env unset myserver REGION`,
}

var envListCmd = &cobra.Command{
	Use:   "list <name>",
	Short: "List the environment variables of a server (values hidden unless --show)",
	Long: `List the environment variables of each registration of a server, and the
variables its mcp.json requires that are not set. Values are hidden unless
--show is given.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeServerNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		targets, err := envTargets(cwd, args[0])
		if err != nil {
			return err
		}
		required, err := requiredEnv(args[0])
		if err != nil {
			return err
		}
		return writeEnvList(cmd.OutOrStdout(), args[0], targets, required, envShow)
	},
}

var envSetCmd = &cobra.Command{
	Use:               "set <name> KEY=VALUE [KEY=VALUE...]",
	Short:             "Set environment variables of a server in every client it is registered in",
	Args:              cobra.MinimumNArgs(2),
	ValidArgsFunction: completeServerNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		vars, err := parseEnvVars(args[1:])
		if err != nil {
			return err
		}
		cmd.SilenceUsage = true
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		return changeEnv(cmd.OutOrStdout(), cmd.ErrOrStderr(), cwd, args[0], setVars(vars))
	},
}

var envUnsetCmd = &cobra.Command{
	Use:               "unset <name> KEY [KEY...]",
	Short:             "Unset environment variables of a server in every client it is registered in",
	Args:              cobra.MinimumNArgs(2),
	ValidArgsFunction: completeServerNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		for _, key := range args[1:] {
			if !envKeyRe.MatchString(key) {
				return fmt.Errorf("invalid environment variable name %q: must match [A-Za-z_][A-Za-z0-9_]*", key)
			}
		}
		cmd.SilenceUsage = true
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		return changeEnv(cmd.OutOrStdout(), cmd.ErrOrStderr(), cwd, args[0], unsetVars(args[1:]))
	},
}

// envServerDef is the part of a client's server entry that mcpm env reads and
// rewrites. Type is empty or "stdio" for servers that start a command.
type envServerDef struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// envTarget is one registration of a server: the client, its scope
// (injector.ScopeLocal or injector.ScopeGlobal) and the stored entry.
type envTarget struct {
	Tool  injector.TargetTool
	Scope string
	Def   envServerDef
}

func (t envTarget) label() string {
	return scopedLabel(t.Tool, t.Scope)
}

// scopedLabel names a client and scope, e.g. "Claude Code (local)".
func scopedLabel(tool injector.TargetTool, scope string) string {
	return fmt.Sprintf("%s (%s)", toolLabels[tool], scope)
}

// bothScopes lists the scopes as global flags, local before global.
var bothScopes = []bool{false, true}

func scopeName(global bool) string {
	if global {
		return injector.ScopeGlobal
	}
	return injector.ScopeLocal
}

// envTargets returns the Claude Code and then the Gemini CLI registrations of
// name, local before global. It fails when name is registered nowhere.
func envTargets(cwd, name string) ([]envTarget, error) {
	claude, err := claudeEnvTargets(cwd, name)
	if err != nil {
		return nil, fmt.Errorf("Claude Code: %w", err)
	}
	gemini, err := geminiEnvTargets(cwd, name)
	if err != nil {
		return nil, fmt.Errorf("Gemini CLI: %w", err)
	}
	targets := append(claude, gemini...)
	if len(targets) == 0 {
		return nil, fmt.Errorf("%s is not registered in Claude Code or Gemini CLI", name)
	}
	return targets, nil
}

// envClaudeConfig holds the user-scope servers and the local-scope servers of
// each project in .claude.json, with their environment.
type envClaudeConfig struct {
	McpServers map[string]envServerDef `json:"mcpServers"`
	Projects   map[string]struct {
		McpServers map[string]envServerDef `json:"mcpServers"`
	} `json:"projects"`
}

// claudeJSONPath returns $CLAUDE_CONFIG_DIR/.claude.json when set, else
// ~/.claude.json.
func claudeJSONPath() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not get home directory: %w", err)
	}
	return filepath.Join(home, ".claude.json"), nil
}

// claudeEnvTargets reads the local (project cwd) and global registrations of
// name from .claude.json. A missing file registers nothing.
func claudeEnvTargets(cwd, name string) ([]envTarget, error) {
	path, err := claudeJSONPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("could not read %s: %w", path, err)
	}
	var cfg envClaudeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("could not parse %s: %w", path, err)
	}
	var targets []envTarget
	if def, ok := cfg.Projects[cwd].McpServers[name]; ok {
		targets = append(targets, envTarget{Tool: injector.TargetClaudeCode, Scope: injector.ScopeLocal, Def: def})
	}
	if def, ok := cfg.McpServers[name]; ok {
		targets = append(targets, envTarget{Tool: injector.TargetClaudeCode, Scope: injector.ScopeGlobal, Def: def})
	}
	return targets, nil
}

// geminiEnvTargets reads the project and global Gemini CLI registrations of
// name.
func geminiEnvTargets(cwd, name string) ([]envTarget, error) {
	var targets []envTarget
	for _, global := range bothScopes {
		def, ok, err := geminiEnvDef(cwd, name, global)
		if err != nil {
			return nil, err
		}
		if ok {
			targets = append(targets, envTarget{Tool: injector.TargetGeminiCLI, Scope: scopeName(global), Def: def})
		}
	}
	return targets, nil
}

func geminiEnvDef(cwd, name string, global bool) (envServerDef, bool, error) {
	path, err := geminiSettingsPath(cwd, global)
	if err != nil {
		return envServerDef{}, false, err
	}
	cfg, err := loadGeminiSettings(path)
	if err != nil {
		return envServerDef{}, false, err
	}
	servers, _ := cfg["mcpServers"].(map[string]interface{})
	entry, ok := servers[name]
	if !ok {
		return envServerDef{}, false, nil
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return envServerDef{}, false, err
	}
	var def envServerDef
	if err := json.Unmarshal(data, &def); err != nil {
		return envServerDef{}, false, fmt.Errorf("%s: server %s: %w", path, name, err)
	}
	return def, true, nil
}

// requiredEnv returns the requiredEnv of the installed server's mcp.json. A
// server that is not in .mcp/servers or has no mcp.json requires nothing.
func requiredEnv(name string) ([]string, error) {
	dir, err := fetcher.GetServerPath(name)
	if err != nil {
		return nil, nil
	}
	path := filepath.Join(dir, "mcp.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("could not read %s: %w", path, err)
	}
	var m builder.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("could not parse %s: %w", path, err)
	}
	return m.RequiredEnv, nil
}

// writeEnvList writes one line per variable and registration: the value
// (hidden unless show), or "(not set, required)" for a required variable the
// registration lacks.
func writeEnvList(w io.Writer, name string, targets []envTarget, required []string, show bool) error {
	keys := envKeys(targets, required)
	if len(keys) == 0 {
		_, err := fmt.Fprintf(w, "%s has no environment variables\n", name)
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, key := range keys {
		for _, t := range targets {
			if line := envLine(key, t, slices.Contains(required, key), show); line != "" {
				fmt.Fprintln(tw, line)
			}
		}
	}
	return tw.Flush()
}

func envLine(key string, t envTarget, required, show bool) string {
	value, ok := t.Def.Env[key]
	switch {
	case ok && show:
		return fmt.Sprintf("%s\t%s\t%s", key, t.label(), value)
	case ok:
		return fmt.Sprintf("%s\t%s\t%s", key, t.label(), hiddenValue)
	case required:
		return fmt.Sprintf("%s\t%s\t(not set, required)", key, t.label())
	}
	return ""
}

// envKeys returns the sorted names set in any registration or required.
func envKeys(targets []envTarget, required []string) []string {
	seen := make(map[string]bool)
	for _, key := range required {
		seen[key] = true
	}
	for _, t := range targets {
		for key := range t.Def.Env {
			seen[key] = true
		}
	}
	return sortedEnvKeys(seen)
}

func sortedEnvKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// setVars returns a change that sets vars and reports whether env changed.
func setVars(vars map[string]string) func(map[string]string) bool {
	return func(env map[string]string) bool {
		changed := false
		for key, value := range vars {
			if old, ok := env[key]; !ok || old != value {
				env[key] = value
				changed = true
			}
		}
		return changed
	}
}

// unsetVars returns a change that removes keys and reports whether env
// changed.
func unsetVars(keys []string) func(map[string]string) bool {
	return func(env map[string]string) bool {
		changed := false
		for _, key := range keys {
			if _, ok := env[key]; ok {
				delete(env, key)
				changed = true
			}
		}
		return changed
	}
}

// changeEnv applies change to the environment of every registration of name
// and rewrites the registrations it changed. It reports each update to w and
// each failure to errW.
func changeEnv(w, errW io.Writer, cwd, name string, change func(map[string]string) bool) error {
	targets, err := envTargets(cwd, name)
	if err != nil {
		return err
	}
	errs := make(map[string]error)
	changed := false
	for _, t := range targets {
		env := maps.Clone(t.Def.Env)
		if env == nil {
			env = make(map[string]string)
		}
		if !change(env) {
			continue
		}
		changed = true
		if err := applyEnv(cwd, name, t, env); err != nil {
			fmt.Fprintf(errW, "Error updating %s: %v\n", t.label(), err)
			errs[t.label()] = err
			continue
		}
		fmt.Fprintf(w, "Updated %s in %s\n", name, t.label())
	}
	if !changed {
		fmt.Fprintf(w, "%s: nothing to change\n", name)
	}
	return clientFailures("update the environment of", errs)
}

// applyEnv rewrites registration t of name with env.
func applyEnv(cwd, name string, t envTarget, env map[string]string) error {
	if t.Def.Command == "" || (t.Def.Type != "" && t.Def.Type != "stdio") {
		return fmt.Errorf("%s does not start a local command; only stdio servers take environment variables", name)
	}
	if t.Tool == injector.TargetClaudeCode {
		return setClaudeEnv(cwd, name, t, env)
	}
	return setGeminiEnv(cwd, name, t.Scope == injector.ScopeGlobal, env)
}

// runClaude runs the claude CLI in dir; tests replace it.
var runClaude = func(dir string, args ...string) ([]byte, error) {
	c := exec.Command("claude", args...)
	c.Dir = dir
	return c.CombinedOutput()
}

// setClaudeEnv re-registers name in Claude Code with env. If the new
// registration fails, it restores the old one.
func setClaudeEnv(cwd, name string, t envTarget, env map[string]string) error {
	scope := "local"
	if t.Scope == injector.ScopeGlobal {
		scope = "user"
	}
	if out, err := runClaude(cwd, "mcp", "remove", "--scope", scope, name); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	out, err := runClaude(cwd, claudeAddArgs(scope, name, t.Def, env)...)
	if err == nil {
		return nil
	}
	if _, restoreErr := runClaude(cwd, claudeAddArgs(scope, name, t.Def, t.Def.Env)...); restoreErr != nil {
		return fmt.Errorf("%w: %s (restoring the old registration also failed: %v)", err, out, restoreErr)
	}
	return fmt.Errorf("%w: %s", err, out)
}

// claudeAddArgs returns the claude arguments that register def with env. The
// name comes before the variadic --env options and "--" ends them, so neither
// the name nor an argument of the server is read as an option.
func claudeAddArgs(scope, name string, def envServerDef, env map[string]string) []string {
	args := []string{"mcp", "add", "--scope", scope, "--transport", "stdio", name}
	for _, key := range sortedEnvKeys(env) {
		args = append(args, "--env", key+"="+env[key])
	}
	args = append(args, "--", def.Command)
	return append(args, def.Args...)
}

// setGeminiEnv replaces the env of name in the project or global Gemini CLI
// settings and keeps every other field.
func setGeminiEnv(cwd, name string, global bool, env map[string]string) error {
	configPath, err := geminiSettingsPath(cwd, global)
	if err != nil {
		return err
	}
	cfg, mcpServers, err := loadGeminiServer(configPath, name)
	if err != nil {
		return err
	}
	entry, ok := mcpServers[name].(map[string]interface{})
	if !ok {
		return fmt.Errorf("%s: server %s is not a JSON object", configPath, name)
	}
	if len(env) == 0 {
		delete(entry, "env")
	} else {
		entry["env"] = env
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeSettingsFile(configPath, data, len(env) > 0)
}

func init() {
	envListCmd.Flags().BoolVar(&envShow, "show", false, "Print the values of the variables")
	envCmd.AddCommand(envListCmd, envSetCmd, envUnsetCmd)
	rootCmd.AddCommand(envCmd)
}
