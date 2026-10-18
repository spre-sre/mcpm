package cmd

import "runtime/debug"

// version is the release version. It has no initializer so that a release
// build can set it with -ldflags "-X mcpm/cmd.version=<V>"; the linker cannot
// override a variable initialized by a function call.
var version string

// resolveVersion returns the version to report: ldflags when it is non-empty,
// else "dev". read is not consulted yet.
func resolveVersion(ldflags string, read func() (*debug.BuildInfo, bool)) string {
	if ldflags != "" {
		return ldflags
	}
	return "dev"
}
