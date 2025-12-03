package builder

import (
	"fmt"
	"path/filepath"
	"runtime"
)

func buildGo(path string) (*BuildResult, error) {
	binName := "mcp-server"
	if runtime.GOOS == "windows" {
		binName = "mcp-server.exe"
	}

	cmd := fmt.Sprintf("go build -o %s .", binName)
	if err := runShellCmd(path, cmd); err != nil {
		return nil, err
	}

	return &BuildResult{
		Command:  filepath.Join(path, binName),
		Args:     []string{},
		EnvNeeds: []string{},
	}, nil
}
