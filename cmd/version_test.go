package cmd

import (
	"bytes"
	"runtime/debug"
	"testing"

	"github.com/spf13/viper"
)

func TestResolveVersion(t *testing.T) {
	withMain := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Path: "mcpm", Version: v}}, true
		}
	}
	noInfo := func() (*debug.BuildInfo, bool) { return nil, false }

	tests := []struct {
		name    string
		ldflags string
		read    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{name: "build info version", read: withMain("v1.2.3"), want: "v1.2.3"},
		{name: "devel build info", read: withMain("(devel)"), want: "dev"},
		{name: "empty build info version", read: withMain(""), want: "dev"},
		{name: "no build info", read: noInfo, want: "dev"},
		{name: "ldflags over build info", ldflags: "v9.8.7", read: withMain("v1.2.3"), want: "v9.8.7"},
		{name: "ldflags over devel", ldflags: "v9.8.7", read: withMain("(devel)"), want: "v9.8.7"},
		{name: "ldflags over empty", ldflags: "v9.8.7", read: withMain(""), want: "v9.8.7"},
		{name: "ldflags without build info", ldflags: "custom-1", read: noInfo, want: "custom-1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersion(tc.ldflags, tc.read); got != tc.want {
				t.Errorf("resolveVersion(%q) = %q, want %q", tc.ldflags, got, tc.want)
			}
		})
	}
}

func TestRunVersionMatchesFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldCfg := cfgFile
	cfgFile = ""
	viper.Reset()
	t.Cleanup(func() {
		cfgFile = oldCfg
		viper.Reset()
		if f := rootCmd.Flags().Lookup("version"); f != nil {
			_ = f.Value.Set("false")
			f.Changed = false
		}
	})

	want := "mcpm version " + rootCmd.Version + "\n"
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("run(%v) exit code = %d, want 0", args, code)
		}
		if stdout.String() != want {
			t.Errorf("run(%v) stdout = %q, want %q", args, stdout.String(), want)
		}
		if stderr.Len() != 0 {
			t.Errorf("run(%v) stderr = %q, want empty", args, stderr.String())
		}
	}
}
