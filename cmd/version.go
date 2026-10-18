package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is the release version. It has no initializer so that a release
// build can set it with -ldflags "-X mcpm/cmd.version=<V>"; the linker cannot
// override a variable initialized by a function call.
var version string

// resolveVersion returns the version to report: ldflags when it is non-empty,
// else the main module version from the build info (what `go install
// <module>@v1.2.3` stamps), else "dev".
func resolveVersion(ldflags string, read func() (*debug.BuildInfo, bool)) string {
	if ldflags != "" {
		return ldflags
	}
	info, ok := read()
	if !ok || info == nil {
		return "dev"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return "dev"
}

// buildSetting returns the value of key in the build info settings, or "" when
// the build info is missing or the key is absent.
func buildSetting(info *debug.BuildInfo, key string) string {
	if info == nil {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

// vcsLines returns the "commit:" and "built:" lines for the VCS revision and
// time stamped in the build info, verbatim; a missing or empty value adds no line.
func vcsLines(read func() (*debug.BuildInfo, bool)) string {
	info, ok := read()
	if !ok {
		return ""
	}
	out := ""
	if rev := buildSetting(info, "vcs.revision"); rev != "" {
		out += "commit: " + rev + "\n"
	}
	if t := buildSetting(info, "vcs.time"); t != "" {
		out += "built: " + t + "\n"
	}
	return out
}

// versionCmd prints the same line as cobra's root --version flag, then the
// VCS commit and build date when the build info has them.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s version %s\n", cmd.Root().Name(), cmd.Root().Version)
		fmt.Fprint(cmd.OutOrStdout(), vcsLines(debug.ReadBuildInfo))
	},
}

func init() {
	rootCmd.Version = resolveVersion(version, debug.ReadBuildInfo)
	rootCmd.AddCommand(versionCmd)
}
