package gateway

import (
	"net/http"
	"testing"

	"gomodel/internal/core"
)

func TestShouldAttemptFallbackOnProviderModelAvailability404(t *testing.T) {
	err := core.NewProviderError("anthropic", http.StatusNotFound, "Claude Fable 5 is not available. Please use Opus 4.8.", nil)

	if !ShouldAttemptFallback(err) {
		t.Fatal("ShouldAttemptFallback() = false, want true for provider model availability 404")
	}
}

func TestShouldAttemptFallbackKeepsGenericEndpoint404NonFallback(t *testing.T) {
	err := core.NewProviderError("anthropic", http.StatusNotFound, "endpoint not found", nil)

	if ShouldAttemptFallback(err) {
		t.Fatal("ShouldAttemptFallback() = true, want false for generic endpoint 404")
	}
}
