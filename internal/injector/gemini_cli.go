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

// geminiSettingsPath returns ~/.gemini/settings.json for global and
// <cwd>/.gemini/settings.json for project-level registration.
func geminiSettingsPath(cwd string, global bool) (string, error) {
	base := cwd
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not get home directory: %w", err)
		}
		base = home
	}
	return filepath.Join(base, ".gemini", "settings.json"), nil
}

// lookupGeminiCLI returns the Gemini CLI registration of name. A missing
// settings file means the server is not registered.
func lookupGeminiCLI(cwd, name string, global bool) (McpServerDef, bool, error) {
	configPath, err := geminiSettingsPath(cwd, global)
	if err != nil {
		return McpServerDef{}, false, err
	}
	cfg, err := loadGeminiConfig(configPath)
	if err != nil {
		return McpServerDef{}, false, err
	}
	def, ok := cfg.McpServers[name]
	return def, ok, nil
}

// GeminiCLIRegistrations returns the Gemini CLI registrations of name in
// <cwd>/.gemini/settings.json (local) and ~/.gemini/settings.json (global).
func GeminiCLIRegistrations(cwd, name string) ([]Registration, error) {
	return registrationsIn(TargetGeminiCLI, cwd, name, lookupGeminiCLI)
}

func updateGeminiCLI(cwd string, result *builder.BuildResult, env map[string]string, global bool) error {
	name, nameErr := serverName(result)
	if nameErr != nil {
		return nameErr
	}

	configPath, err := geminiSettingsPath(cwd, global)
	if err != nil {
		return err
	}
	configDir := filepath.Dir(configPath)

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
	return writeSettingsFile(configPath, data, len(env) > 0)
}

func writeSettingsFile(path string, data []byte, hasSecrets bool) error {
	if hasSecrets {
		if err := os.WriteFile(path, data, 0600); err != nil {
			return err
		}
		return os.Chmod(path, 0600)
	}
	return os.WriteFile(path, data, 0644)
}
