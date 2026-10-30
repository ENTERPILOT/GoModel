package anthropic

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
		"/v1/oauth/token": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"sk-ant-oat01-minted","token_type":"Bearer","expires_in":600}`)
		},
		"/v1/messages": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		},
	})

	factory := providers.NewProviderFactory()
	factory.Add(Registration)
	provider, err := factory.Create(providers.ProviderConfig{
		Name:    "anthropic",
		Type:    "anthropic",
		BaseURL: server.URL + "/v1",
		WorkloadIdentity: config.WorkloadIdentityConfig{
			IdentityToken:    "platform-jwt",
			FederationRuleID: "fdrl_1",
			OrganizationID:   "org-uuid",
			ServiceAccountID: "svac_1",
			WorkspaceID:      "wrkspc_1",
		},
	})
	require.NoError(t, err)

	for range 2 {
		_, err = provider.ChatCompletion(t.Context(), &core.ChatRequest{
			Model:    "claude-sonnet-4-6",
			Messages: []core.Message{{Role: "user", Content: "hi"}},
		})
		require.NoError(t, err)
	}

	requests := capture.All()
	require.Len(t, requests, 3, "one exchange, then two messages on the cached token")
	exchange := requests[0]
	assert.Equal(t, "/v1/oauth/token", exchange.Path)
	assert.Equal(t, map[string]any{
		"grant_type":         "urn:ietf:params:oauth:grant-type:jwt-bearer",
		"assertion":          "platform-jwt",
		"federation_rule_id": "fdrl_1",
		"organization_id":    "org-uuid",
		"service_account_id": "svac_1",
		"workspace_id":       "wrkspc_1",
	}, exchange.JSON(t))

	for _, message := range requests[1:] {
		assert.Equal(t, "/v1/messages", message.Path)
		assert.Equal(t, "Bearer sk-ant-oat01-minted", message.Header.Get("Authorization"))
		assert.Empty(t, message.Header.Get("x-api-key"))
		assert.Empty(t, message.Header.Get(anthropicBetaHeader), "federated tokens do not take the subscription oauth beta")
		assert.NotEmpty(t, message.Header.Get("anthropic-version"))
	}
}
