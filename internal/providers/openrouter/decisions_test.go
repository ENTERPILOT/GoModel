package openrouter

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

func decide(t *testing.T, provider *Provider, body string) (*core.PassthroughResponse, error) {
	t.Helper()
	return provider.Decisions(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "decisions",
		Model:    "typesafe/jev-1.13",
		Body:     io.NopCloser(strings.NewReader(body)),
		Headers:  http.Header{"Content-Type": {"application/json"}},
	})
}

// OpenRouter serves Jev at System One's path, so a Decisions request reaches
// it as System One and the answer comes back in OpenAI's shapes.
func TestDecisions_TranslatesThroughOpenRoutersSystemOneRoute(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK,
		`{"id":"gen-1","model":"typesafe/jev-1.13-20260917","answers":{"refund":{"type":"noul","noul":0.9}},"usage":{"input_tokens":20,"output_tokens":2,"cost":0.00001}}`)
	provider := newHTTPTestProvider("or-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/api/v1")

	resp, err := decide(t, provider, `{"model":"typesafe/jev-1.13","input":"Refund please","questions":[{"type":"predicate","name":"refund","instructions":"Refund?"}]}`)
	require.NoError(t, err)
	defer resp.Body.Close()

	sent := capture.Last(t)
	assert.Equal(t, "/api/v1/systemone", sent.Path)
	assert.Equal(t, "Bearer or-key", sent.Header.Get("Authorization"))
	assert.JSONEq(t, `{"model":"typesafe/jev-1.13","state":"Refund please","questions":{"refund":{"type":"noul","instructions":"Refund?"}}}`, string(sent.Body))
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(got), `{"name":"refund","type":"predicate","probability":0.9}`)
	assert.Contains(t, string(got), `"cost":0.00001`, "OpenRouter's reported charge survives translation, for usage accounting")
}

// Images are refused before the upstream: OpenRouter's System One route is
// not known to read TypeSafe's image assets.
func TestDecisions_OpenRouterRefusesImages(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{}`)
	provider := newHTTPTestProvider("or-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/api/v1")

	_, err := decide(t, provider, `{"model":"typesafe/jev-1.13","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]}],"questions":[{"type":"predicate","instructions":"?"}]}`)
	providertest.AssertUnsupported(t, err)
	assert.Contains(t, err.Error(), "read text only")
	assert.Zero(t, capture.Count())
	assert.False(t, provider.DecisionsReadImages())
}
