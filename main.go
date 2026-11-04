// Command mcpm is the Model Context Protocol Manager CLI.
//
// Startup cost: main does no work of its own. It hands control straight to
// cmd.Execute, which reads the config file only for commands that need it
// (help, version and shell completion skip it; see cmd.needsConfig). Keep
// this function free of I/O, network calls, subprocesses and other setup so
// that `mcpm help` and `mcpm --version` print at once.
package main

import (
	"mcpm/cmd"
)

// main runs the root command; cmd.Execute exits the process with its code.
func main() {
	cmd.Execute()
}
