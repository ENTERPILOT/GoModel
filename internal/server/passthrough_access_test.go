package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// Passthrough reaches the provider account directly, so a managed key needs
// passthrough access; the master key keeps it.
func TestProviderPassthroughRoute_RequiresPassthroughAccess(t *testing.T) {
	authenticator := mockAuthenticator{
		enabled:          true,
		tokenToID:        map[string]string{"sk_gom_plain": "key-plain", "sk_gom_proxy": "key-proxy"},
		tokenPassthrough: map[string]bool{"sk_gom_proxy": true},
	}
	tests := []struct {
		name       string
		token      string
		websocket  bool
		wantStatus int
	}{
		{name: "key without passthrough", token: "sk_gom_plain", wantStatus: http.StatusForbidden},
		{name: "websocket upgrade without passthrough", token: "sk_gom_plain", websocket: true, wantStatus: http.StatusForbidden},
		{name: "key with passthrough", token: "sk_gom_proxy", wantStatus: http.StatusOK},
		{name: "master key", token: "master", wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockProvider{passthroughResponse: okPassthroughResponse()}
			srv := New(mock, &Config{MasterKey: "master", Authenticator: authenticator})
			req := httptest.NewRequest(http.MethodGet, "/p/openai/models", nil)
			req.Header.Set("Authorization", "Bearer "+tt.token)
			if tt.websocket {
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
			}
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantStatus == http.StatusForbidden {
				assert.Contains(t, rec.Body.String(), "passthrough_access_denied")
				assert.Nil(t, mock.lastPassthroughReq, "a denied request must not reach the provider")
				return
			}
			assert.NotNil(t, mock.lastPassthroughReq)
		})
	}
}

// A request that names no model, such as GET /files, acts on the whole
// provider account: it is authorized for the provider instance it routes to,
// not skipped.
func TestProviderPassthroughRoute_AuthorizesProviderForModellessRequest(t *testing.T) {
	denied := core.NewInvalidRequestError(`this API key needs all of provider "anthropic_eu"`, nil).WithCode("model_access_denied")
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "provider granted", wantStatus: http.StatusOK},
		{name: "provider not granted", err: denied, wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockProvider{
				passthroughResponse: okPassthroughResponse(),
				providerTypes:       map[string]string{"anthropic_eu/claude-sonnet-4-6": "anthropic"},
				providerNames:       map[string]string{"anthropic_eu/claude-sonnet-4-6": "anthropic_eu"},
			}
			authorizer := &recordingModelAuthorizer{err: tt.err}
			srv := New(mock, &Config{ModelAuthorizer: authorizer})
			req := httptest.NewRequest(http.MethodGet, "/p/anthropic/v1/files", nil)
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assert.Equal(t, "anthropic_eu", authorizer.lastProvider)
			assert.Empty(t, authorizer.lastSelector.Model, "no model check runs for a model-less request")
			if tt.err != nil {
				assert.Contains(t, rec.Body.String(), "model_access_denied")
				assert.Nil(t, mock.lastPassthroughReq, "a denied request must not reach the provider")
				return
			}
			assert.NotNil(t, mock.lastPassthroughReq)
		})
	}
}

func okPassthroughResponse() *core.PassthroughResponse {
	return &core.PassthroughResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}
}
