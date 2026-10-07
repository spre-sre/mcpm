package server

import "strings"

func StripHostPort(host string) string {
	if host == "" {
		return ""
	}

	if strings.HasPrefix(host, "[") {
		if i := strings.LastIndex(host, "]"); i != -1 {
			return host[:i+1]
		}
		return host
	}

	if i := strings.LastIndex(host, ":"); i != -1 {
		return host[:i]
	}

	return host
}
