package openai

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestWorkloadIdentityAuthenticatesWithExchangedBearerToken(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/oauth/token": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"minted-access-token","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`)
		},
		"/v1/chat/completions": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		},
	})

	factory := providers.NewProviderFactory()
	factory.Add(Registration)
	provider, err := factory.Create(providers.ProviderConfig{
		Name:    "openai",
		Type:    "openai",
		BaseURL: server.URL + "/v1",
		WorkloadIdentity: config.WorkloadIdentityConfig{
			IdentityToken:      "platform-jwt",
			TokenEndpoint:      server.URL + "/oauth/token",
			IdentityProviderID: "idp_1",
			ServiceAccountID:   "sa_1",
		},
	})
	require.NoError(t, err)

	_, err = provider.ChatCompletion(t.Context(), &core.ChatRequest{
		Model:    "gpt-4o-mini",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	requests := capture.All()
	require.Len(t, requests, 2)
	assert.Equal(t, map[string]any{
		"grant_type":           "urn:ietf:params:oauth:grant-type:token-exchange",
		"subject_token_type":   "urn:ietf:params:oauth:token-type:jwt",
		"subject_token":        "platform-jwt",
		"identity_provider_id": "idp_1",
		"service_account_id":   "sa_1",
	}, requests[0].JSON(t))
	assert.Equal(t, "/v1/chat/completions", requests[1].Path)
	assert.Equal(t, "Bearer minted-access-token", requests[1].Header.Get("Authorization"))
}
