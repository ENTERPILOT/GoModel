package openai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Decisions is OpenAI's own API: the body reaches /decisions as sent, with
// the provider's key, and the answer comes back unchanged.
func TestDecisions_ForwardsToDecisionsEndpoint(t *testing.T) {
	const answer = `{"answers":[{"name":"refund","type":"predicate","probability":0.97}],"model":"gpt-6-luna","usage":{"input_tokens":40,"output_tokens":0,"total_tokens":40}}`
	server, capture := providertest.JSONServer(t, http.StatusOK, answer)
	provider := newHTTPTestProvider("sk-test", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/v1")

	const body = `{"model":"gpt-6-luna","input":"Refund me","questions":[{"type":"predicate","name":"refund","instructions":"Is this a refund request?"}]}`
	resp, err := provider.Decisions(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "ignored",
		Model:    "gpt-6-luna",
		Body:     io.NopCloser(strings.NewReader(body)),
		Headers:  http.Header{"Content-Type": {"application/json"}},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.JSONEq(t, answer, string(got))
	req := capture.Last(t)
	assert.Equal(t, "/v1/decisions", req.Path)
	assert.Equal(t, "Bearer sk-test", req.Header.Get("Authorization"))
	assert.JSONEq(t, body, string(req.Body))
}

// The OpenAI-compatible adapter other providers are built on must not claim
// the Decisions API, or the gateway would route decisions to upstreams that
// do not serve them.
func TestDecisions_OnlyOpenAIProviderServesDecisions(t *testing.T) {
	var provider core.Provider = &CompatibleProvider{}
	_, ok := provider.(core.DecisionsProvider)
	assert.False(t, ok)
}
