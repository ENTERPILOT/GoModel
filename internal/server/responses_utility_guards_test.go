package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

var responsesUtilityPaths = []string{"/v1/responses/input_tokens", "/v1/responses/compact"}

func utilityTestProvider() *mockProvider {
	return &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes:   map[string]string{"gpt-5-mini": "mock"},
	}
}

func postUtility(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// The utility endpoints forward the prompt upstream, so a caller denied the
// model is refused before anything reaches the provider.
func TestResponsesUtility_EnforcesModelAccess(t *testing.T) {
	for _, path := range responsesUtilityPaths {
		t.Run(path, func(t *testing.T) {
			provider := utilityTestProvider()
			authorizer := &recordingModelAuthorizer{err: core.NewInvalidRequestError("model access denied", nil)}
			srv := New(provider, &Config{ModelAuthorizer: authorizer})

			rec := postUtility(t, srv, path, `{"model":"gpt-5-mini","input":"hello"}`)

			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, "gpt-5-mini", authorizer.lastSelector.Model)
			assert.Empty(t, provider.capturedResponseUtilityReqs)
		})
	}
}

// Prompt guardrails see the input the utility endpoints forward, as they do
// for /v1/responses, so a redacting guardrail cannot be bypassed by counting
// or compacting instead of generating.
func TestResponsesUtility_AppliesPromptGuardrails(t *testing.T) {
	for _, path := range responsesUtilityPaths {
		t.Run(path, func(t *testing.T) {
			provider := utilityTestProvider()
			patcher := &redactingPatcher{}
			srv := New(provider, &Config{TranslatedRequestPatcher: patcher})

			rec := postUtility(t, srv, path, `{"model":"gpt-5-mini","input":"my pet is a zebra"}`)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Len(t, patcher.seen, 1)
			require.Len(t, provider.capturedResponseUtilityReqs, 1)
			forwarded, err := json.Marshal(provider.capturedResponseUtilityReqs[0].Input)
			require.NoError(t, err)
			assert.NotContains(t, string(forwarded), "zebra")
			assert.Contains(t, string(forwarded), "[animal]")
		})
	}
}
