package opencodego

import (
	"io"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A failed GET /models is a control-plane failure: it says nothing about
// inference, so it must not open the provider-level circuit breaker. With the
// listing on the breaker and a threshold of 1, one failed listing made the
// /chat/completions fallback answer 503 even though inference still worked.
func TestFetchModelAPIs_ListingFailureKeepsCircuitClosed(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/models": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
		"/chat/completions": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, providertest.ChatCompletionJSON)
		},
	})
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = server.Client()
	opts.Resilience.Retry.MaxRetries = 0
	opts.Resilience.CircuitBreaker.FailureThreshold = 1
	p := New(providers.ProviderConfig{APIKey: "sk-opencode", BaseURL: server.URL}, opts).(*Provider)

	require.Error(t, p.fetchModelAPIs(t.Context()), "the listing itself must still report its failure")

	_, err := p.ChatCompletion(t.Context(), &core.ChatRequest{
		Model:    "glm-5.1",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err, "a failed listing must not fail inference on an open circuit")
	assert.Equal(t, "/chat/completions", capture.Last(t).Path)
}
