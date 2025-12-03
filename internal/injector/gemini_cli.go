package injector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	json.Unmarshal(data, &m)
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

func updateGeminiCLI(cwd string, result *builder.BuildResult, env map[string]string) error {
	configDir := filepath.Join(cwd, ".gemini")
	configPath := filepath.Join(configDir, "settings.json")

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("could not create .gemini dir: %w", err)
	}

	var cfg GeminiConfig
	if data, err := os.ReadFile(configPath); err == nil {
		json.Unmarshal(data, &cfg)
	}
	if cfg.McpServers == nil {
		cfg.McpServers = make(map[string]McpServerDef)
	}

	// Extract server name from path
	// Look for .mcp/servers/<name> pattern
	name := "mcp-server"
	if len(result.Args) > 0 {
		path := result.Args[0]
		// Find "servers" in path and get the next component
		parts := strings.Split(path, string(filepath.Separator))
		for i, part := range parts {
			if part == "servers" && i+1 < len(parts) {
				name = parts[i+1]
				break
			}
		}
	}
	if name == "" || name == "." {
		name = filepath.Base(result.Command)
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
