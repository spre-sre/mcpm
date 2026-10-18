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

// versionCmd prints the same line as cobra's root --version flag.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s version %s\n", cmd.Root().Name(), cmd.Root().Version)
	},
}

func init() {
	rootCmd.Version = resolveVersion(version, debug.ReadBuildInfo)
	rootCmd.AddCommand(versionCmd)
}
