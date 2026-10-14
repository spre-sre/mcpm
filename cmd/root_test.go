package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestRunArgumentErrorPrintedOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldCfg := cfgFile
	cfgFile = ""
	viper.Reset()
	t.Cleanup(func() {
		cfgFile = oldCfg
		viper.Reset()
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"remove"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	const msg = "accepts 1 arg(s), received 0"
	if n := strings.Count(stderr.String(), msg); n != 1 {
		t.Errorf("stderr has %d occurrences of %q, want 1:\n%s", n, msg, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("stderr lacks usage:\n%s", stderr.String())
	}
}

func TestRunRootErrorPrintsRootUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldCfg := cfgFile
	cfgFile = ""
	viper.Reset()
	t.Cleanup(func() {
		cfgFile = oldCfg
		viper.Reset()
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"--bogus"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	out := stderr.String()
	for _, s := range []string{"Usage:", "Usage:\n  mcpm [command]\n", "Error: unknown flag: --bogus"} {
		if n := strings.Count(out, s); n != 1 {
			t.Errorf("stderr has %d occurrences of %q, want 1:\n%s", n, s, out)
		}
	}
	if strings.Index(out, "Usage:") > strings.Index(out, "Error: ") {
		t.Errorf("usage should come before the error line:\n%s", out)
	}
}

func TestRunRestoresSilenceUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldCfg := cfgFile
	oldSilence := rootCmd.SilenceUsage
	cfgFile = ""
	viper.Reset()
	t.Cleanup(func() {
		cfgFile = oldCfg
		rootCmd.SilenceUsage = oldSilence
		viper.Reset()
	})

	rootCmd.SilenceUsage = true
	var stdout, stderr bytes.Buffer
	run([]string{"--bogus"}, &stdout, &stderr)
	if !rootCmd.SilenceUsage {
		t.Error("rootCmd.SilenceUsage = false after run, want true")
	}
}

func TestRunHelpGoesToStdout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldCfg := cfgFile
	cfgFile = ""
	viper.Reset()
	list, _, err := rootCmd.Find([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cfgFile = oldCfg
		viper.Reset()
		if f := list.Flags().Lookup("help"); f != nil {
			_ = f.Value.Set("false")
			f.Changed = false
		}
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"list", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Errorf("stdout lacks usage:\n%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestReadConfig(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string)
		wantErr bool
	}{
		{
			name: "no config file in search path",
			setup: func(t *testing.T, dir string) {
				viper.AddConfigPath(dir)
				viper.SetConfigType("yaml")
				viper.SetConfigName(".mcpm")
			},
		},
		{
			name: "malformed explicit config",
			setup: func(t *testing.T, dir string) {
				path := filepath.Join(dir, "bad.yaml")
				if err := os.WriteFile(path, []byte("key: [unclosed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				viper.SetConfigFile(path)
			},
			wantErr: true,
		},
		{
			name: "missing explicit config",
			setup: func(t *testing.T, dir string) {
				viper.SetConfigFile(filepath.Join(dir, "missing.yaml"))
			},
			wantErr: true,
		},
		{
			name: "valid explicit config",
			setup: func(t *testing.T, dir string) {
				path := filepath.Join(dir, "good.yaml")
				if err := os.WriteFile(path, []byte("key: value\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				viper.SetConfigFile(path)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)
			tc.setup(t, t.TempDir())
			err := readConfig()
			if (err != nil) != tc.wantErr {
				t.Fatalf("readConfig() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
