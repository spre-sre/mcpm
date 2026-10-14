package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "mcpm",
	Short: "Model Context Protocol Manager",
	Long:  `A CLI to manage Model Context Protocol (MCP) servers for Claude Code and Gemini CLI.`,

	SilenceErrors: true,
}

func Execute() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the root command with args and returns the process exit code.
// Cobra's help goes to stdout. After a failure nothing goes to stdout: the
// usage (unless the command set SilenceUsage) and one error line go to stderr.
func run(args []string, stdout, stderr io.Writer) int {
	rootCmd.SetArgs(args)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.SilenceUsage = true
	defer func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SilenceUsage = false
	}()
	cmd, err := rootCmd.ExecuteC()
	if err != nil {
		if cmd != nil && !cmd.SilenceUsage {
			fmt.Fprint(stderr, cmd.UsageString())
		}
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.mcpm.yaml)")
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".mcpm")
	}

	viper.AutomaticEnv()
	if err := readConfig(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// readConfig loads the config file. A missing default file is normal, so
// only other errors (malformed file, bad --config path) are returned.
func readConfig() error {
	err := viper.ReadInConfig()
	var notFound viper.ConfigFileNotFoundError
	if errors.As(err, &notFound) {
		return nil
	}
	return err
}
