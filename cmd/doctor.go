package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"mcpm/internal/builder"
	"mcpm/internal/fetcher"
	"mcpm/internal/injector"
	"mcpm/internal/server"
)

var (
	doctorClaudeCode bool
	doctorGeminiCLI  bool
	doctorLocal      bool
	doctorGlobal     bool
)

// detectAndBuild is builder.DetectAndBuild; tests replace it to avoid real builds.
var detectAndBuild = builder.DetectAndBuild

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check installed MCP servers for problems",
	Long: `Check every server in .mcp/servers/: its folder exists, it still builds,
its registration in Claude Code and Gemini CLI runs the built command, and
the registration sets every environment variable its mcp.json requires.

Checks the local (project) and the global registrations in one run unless
--local or --global limits it to one scope. When both scopes are checked, a
server only has to be registered in one of them.

A server switched off with mcpm disable is not reported as unregistered in
the clients and scopes it is off in, and a server switched off everywhere
it is checked is not checked at all.

Prints one line per problem, naming the client and scope it belongs to, and
exits with status 1 if it finds any.

Examples:
  mcpm doctor
  mcpm doctor --claude
  mcpm doctor --local
  mcpm doctor --global`,
	Args: cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		tools := doctorTools(doctorClaudeCode, doctorGeminiCLI)
		problems, err := runDoctor(os.Stdout, tools, doctorScopes(doctorLocal, doctorGlobal))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if problems > 0 {
			os.Exit(1)
		}
	},
}

// doctorTools returns the clients to check; both when neither is selected.
func doctorTools(claude, gemini bool) []injector.TargetTool {
	if !claude && !gemini {
		claude, gemini = true, true
	}
	var tools []injector.TargetTool
	if claude {
		tools = append(tools, injector.TargetClaudeCode)
	}
	if gemini {
		tools = append(tools, injector.TargetGeminiCLI)
	}
	return tools
}

// doctorScopes returns the scopes to check as global flags; both when neither
// or both are selected.
func doctorScopes(local, global bool) []bool {
	switch {
	case local && !global:
		return []bool{false}
	case global && !local:
		return []bool{true}
	}
	return bothScopes
}

// runDoctor checks every installed server in scopes, writes one line per
// problem to w and returns the number of problems.
func runDoctor(w io.Writer, tools []injector.TargetTool, scopes []bool) (int, error) {
	servers, err := fetcher.ListServers()
	if err != nil {
		return 0, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 0, err
	}
	off, err := loadOffScopes(cwd, scopes)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, name := range servers {
		for _, problem := range checkServer(name, tools, scopes, off) {
			if _, err := fmt.Fprintln(w, problem); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

// offScopes holds the servers switched off in each scope, by global flag.
type offScopes map[bool]offServers

func loadOffScopes(cwd string, scopes []bool) (offScopes, error) {
	off := make(offScopes)
	for _, global := range scopes {
		servers, _, err := loadOff(cwd, global)
		if err != nil {
			return nil, err
		}
		off[global] = servers
	}
	return off, nil
}

func (o offScopes) has(name string, tool injector.TargetTool, global bool) bool {
	return o[global].has(name, tool)
}

// offIn reports whether name is switched off in tool in one of scopes.
func (o offScopes) offIn(name string, tool injector.TargetTool, scopes []bool) bool {
	for _, global := range scopes {
		if o.has(name, tool, global) {
			return true
		}
	}
	return false
}

// switchedOff reports whether name is switched off in each of tools, in one of
// scopes at least, and registered in none of them, so no client starts it.
func (o offScopes) switchedOff(name string, tools []injector.TargetTool, scopes []bool) bool {
	for _, tool := range tools {
		if !o.offIn(name, tool, scopes) {
			return false
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	for _, global := range scopes {
		defs, err := scopedEnvDefs(cwd, name, global)
		if err != nil {
			return false
		}
		for _, tool := range tools {
			if _, ok := defs[tool]; ok {
				return false
			}
		}
	}
	return true
}

// checkServer returns the problems of one installed server. A server switched
// off everywhere it is checked has none. Registration is checked, in each of
// scopes it is not switched off in, only when the server builds to a command.
func checkServer(name string, tools []injector.TargetTool, scopes []bool, off offScopes) []string {
	if off.switchedOff(name, tools, scopes) {
		return nil
	}
	path, err := fetcher.GetServerPath(name)
	if err != nil {
		return []string{fmt.Sprintf("%s: folder missing: %v", name, err)}
	}
	result, err := detectAndBuild(path)
	if err != nil {
		return []string{fmt.Sprintf("%s: build failed: %v", name, err)}
	}
	var problems []string
	for _, buildErr := range result.BuildErrors {
		problems = append(problems, fmt.Sprintf("%s: build failed: %v", name, buildErr))
	}
	if result.Command == "" {
		return append(problems, fmt.Sprintf("%s: no project detected, nothing to run", name))
	}
	for _, tool := range tools {
		problems = append(problems, registrationProblems(name, result, tool, scopes, off)...)
	}
	for _, global := range scopes {
		problems = append(problems, envProblems(name, result.EnvNeeds, tools, global)...)
	}
	return problems
}

// registrationProblems checks the registration of name in tool in each of
// scopes it is not switched off in, and names the scope in every problem.
// Name missing from a scope is a problem only if it is missing from every
// checked scope and switched off in none.
func registrationProblems(name string, result *builder.BuildResult, tool injector.TargetTool, scopes []bool, off offScopes) []string {
	unregistered := fmt.Sprintf("%s: not registered in %s", name, toolLabels[tool])
	var problems []string
	missing := 0
	for _, global := range scopes {
		if off.has(name, tool, global) {
			continue
		}
		problem := scopeProblem(name, result, tool, global)
		if problem == unregistered {
			missing++
		} else if problem != "" {
			problems = append(problems, withScope(problem, name, tool, scopeName(global)))
		}
	}
	if missing == len(scopes) {
		problems = append(problems, withScope(unregistered, name, tool, scopesLabel(scopes)))
	}
	return problems
}

// scopesLabel names the checked scopes, e.g. "local or global".
func scopesLabel(scopes []bool) string {
	if len(scopes) == 1 {
		return scopeName(scopes[0])
	}
	return injector.ScopeLocal + " or " + injector.ScopeGlobal
}

// scopeProblem returns the problem injector.Check finds with the registration
// of name in tool in one scope, or "" if there is none.
func scopeProblem(name string, result *builder.BuildResult, tool injector.TargetTool, global bool) string {
	problems := injector.Check(name, result, []injector.TargetTool{tool}, global)
	if len(problems) == 0 {
		return ""
	}
	return problems[0]
}

// withScope adds scope to the first mention of tool in problem, which
// injector.Check writes as "<name>: ..." with the client label before any
// command or error text.
func withScope(problem, name string, tool injector.TargetTool, scope string) string {
	prefix := name + ": "
	rest, ok := strings.CutPrefix(problem, prefix)
	if !ok {
		return problem
	}
	return prefix + strings.Replace(rest, toolLabels[tool], scopedLabel(tool, scope), 1)
}

// envProblems returns one line per registration of name in tools that lacks a
// variable in required. A missing registration or an unreadable config is
// already reported by injector.Check.
func envProblems(name string, required []string, tools []injector.TargetTool, global bool) []string {
	if len(required) == 0 {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	defs, err := scopedEnvDefs(cwd, name, global)
	if err != nil {
		return nil
	}
	var problems []string
	for _, tool := range tools {
		def, ok := defs[tool]
		if !ok {
			continue
		}
		if missing := missingKeys(required, def.Env); len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("%s: %s (%s) is missing environment variable(s) %s (run mcpm env set %s KEY=VALUE)",
				name, toolLabels[tool], scopeName(global), strings.Join(missing, ", "), name))
		}
	}
	return problems
}

// missingKeys returns the keys that env does not set, in order.
func missingKeys(keys []string, env map[string]string) []string {
	var missing []string
	for _, key := range keys {
		if _, ok := env[key]; !ok {
			missing = append(missing, key)
		}
	}
	return missing
}

var infoJSON bool

var infoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Show where an installed MCP server is registered and how it starts",
	Long: `Show one server in .mcp/servers/: where it lives on disk, the repository
and commit it came from, and each Claude Code and Gemini CLI registration
(local or global) with the command that starts it and the names of the
environment variables it is given. Values are never printed.

Examples:
  mcpm info myserver
  mcpm info myserver --json`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeServerNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		info, err := collectServerInfo(cwd, args[0])
		if err != nil {
			return err
		}
		return writeServerInfo(cmd.OutOrStdout(), info, infoJSON)
	},
}

// infoRegistration is one registration of a server and the runtime its
// command starts.
type infoRegistration struct {
	injector.Registration
	Runtime string `json:"runtime"`
}

// serverInfo is what mcpm info reports about one installed server.
// Repository and Commit are empty when the server is not a git clone.
type serverInfo struct {
	Name          string             `json:"name"`
	Path          string             `json:"path"`
	Repository    string             `json:"repository"`
	Commit        string             `json:"commit"`
	Registrations []infoRegistration `json:"registrations"`
}

// collectServerInfo describes the installed server name. It fails when name
// is not installed or a client config cannot be read.
func collectServerInfo(cwd, name string) (serverInfo, error) {
	path, err := installedServerDir(name)
	if err != nil {
		return serverInfo{}, err
	}
	regs, err := serverRegistrations(cwd, name)
	if err != nil {
		return serverInfo{}, err
	}
	info := serverInfo{Name: name, Path: path, Registrations: make([]infoRegistration, 0, len(regs))}
	if url, commit, err := repoOrigin(path); err == nil {
		info.Repository, info.Commit = url, commit
	} else {
		info.Commit = headCommit(path)
	}
	for _, reg := range regs {
		info.Registrations = append(info.Registrations, infoRegistration{Registration: reg, Runtime: server.ClassifyLaunchCommand(reg.Command)})
	}
	return info, nil
}

// toolLabels names each client the way the other commands print it.
var toolLabels = map[injector.TargetTool]string{
	injector.TargetClaudeCode: "Claude Code",
	injector.TargetGeminiCLI:  "Gemini CLI",
}

// writeServerInfo writes info to w as indented JSON or as text.
func writeServerInfo(w io.Writer, info serverInfo, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Name:       %s\n", info.Name)
	fmt.Fprintf(&b, "Path:       %s\n", info.Path)
	fmt.Fprintf(&b, "Repository: %s\n", orNone(info.Repository))
	fmt.Fprintf(&b, "Commit:     %s\n", orNone(info.Commit))
	if len(info.Registrations) == 0 {
		b.WriteString("Registered: nowhere\n")
	} else {
		b.WriteString("Registered:\n")
	}
	for _, reg := range info.Registrations {
		writeInfoRegistration(&b, reg)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeInfoRegistration(b *strings.Builder, reg infoRegistration) {
	fmt.Fprintf(b, "  %s (%s): %s [%s]\n", toolLabels[reg.Tool], reg.Scope,
		formatRegistration(registration{Command: reg.Command, Args: reg.Args}), reg.Runtime)
	if len(reg.EnvNames) > 0 {
		fmt.Fprintf(b, "    env: %s\n", strings.Join(reg.EnvNames, ", "))
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func init() {
	infoCmd.Flags().BoolVar(&infoJSON, "json", false, "Print the server as JSON")
	rootCmd.AddCommand(infoCmd)

	doctorCmd.Flags().BoolVar(&doctorClaudeCode, "claude", false, "Check only Claude Code")
	doctorCmd.Flags().BoolVar(&doctorGeminiCLI, "gemini", false, "Check only Gemini CLI")
	doctorCmd.Flags().BoolVarP(&doctorLocal, "local", "l", false, "Check only the local (project) registrations")
	doctorCmd.Flags().BoolVarP(&doctorGlobal, "global", "g", false, "Check only the global registrations")
	rootCmd.AddCommand(doctorCmd)
}
