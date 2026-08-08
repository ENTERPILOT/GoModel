package config

import "testing"

func TestResolveMetricsEndpoint(t *testing.T) {
	tests := map[string]string{
		"":                       "/metrics",
		"/monitoring/metrics/":   "/monitoring/metrics",
		"/foo/../metrics-custom": "/metrics-custom",
		"/v1/models":             "/metrics",
		"/p/internal":            "/metrics",
	}
	for input, want := range tests {
		if got := ResolveMetricsEndpoint(input); got != want {
			t.Errorf("ResolveMetricsEndpoint(%q) = %q, want %q", input, got, want)
		}
	}
}
