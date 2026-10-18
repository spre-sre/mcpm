package cmd

import (
	"strings"

	"mcpm/internal/fetcher"

	"github.com/spf13/cobra"
)

// completeServerNames completes the single server name argument of a command
// with the installed servers whose names start with toComplete. Cobra does not
// filter a ValidArgsFunction's result by prefix, so this does.
func completeServerNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	servers, err := fetcher.ListServers()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, name := range servers {
		if strings.HasPrefix(name, toComplete) {
			names = append(names, name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
