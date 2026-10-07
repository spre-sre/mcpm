package server

import "strings"

func NormalizeEndpoint(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.ToLower(s)
	if s == "" || s == "/" {
		return "/"
	}
	if s[0] != '/' {
		s = "/" + s
	}
	s = strings.TrimRight(s, "/")
	return s
}
