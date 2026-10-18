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

func TestVCSLines(t *testing.T) {
	withSettings := func(kv ...string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			info := &debug.BuildInfo{Main: debug.Module{Path: "mcpm"}}
			for i := 0; i+1 < len(kv); i += 2 {
				info.Settings = append(info.Settings, debug.BuildSetting{Key: kv[i], Value: kv[i+1]})
			}
			return info, true
		}
	}
	const rev = "0123456789abcdef0123456789abcdef01234567"

	tests := []struct {
		name string
		read func() (*debug.BuildInfo, bool)
		want string
	}{
		{name: "no build info", read: func() (*debug.BuildInfo, bool) { return nil, false }, want: ""},
		{name: "nil build info", read: func() (*debug.BuildInfo, bool) { return nil, true }, want: ""},
		{name: "no settings", read: withSettings(), want: ""},
		{name: "both", read: withSettings("vcs", "git", "vcs.revision", rev, "vcs.time", "2026-01-02T03:04:05Z", "vcs.modified", "true"),
			want: "commit: " + rev + "\nbuilt: 2026-01-02T03:04:05Z\n"},
		{name: "time before revision", read: withSettings("vcs.time", "2025-07-08T09:10:11Z", "vcs.revision", rev),
			want: "commit: " + rev + "\nbuilt: 2025-07-08T09:10:11Z\n"},
		{name: "revision only", read: withSettings("vcs.revision", rev), want: "commit: " + rev + "\n"},
		{name: "time only", read: withSettings("vcs.time", "2025-07-08T09:10:11Z"), want: "built: 2025-07-08T09:10:11Z\n"},
		{name: "empty values", read: withSettings("vcs.revision", "", "vcs.time", ""), want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := vcsLines(tc.read); got != tc.want {
				t.Errorf("vcsLines() = %q, want %q", got, tc.want)
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
