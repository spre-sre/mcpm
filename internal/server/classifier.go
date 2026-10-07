package server

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
// verified
