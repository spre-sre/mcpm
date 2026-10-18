package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/cobra"
)

func TestCompleteServerNames(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, dir string)
		args       []string
		toComplete string
		want       []string
	}{
		{
			name:  "all server directories, no files",
			setup: makeServers("alpha", "beta"),
			want:  []string{"alpha", "beta"},
		},
		{
			name:       "case-sensitive prefix",
			setup:      makeServers("alpha", "alpine", "beta", "Alps"),
			toComplete: "alp",
			want:       []string{"alpha", "alpine"},
		},
		{
			name:       "no match",
			setup:      makeServers("alpha", "beta"),
			toComplete: "z",
		},
		{
			name:  "name already given",
			setup: makeServers("alpha", "beta"),
			args:  []string{"alpha"},
		},
		{
			name:  "no servers directory",
			setup: func(t *testing.T, dir string) {},
		},
		{
			name: "servers path is a file",
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, ".mcp"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".mcp", "servers"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := chdirTemp(t)
			tc.setup(t, dir)
			got, directive := completeServerNames(nil, tc.args, tc.toComplete)
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("names = %q, want %q", got, tc.want)
			}
			if directive != cobra.ShellCompDirectiveNoFileComp {
				t.Errorf("directive = %d, want %d", directive, cobra.ShellCompDirectiveNoFileComp)
			}
		})
	}
}

// makeServers returns a setup that creates the server directories names and
// the regular file notes.txt under dir/.mcp/servers.
func makeServers(names ...string) func(t *testing.T, dir string) {
	return func(t *testing.T, dir string) {
		servers := filepath.Join(dir, ".mcp", "servers")
		for _, name := range names {
			if err := os.MkdirAll(filepath.Join(servers, name), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(servers, "notes.txt"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
