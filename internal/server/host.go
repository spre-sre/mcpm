package server

import "strings"

func StripHostPort(host string) string {
	if host == "" {
		return ""
	}

	if strings.HasPrefix(host, "[") {
		if i := strings.IndexByte(host, ']'); i != -1 {
			return host[1:i]
		}
		return host
	}

	if i := strings.IndexByte(host, ':'); i != -1 && i == strings.LastIndex(host, ":") {
		return host[:i]
	}

	return host
}
