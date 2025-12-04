# mcpm - MCP Package Manager

A CLI tool to install and manage [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) servers for Claude Code and Gemini CLI.

## Features

- **Easy Installation** - Install MCP servers with a single command
- **Multi-Platform Support** - Works with GitHub and GitLab
- **Auto-Detection** - Automatically detects project type (Node.js, Python, Go)
- **Build Automation** - Handles dependencies and build steps automatically
- **Multi-Client** - Register servers with Claude Code and/or Gemini CLI

## Installation

### From Source

```bash
git clone https://github.com/spre-sre/mcpm.git
cd mcpm
go build -o mcpm .
```

### Move to PATH (optional)

```bash
sudo mv mcpm /usr/local/bin/
```

## Usage

### Install an MCP Server from Repository

```bash
# From GitHub
mcpm install @modelcontextprotocol/server-filesystem

# From GitLab
mcpm install gl:@gitlab-org/my-mcp-server

# From direct URL
mcpm install https://github.com/user/repo.git
```

### Add an Existing MCP Server

For HTTP endpoints or already installed servers:

```bash
# Add HTTP server
mcpm add sentry https://mcp.sentry.dev/mcp --transport http

# Add SSE server
mcpm add slack https://mcp.slack.com/sse --transport sse

# Add local stdio server
mcpm add myserver /usr/local/bin/my-mcp-server

# Add with environment variables
mcpm add myserver node /path/to/server.js -e API_KEY=xxx

# Add only to Claude Code
mcpm add myserver /path/to/server --claude

# Add only to Gemini CLI
mcpm add myserver /path/to/server --gemini
```

### URL Schemes

| Scheme | Description | Example |
|--------|-------------|---------|
| `@org/repo` | GitHub (default) | `@anthropics/mcp-server` |
| `gl:@org/repo` | GitLab | `gl:@gitlab-org/server` |
| `https://...` | Direct URL | Any git URL |

## How It Works

1. **Clone** - Fetches the repository to `.mcp/servers/<name>/`
2. **Detect** - Identifies project type based on config files:
   - `package.json` → Node.js
   - `requirements.txt` or `pyproject.toml` → Python
   - `go.mod` → Go
   - `mcp.json` → Custom manifest
3. **Build** - Installs dependencies and builds the project
4. **Register** - Adds the server to your chosen clients (Claude Code / Gemini CLI)

## Supported Project Types

### Node.js
- Detects package manager (npm, yarn, pnpm)
- Falls back to npm if preferred manager unavailable
- Runs `install` and `build` scripts
- Supports monorepo structures

### Python
- Creates virtual environment (`.venv`)
- Installs from `requirements.txt` or `pyproject.toml`
- Auto-detects entry point (`main.py`, `server.py`, etc.)

### Go
- Runs `go build`
- Outputs binary as `mcp-server`

### Custom (mcp.json)

Create an `mcp.json` in your repo root:

```json
{
  "type": "node",
  "buildCmd": "npm run build",
  "runCmd": "node",
  "args": ["dist/index.js"],
  "requiredEnv": ["API_KEY", "SECRET"]
}
```

## Configuration

### Claude Code

Servers are registered using `claude mcp add` command, which stores configuration in `~/.claude.json` under the project path.

### Gemini CLI

Servers are registered in `.gemini/settings.json` in the current directory:

```json
{
  "mcpServers": {
    "server-name": {
      "type": "stdio",
      "command": "node",
      "args": ["/path/to/server/index.js"]
    }
  }
}
```

## Requirements

- Go 1.23+ (for building from source)
- Git
- Node.js/npm (for Node.js servers)
- Python 3 (for Python servers)
- Claude Code CLI (for Claude Code integration)

## Project Structure

```
mcpm/
├── cmd/
│   ├── root.go          # Root command setup
│   └── install.go       # Install command
├── internal/
│   ├── fetcher/
│   │   └── git.go       # Git clone functionality
│   ├── builder/
│   │   ├── builder.go   # Main build logic
│   │   ├── node.go      # Node.js builder
│   │   ├── python.go    # Python builder
│   │   ├── golang.go    # Go builder
│   │   ├── shell.go     # Shell command helper
│   │   └── types.go     # Type definitions
│   ├── injector/
│   │   ├── injector.go  # Unified injector
│   │   ├── claude_code.go
│   │   └── gemini_cli.go
│   └── tui/
│       ├── installer.go # Main TUI model
│       ├── commands.go  # Tea commands
│       ├── helpers.go   # Input handlers
│       └── styles.go    # Lipgloss styles
├── main.go
├── go.mod
└── go.sum
```

## License

MIT License - see [LICENSE](LICENSE) for details.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.
