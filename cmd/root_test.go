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
	if strings.Contains(stdout.String(), msg) {
		t.Errorf("stdout contains %q:\n%s", msg, stdout.String())
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
