package server

import (
	"path/filepath"
	"strings"
)
// statusNames maps a status code to its class name.
var statusNames = map[int]string{
	200: "ok",
	201: "created",
	204: "no_content",
	301: "moved_permanently",
	302: "found",
	400: "bad_request",
	401: "unauthorized",
	403: "forbidden",
	404: "not_found",
	429: "too_many_requests",
}

// environmentStatusNames maps a server-error code to its production and debug class names.
var environmentStatusNames = map[int]struct{ prod, debug string }{
	500: {"internal_server_error_prod", "internal_server_error_debug"},
	503: {"service_unavailable_prod", "service_unavailable_debug"},
}

func ClassifyStatusCode(code int, isProd bool) string {
	if names, ok := environmentStatusNames[code]; ok {
		if isProd {
			return names.prod
		}
		return names.debug
	}
	if name, ok := statusNames[code]; ok {
		return name
	}
	return "unknown"
}

// launchRuntimes maps the base name of a launch command to the runtime that
// starts the server.
var launchRuntimes = map[string]string{
	"node":    "node",
	"npx":     "node",
	"npm":     "node",
	"bun":     "node",
	"python":  "python",
	"python3": "python",
	"uv":      "python",
	"uvx":     "python",
	"go":      "go",
	"docker":  "docker",
	"podman":  "docker",
}

// ClassifyLaunchCommand reports how a registered server is started: the
// runtime named by command ("node", "python", "go", "docker"), "binary" for
// any other executable, or "unknown" for an empty command. A directory
// prefix and a Windows ".exe" suffix are ignored.
func ClassifyLaunchCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return "unknown"
	}
	base := strings.ToLower(filepath.Base(command))
	base = strings.TrimSuffix(base, ".exe")
	if runtime, ok := launchRuntimes[base]; ok {
		return runtime
	}
	return "binary"
}
