package cmd

import "testing"

func TestParseEnvVars(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		wantKey string
		wantVal string
		wantErr bool
	}{
		{"key=value", []string{"API_KEY=secret"}, "API_KEY", "secret", false},
		{"empty value", []string{"KEY="}, "KEY", "", false},
		{"value with equals", []string{"K=a=b"}, "K", "a=b", false},
		{"underscore prefix", []string{"_FOO=bar"}, "_FOO", "bar", false},
		{"missing equals", []string{"API_KEY"}, "", "", true},
		{"empty key", []string{"=v"}, "", "", true},
		{"digit prefix", []string{"1BAD=x"}, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := parseEnvVars(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %v", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if env[tt.wantKey] != tt.wantVal {
				t.Errorf("got %q=%q, want %q=%q", tt.wantKey, env[tt.wantKey], tt.wantKey, tt.wantVal)
			}
		})
	}
}

func TestValidateTransport(t *testing.T) {
	for _, valid := range []string{"stdio", "http", "sse"} {
		if err := validateTransport(valid); err != nil {
			t.Errorf("expected %q to be valid, got error: %v", valid, err)
		}
	}
	for _, invalid := range []string{"foo", "HTTP", "SSE", ""} {
		if err := validateTransport(invalid); err == nil {
			t.Errorf("expected %q to be invalid", invalid)
		}
	}
}
