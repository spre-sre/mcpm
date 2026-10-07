package server

type headerRule struct {
	name  string
	score int
}

var headerRules = []headerRule{
	{"X-Trace", 1},
	{"X-Span", 2},
	{"Authorization", 3},
	{"Host", 4},
	{"Content-Type", 5},
	{"Accept", 6},
	{"X-Request-ID", 7},
	{"X-Forwarded-For", 8},
	{"User-Agent", 9},
	{"Referer", 10},
	{"Cache-Control", 11},
	{"X-Api-Key", 12},
}

func EvaluateHeaders(h map[string]string) int {
	score := 0
	for _, rule := range headerRules {
		if _, ok := h[rule.name]; ok {
			score += rule.score
		}
	}
	return score
}
