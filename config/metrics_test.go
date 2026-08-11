package config

import "testing"

func TestResolveMetricsEndpoint(t *testing.T) {
	tests := map[string]string{
		"":                       "/metrics",
		"metrics":                "/metrics",
		"/monitoring/metrics/":   "/monitoring/metrics",
		"/foo/../metrics-custom": "/metrics-custom",
		"v1/models":              "/metrics",
		"../v1/models":           "/metrics",
		"/v1/models":             "/metrics",
		"/p/internal":            "/metrics",
	}
	for input, want := range tests {
		if got := ResolveMetricsEndpoint(input); got != want {
			t.Errorf("ResolveMetricsEndpoint(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveMetricsEndpointWithPprof(t *testing.T) {
	tests := map[string]struct {
		endpoint     string
		pprofEnabled bool
		want         string
	}{
		"pprof disabled":      {endpoint: "/debug/pprof", want: "/debug/pprof"},
		"pprof root conflict": {endpoint: "/debug/pprof", pprofEnabled: true, want: "/metrics"},
		"pprof child conflict": {
			endpoint: "/debug/pprof/goroutine", pprofEnabled: true, want: "/metrics",
		},
		"custom endpoint": {endpoint: "monitoring/metrics", pprofEnabled: true, want: "/monitoring/metrics"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := ResolveMetricsEndpointWithPprof(test.endpoint, test.pprofEnabled); got != test.want {
				t.Errorf("ResolveMetricsEndpointWithPprof(%q, %v) = %q, want %q", test.endpoint, test.pprofEnabled, got, test.want)
			}
		})
	}
}
