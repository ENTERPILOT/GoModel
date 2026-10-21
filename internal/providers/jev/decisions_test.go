package jev

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
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

var (
	_ core.DecisionsProvider   = (*Provider)(nil)
	_ core.DecisionImageReader = (*Provider)(nil)
)

func decide(t *testing.T, provider *Provider, body string) (*core.PassthroughResponse, error) {
	t.Helper()
	return provider.Decisions(context.Background(), &core.PassthroughRequest{
		Method:       http.MethodPost,
		Endpoint:     "decisions",
		Model:        "jev-latest",
		Body:         io.NopCloser(strings.NewReader(body)),
		Headers:      http.Header{"Content-Type": {"application/json"}},
		ProviderName: "jev",
	})
}

// A Decisions request reaches the provider's /v1/systemone route with its
// key, translated; the translation itself is covered in package systemone.
func TestDecisions_SendsSystemOneUpstream(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"model":"jev-1.13.0","answers":{"a":{"type":"noul","noul":0.4}},"usage":{"input_tokens":5,"output_tokens":1}}`)
	provider := newTestProvider("ts-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := decide(t, provider, `{"model":"jev-latest","input":"x","questions":[{"type":"predicate","name":"a","instructions":"A?"}]}`)
	require.NoError(t, err)
	defer resp.Body.Close()

	sent := capture.Last(t)
	assert.Equal(t, "/v1/systemone", sent.Path)
	assert.Equal(t, "Bearer ts-key", sent.Header.Get("Authorization"))
	assert.JSONEq(t, `{"model":"jev-latest","state":"x","questions":{"a":{"type":"noul","instructions":"A?"}}}`, string(sent.Body))
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(got), `{"name":"a","type":"predicate","probability":0.4}`)
}

// Only TypeSafe's hosted API reads images. A jev provider pointing anywhere
// else is a Kev server, and refuses images itself, without the catalog:
// models configured without listing carry no vision capability for the
// gateway to check.
func TestDecisions_KevServersRefuseImages(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{}`)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	_, err := decide(t, provider, `{"model":"kev-latest","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]}],"questions":[{"type":"predicate","instructions":"?"}]}`)
	providertest.AssertUnsupported(t, err)
	assert.Contains(t, err.Error(), "Kev server, which reads text only")
	assert.Zero(t, capture.Count())
}

func TestHostedTypeSafe(t *testing.T) {
	for base, want := range map[string]bool{
		"https://api.typesafe.ai":         true,
		"https://api.typesafe.ai/v1":      true,
		"https://API.TypeSafe.AI":         true,
		"http://api.typesafe.ai":          false,
		"https://typesafe.ai":             false,
		"https://docs.typesafe.ai":        false,
		"https://api.typesafe.ai:8443":    false,
		"http://localhost:8009":           false,
		"https://me--kev-api.modal.run":   false,
		"https://typesafe.ai.example.com": false,
		"https://nottypesafe.ai":          false,
	} {
		assert.Equal(t, want, hostedTypeSafe(base), base)
	}
	provider := New(providers.ProviderConfig{}, providers.ProviderOptions{}).(*Provider)
	assert.True(t, provider.hosted.Load(), "the default base URL is the hosted API")
	provider.SetBaseURL("http://localhost:8009")
	assert.False(t, provider.hosted.Load())
}
