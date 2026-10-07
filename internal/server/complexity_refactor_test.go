package server

import "testing"

// These tests pin the behavior of ClassifyStatusCode and EvaluateRiskScore
// over their whole practical input domain, so a refactor cannot change it.

func TestClassifyStatusCodeAllCodes(t *testing.T) {
	named := map[int]string{
		200: "ok", 201: "created", 204: "no_content", 301: "moved_permanently", 302: "found",
		400: "bad_request", 401: "unauthorized", 403: "forbidden", 404: "not_found", 429: "too_many_requests",
	}
	byEnvironment := map[int][2]string{
		500: {"internal_server_error_prod", "internal_server_error_debug"},
		503: {"service_unavailable_prod", "service_unavailable_debug"},
	}
	for code := -1; code <= 1000; code++ {
		for _, isProd := range []bool{true, false} {
			want := "unknown"
			if name, ok := named[code]; ok {
				want = name
			}
			if names, ok := byEnvironment[code]; ok {
				want = names[1]
				if isProd {
					want = names[0]
				}
			}
			if got := ClassifyStatusCode(code, isProd); got != want {
				t.Errorf("ClassifyStatusCode(%d, %v) = %q, want %q", code, isProd, got, want)
			}
		}
	}
}

func TestEvaluateRiskScoreAllCombinations(t *testing.T) {
	for mask := 0; mask < 1<<12; mask++ {
		var flags [12]bool
		want := 0
		for bit := range flags {
			flags[bit] = mask&(1<<bit) != 0
			if flags[bit] {
				want += bit + 1 // flag A weighs 1, flag L weighs 12
			}
		}
		got := EvaluateRiskScore(flags[0], flags[1], flags[2], flags[3], flags[4], flags[5],
			flags[6], flags[7], flags[8], flags[9], flags[10], flags[11])
		if got != want {
			t.Fatalf("EvaluateRiskScore(%v) = %d, want %d", flags, got, want)
		}
	}
}
