package server

import "testing"

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "/"},
		{" ", "/"},
		{"/", "/"},
		{"//", "/"},
		{"///", "/"},
		{" // ", "/"},
		{"/api/", "/api"},
		{"API//", "/api"},
		{"api", "/api"},
		{" /Api/V1/ ", "/api/v1"},
		{"/a//b/", "/a//b"},
	}
	for _, tt := range tests {
		got := NormalizeEndpoint(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeEndpoint(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
