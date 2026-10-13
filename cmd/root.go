package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "mcpm",
	Short: "Model Context Protocol Manager",
	Long:  `A CLI to manage Model Context Protocol (MCP) servers for Claude Code and Gemini CLI.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
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
