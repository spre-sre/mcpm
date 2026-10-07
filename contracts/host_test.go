package contracts

import (
	"net"
	"strings"
	"testing"
	"testing/quick"

	"mcpm/internal/server"
)

// TestInvariantStripHostPort verifies StripHostPort against standard library behavior.
func TestInvariantStripHostPort(t *testing.T) {
	// 1. Concrete edge cases that LLMs commonly miss
	concreteTests := []struct {
		input    string
		expected string
	}{
		{"localhost:8080", "localhost"},
		{"[::1]:8080", "::1"}, // Brackets must be stripped to match net.SplitHostPort
		{"[::1]", "::1"},      // Bracketed IPv6 without port
		{"::1", "::1"},        // Bare IPv6 without port must NOT cut at the last colon
		{"127.0.0.1:9000", "127.0.0.1"},
		{"example.com", "example.com"},
		{"", ""},
	}

	for _, tc := range concreteTests {
		got := server.StripHostPort(tc.input)
		if got != tc.expected {
			t.Fatalf("FAILED invariant for input %q: expected %q, got %q", tc.input, tc.expected, got)
		}
	}

	// 2. Randomized property fuzzing using testing/quick
	f := func(host string, port uint16) bool {
		// Clean host to valid DNS/IP tokens
		if strings.ContainsAny(host, ":/ \t\n") || host == "" {
			return true
		}
		raw := net.JoinHostPort(host, string(rune(port)))
		expected, _, err := net.SplitHostPort(raw)
		if err != nil {
			return true
		}
		got := server.StripHostPort(raw)
		return got == expected
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatalf("Property violation: %v", err)
	}
}
