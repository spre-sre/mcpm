package injector

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mcpm/internal/builder"
)

type GeminiConfig struct {
	McpServers  map[string]McpServerDef    `json:"mcpServers"`
	OtherFields map[string]json.RawMessage `json:"-"`
}

func (c *GeminiConfig) UnmarshalJSON(data []byte) error {
	type Alias GeminiConfig
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(c),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	delete(m, "mcpServers")
	c.OtherFields = m
	return nil
}

func (c GeminiConfig) MarshalJSON() ([]byte, error) {
	output := make(map[string]interface{})
	for k, v := range c.OtherFields {
		output[k] = v
	}
	output["mcpServers"] = c.McpServers
	return json.MarshalIndent(output, "", "  ")
}

func loadGeminiConfig(path string) (GeminiConfig, error) {
	var cfg GeminiConfig
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("could not read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("could not parse %s: %w", path, err)
	}
	return cfg, nil
}

func updateGeminiCLI(cwd string, result *builder.BuildResult, env map[string]string, global bool) error {
	name, nameErr := serverName(result)
	if nameErr != nil {
		return nameErr
	}

	var configDir, configPath string

	if global {
		// Global config in ~/.gemini/settings.json
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("could not get home directory: %w", err)
		}
		configDir = filepath.Join(home, ".gemini")
		configPath = filepath.Join(configDir, "settings.json")
	} else {
		// Project-level config in ./.gemini/settings.json
		configDir = filepath.Join(cwd, ".gemini")
		configPath = filepath.Join(configDir, "settings.json")
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("could not create .gemini dir: %w", err)
	}

	cfg, err := loadGeminiConfig(configPath)
	if err != nil {
		return err
	}
	if cfg.McpServers == nil {
		cfg.McpServers = make(map[string]McpServerDef)
	}

	cfg.McpServers[name] = McpServerDef{
		Type:    "stdio",
		Command: result.Command,
		Args:    result.Args,
		Env:     env,
	}

	data, err := cfg.MarshalJSON()
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}
