package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mcpm/internal/fetcher"
	"mcpm/internal/injector"

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
A server switched off with mcpm disable is forgotten, so mcpm enable cannot
bring it back. To keep it for later, use mcpm disable instead.

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

		scope := claudeScope(removeGlobal)

		cmd.SilenceUsage = true
		if removeDryRun {
			return dryRunRemove(cwd, name, scope)
		}
		errs := make(map[string]error)

		if removeClaudeCode {
			reportRemove("Claude Code", name, removeOrForget(cwd, name, injector.TargetClaudeCode, removeGlobal), errs)
		}

		if removeGeminiCLI {
			reportRemove("Gemini CLI", name, removeOrForget(cwd, name, injector.TargetGeminiCLI, removeGlobal), errs)
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

	if removeClaudeCode && !dryRunForget(cwd, name, injector.TargetClaudeCode) {
		fmt.Printf("Would run: claude mcp remove --scope %s %s\n", scope, name)
	}

	if removeGeminiCLI && !dryRunForget(cwd, name, injector.TargetGeminiCLI) {
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

// dryRunForget prints that remove would forget the switched-off registration
// of name in tool, and reports whether name is switched off there.
func dryRunForget(cwd, name string, tool injector.TargetTool) bool {
	off, path, err := loadOff(cwd, removeGlobal)
	if err != nil || !off.has(name, tool) {
		return false
	}
	fmt.Printf("Would forget switched-off %s in %s (%s)\n", name, toolLabels[tool], path)
	return true
}

// removeOrForget removes name from tool in the scope. When name is switched
// off there, it forgets the kept registration instead, so mcpm enable cannot
// bring it back.
func removeOrForget(cwd, name string, tool injector.TargetTool, global bool) error {
	off, path, err := loadOff(cwd, global)
	if err != nil {
		return err
	}
	if !off.has(name, tool) {
		return removeRegistration(cwd, name, tool, global)
	}
	off.drop(name, tool)
	if err := saveOff(path, off); err != nil {
		return err
	}
	entry, err := clientEntry(cwd, name, tool, global)
	if err != nil || entry == nil {
		return err
	}
	return removeRegistration(cwd, name, tool, global)
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
	output, err := runClaude(cwd, cmdArgs...)
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}

	return nil
}

// claudeScope returns the claude --scope value of the local or global scope.
func claudeScope(global bool) string {
	if global {
		return "user"
	}
	return "local"
}

// removeRegistration removes name from tool in the local or global scope.
func removeRegistration(cwd, name string, tool injector.TargetTool, global bool) error {
	if tool == injector.TargetClaudeCode {
		return removeFromClaudeCode(cwd, name, claudeScope(global))
	}
	return removeFromGeminiCLI(cwd, name, global)
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

// installedServerDir returns the directory of the installed server name, or
// an error that says name is not installed.
func installedServerDir(name string) (string, error) {
	dir, err := fetcher.ServerDirToRemove(name)
	if err != nil {
		return "", fmt.Errorf("%w (run mcpm list to see installed servers)", err)
	}
	return dir, nil
}

// serverRegistrations returns the Claude Code and then the Gemini CLI
// registrations of name, local before global, with env names only.
func serverRegistrations(cwd, name string) ([]injector.Registration, error) {
	regs, err := injector.ClaudeCodeRegistrations(cwd, name)
	if err != nil {
		return nil, fmt.Errorf("Claude Code: %w", err)
	}
	gemini, err := injector.GeminiCLIRegistrations(cwd, name)
	if err != nil {
		return nil, fmt.Errorf("Gemini CLI: %w", err)
	}
	return append(regs, gemini...), nil
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

var (
	switchClaudeCode bool
	switchGeminiCLI  bool
	switchGlobal     bool
)

var disableCmd = &cobra.Command{
	Use:     "disable <name>",
	Aliases: []string{"off"},
	Short:   "Switch an MCP server off in Claude Code and/or Gemini CLI without removing it",
	Long: `Switch an MCP server off: Claude Code and Gemini CLI no longer start it,
but mcpm keeps its folder in .mcp/servers, its build and its registration,
including its environment variables, so mcpm enable switches it back on.

The registrations taken out are kept in .mcp/disabled.json, or in
~/.mcp/disabled-global.json with --global.

Examples:
  # Switch off in both (current directory)
  mcpm disable myserver

  # Switch off only in Claude Code
  mcpm disable myserver --claude

  # Switch off in the global configuration
  mcpm disable myserver --global`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeServerNames,
	RunE:              switchRunE(false),
}

var enableCmd = &cobra.Command{
	Use:     "enable <name>",
	Aliases: []string{"on"},
	Short:   "Switch an MCP server that mcpm disable switched off back on",
	Long: `Switch an MCP server back on: its registration in Claude Code and/or
Gemini CLI is restored as it was when mcpm disable switched it off.

Examples:
  mcpm enable myserver
  mcpm enable myserver --gemini
  mcpm enable myserver --global`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeServerNames,
	RunE:              switchRunE(true),
}

func switchRunE(on bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		tools := doctorTools(switchClaudeCode, switchGeminiCLI)
		return switchServer(cmd.OutOrStdout(), cmd.ErrOrStderr(), cwd, args[0], tools, switchGlobal, on)
	}
}

// offServers maps a server name to the registrations mcpm disable took out of
// each client: the client's entry as it was written.
type offServers map[string]map[injector.TargetTool]json.RawMessage

func (o offServers) has(name string, tool injector.TargetTool) bool {
	_, ok := o[name][tool]
	return ok
}

func (o offServers) keep(name string, tool injector.TargetTool, entry json.RawMessage) {
	if o[name] == nil {
		o[name] = make(map[injector.TargetTool]json.RawMessage)
	}
	o[name][tool] = entry
}

func (o offServers) drop(name string, tool injector.TargetTool) {
	delete(o[name], tool)
	if len(o[name]) == 0 {
		delete(o, name)
	}
}

// offPath returns the file that keeps the servers switched off in the project
// cwd, or globally.
func offPath(cwd string, global bool) (string, error) {
	if !global {
		return filepath.Join(cwd, ".mcp", "disabled.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not get home directory: %w", err)
	}
	return filepath.Join(home, ".mcp", "disabled-global.json"), nil
}

// loadOff reads the servers switched off in the scope and the path of the file
// that keeps them. A missing file switches nothing off.
func loadOff(cwd string, global bool) (offServers, string, error) {
	path, err := offPath(cwd, global)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(offServers), path, nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("could not read %s: %w", path, err)
	}
	var off offServers
	if err := json.Unmarshal(data, &off); err != nil {
		return nil, "", fmt.Errorf("could not parse %s: %w", path, err)
	}
	if off == nil {
		off = make(offServers)
	}
	return off, path, nil
}

// saveOff writes off to path. The entries can hold environment values, so
// only the owner can read the file.
func saveOff(path string, off offServers) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(off, "", "  ")
	if err != nil {
		return err
	}
	return writeSettingsFile(path, data, true)
}

// rawClaudeConfig holds the user-scope servers and the local-scope servers of
// each project in .claude.json, as written.
type rawClaudeConfig struct {
	McpServers map[string]json.RawMessage `json:"mcpServers"`
	Projects   map[string]struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	} `json:"projects"`
}

// clientEntry returns the entry of name in tool's config for the scope as
// written, or nil when name is not registered there.
func clientEntry(cwd, name string, tool injector.TargetTool, global bool) (json.RawMessage, error) {
	if tool == injector.TargetClaudeCode {
		return claudeEntry(cwd, name, global)
	}
	return geminiEntry(cwd, name, global)
}

func claudeEntry(cwd, name string, global bool) (json.RawMessage, error) {
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
	var cfg rawClaudeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("could not parse %s: %w", path, err)
	}
	if global {
		return cfg.McpServers[name], nil
	}
	return cfg.Projects[cwd].McpServers[name], nil
}

func geminiEntry(cwd, name string, global bool) (json.RawMessage, error) {
	path, err := geminiSettingsPath(cwd, global)
	if err != nil {
		return nil, err
	}
	cfg, err := loadGeminiSettings(path)
	if err != nil {
		return nil, err
	}
	servers, _ := cfg["mcpServers"].(map[string]interface{})
	entry, ok := servers[name]
	if !ok {
		return nil, nil
	}
	return json.Marshal(entry)
}

// restoreEntry registers name in tool in the scope with the entry mcpm
// disable kept.
func restoreEntry(cwd, name string, tool injector.TargetTool, global bool, entry json.RawMessage) error {
	if tool == injector.TargetGeminiCLI {
		return restoreGeminiEntry(cwd, name, global, entry)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, entry); err != nil {
		return fmt.Errorf("server %s: %w", name, err)
	}
	out, err := runClaude(cwd, "mcp", "add-json", "--scope", claudeScope(global), name, compact.String())
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// restoreGeminiEntry adds entry as name to the project or global Gemini CLI
// settings and keeps every other field.
func restoreGeminiEntry(cwd, name string, global bool, entry json.RawMessage) error {
	path, err := geminiSettingsPath(cwd, global)
	if err != nil {
		return err
	}
	cfg, err := loadGeminiSettings(path)
	if err != nil {
		return err
	}
	var value map[string]interface{}
	if err := json.Unmarshal(entry, &value); err != nil {
		return fmt.Errorf("server %s: %w", name, err)
	}
	servers, _ := cfg["mcpServers"].(map[string]interface{})
	if servers == nil {
		servers = make(map[string]interface{})
	}
	servers[name] = value
	cfg["mcpServers"] = servers
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("could not create .gemini dir: %w", err)
	}
	env, _ := value["env"].(map[string]interface{})
	return writeSettingsFile(path, data, len(env) > 0)
}

// absentError says that a client has nothing to switch: name is neither
// registered nor kept switched off there.
type absentError struct{ msg string }

func (e absentError) Error() string { return e.msg }

func isAbsent(err error) bool {
	var absent absentError
	return errors.As(err, &absent)
}

// switchResult is the outcome of switching name in one client.
type switchResult struct {
	label   string
	changed bool
	err     error
}

// switchServer switches name off, or back on, in each of tools in the scope.
// It reports each change to w and each failure to errW. Clients that do not
// have name are only reported, unless none of tools has it.
func switchServer(w, errW io.Writer, cwd, name string, tools []injector.TargetTool, global, on bool) error {
	off, path, err := loadOff(cwd, global)
	if err != nil {
		return err
	}
	results := make([]switchResult, 0, len(tools))
	for _, tool := range tools {
		changed, err := switchTool(cwd, name, tool, global, on, off, path)
		results = append(results, switchResult{scopedLabel(tool, scopeName(global)), changed, err})
	}
	return reportSwitch(w, errW, name, on, results)
}

// reportSwitch prints each result and returns the failures. A client that
// does not have name is a failure only when no client has it.
func reportSwitch(w, errW io.Writer, name string, on bool, results []switchResult) error {
	state, action := "off", "switch off"
	if on {
		state, action = "on", "switch on"
	}
	allAbsent := true
	for _, r := range results {
		allAbsent = allAbsent && isAbsent(r.err)
	}
	errs := make(map[string]error)
	for _, r := range results {
		switch {
		case isAbsent(r.err) && !allAbsent:
			fmt.Fprintf(w, "Skipped %s: %v\n", r.label, r.err)
		case r.err != nil:
			fmt.Fprintf(errW, "Error switching %s %s in %s: %v\n", name, state, r.label, r.err)
			errs[r.label] = r.err
		case r.changed:
			fmt.Fprintf(w, "Switched %s %s in %s\n", name, state, r.label)
		default:
			fmt.Fprintf(w, "%s is already %s in %s\n", name, state, r.label)
		}
	}
	return clientFailures(action, errs)
}

// switchTool switches name off or on in tool in the scope, keeping off in
// path, and reports whether anything changed.
func switchTool(cwd, name string, tool injector.TargetTool, global, on bool, off offServers, path string) (bool, error) {
	entry, err := clientEntry(cwd, name, tool, global)
	if err != nil {
		return false, err
	}
	if on {
		return switchOn(cwd, name, tool, global, entry != nil, off, path)
	}
	return switchOff(cwd, name, tool, global, entry, off, path)
}

// switchOff keeps entry, the registration of name in tool, in off and then
// removes the registration. If the removal fails, it forgets entry again.
func switchOff(cwd, name string, tool injector.TargetTool, global bool, entry json.RawMessage, off offServers, path string) (bool, error) {
	if entry == nil {
		if off.has(name, tool) {
			return false, nil
		}
		return false, absentError{fmt.Sprintf("server %s not found", name)}
	}
	off.keep(name, tool, entry)
	if err := saveOff(path, off); err != nil {
		return false, err
	}
	if err := removeRegistration(cwd, name, tool, global); err != nil {
		off.drop(name, tool)
		if saveErr := saveOff(path, off); saveErr != nil {
			return false, fmt.Errorf("%w (forgetting the kept registration in %s also failed: %v)", err, path, saveErr)
		}
		return false, err
	}
	return true, nil
}

// switchOn restores the registration of name in tool that off keeps and then
// forgets it. A registration added since mcpm disable wins over the kept one.
func switchOn(cwd, name string, tool injector.TargetTool, global, registered bool, off offServers, path string) (bool, error) {
	entry, ok := off[name][tool]
	if !ok && registered {
		return false, nil
	}
	if !ok {
		return false, absentError{fmt.Sprintf("server %s is not switched off (run mcpm list to see which servers are off)", name)}
	}
	if !registered {
		if err := restoreEntry(cwd, name, tool, global, entry); err != nil {
			return false, err
		}
	}
	off.drop(name, tool)
	return !registered, saveOff(path, off)
}

func init() {
	for _, c := range []*cobra.Command{disableCmd, enableCmd} {
		c.Flags().BoolVar(&switchClaudeCode, "claude", false, "Only Claude Code")
		c.Flags().BoolVar(&switchGeminiCLI, "gemini", false, "Only Gemini CLI")
		c.Flags().BoolVarP(&switchGlobal, "global", "g", false, "Global configuration")
		rootCmd.AddCommand(c)
	}

	removeCmd.Flags().BoolVar(&removeClaudeCode, "claude", false, "Remove only from Claude Code")
	removeCmd.Flags().BoolVar(&removeGeminiCLI, "gemini", false, "Remove only from Gemini CLI")
	removeCmd.Flags().BoolVarP(&removeGlobal, "global", "g", false, "Remove from global configuration")
	removeCmd.Flags().BoolVar(&removePurge, "purge", false, "Also delete the server directory from .mcp/servers")
	removeCmd.Flags().BoolVar(&removeDryRun, "dry-run", false, "Print what would be removed and change nothing")
	rootCmd.AddCommand(removeCmd)
}
