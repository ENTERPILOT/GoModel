package gateway

import (
	"net/http"
	"testing"

	"gomodel/internal/core"
)

func TestShouldAttemptFallback(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
		want    bool
	}{
		// Server-side and rate-limit failures always fall back.
		{"500 server error", http.StatusInternalServerError, "internal error", true},
		{"429 rate limited", http.StatusTooManyRequests, "slow down", true},

		// Model-availability phrasing falls back regardless of status code.
		{"model not found message", http.StatusBadRequest, "model gpt-9 does not exist", true},

		// 404 with availability phrasing (no literal "model") still falls back:
		// providers report retired/unavailable models this way.
		{"availability 404", http.StatusNotFound, "Claude Fable 5 is not available. Please use Opus 4.8.", true},
		{"deprecated 404", http.StatusNotFound, "this checkpoint is deprecated", true},

		// 404s without availability phrasing must NOT fall back — they are
		// genuine routing/endpoint misses, not model failures.
		{"generic endpoint 404", http.StatusNotFound, "endpoint not found", false},
		{"route 404", http.StatusNotFound, "404 page not found", false},
		{"unknown path 404", http.StatusNotFound, "no route for /v1/foo", false},

		// A plain client error without availability phrasing is not retried.
		{"plain 400", http.StatusBadRequest, "invalid request", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := core.NewProviderError("anthropic", tt.status, tt.message, nil)
			if got := ShouldAttemptFallback(err); got != tt.want {
				t.Fatalf("ShouldAttemptFallback(%d, %q) = %v, want %v", tt.status, tt.message, got, tt.want)
			}
		})
	}
}
