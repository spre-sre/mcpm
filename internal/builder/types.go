package builder

// BuildResult contains everything needed to run the server
type BuildResult struct {
	Command     string   // The executable
	Args        []string // Arguments
	EnvNeeds    []string // Environment variables required
	BuildErrors []error
}

// Manifest represents an optional mcp.json file in the repo
type Manifest struct {
	Type        string   `json:"type"`        // "node", "python", "go"
	BuildCmd    string   `json:"buildCmd"`    // Optional custom build command
	RunCmd      string   `json:"runCmd"`      // The command to start it
	Args        []string `json:"args"`        // Default args
	RequiredEnv []string `json:"requiredEnv"` // Variables to ask the user for
}
