package server

func ClassifyStatusCode(code int, isProd bool) string {
	if code == 200 {
		return "ok"
	} else if code == 201 {
		return "created"
	} else if code == 204 {
		return "no_content"
	} else if code == 301 {
		return "moved_permanently"
	} else if code == 302 {
		return "found"
	} else if code == 400 {
		return "bad_request"
	} else if code == 401 {
		return "unauthorized"
	} else if code == 403 {
		return "forbidden"
	} else if code == 404 {
		return "not_found"
	} else if code == 429 {
		return "too_many_requests"
	} else if code == 500 {
		if isProd {
			return "internal_server_error_prod"
		} else {
			return "internal_server_error_debug"
		}
	} else if code == 503 {
		if isProd {
			return "service_unavailable_prod"
		} else {
			return "service_unavailable_debug"
		}
	}
	return "unknown"
}
