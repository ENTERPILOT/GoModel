package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func fidelityResponsesProvider() *mockProvider {
	return &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes:   map[string]string{"gpt-5-mini": "mock"},
		responsesResponse: &core.ResponsesResponse{
			ID:        "resp_fidelity",
			Object:    "response",
			CreatedAt: 1000,
			Model:     "gpt-5-mini",
			Status:    "completed",
			Output:    []core.ResponsesOutputItem{},
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"instructions":        json.RawMessage(`"be terse"`),
				"metadata":            json.RawMessage(`{"k":"v"}`),
				"tool_choice":         json.RawMessage(`"auto"`),
				"parallel_tool_calls": json.RawMessage(`false`),
				"truncation":          json.RawMessage(`"auto"`),
			}),
		},
	}
}

// The non-streaming answer carries the Response members the provider returned,
// so metadata written on the request can be read back — from the create call
// and from the stored response.
func TestResponses_NonStreamingKeepsProviderResponseMembers(t *testing.T) {
	srv := New(fidelityResponsesProvider(), nil)

	body := `{"model":"gpt-5-mini","input":"hi","metadata":{"k":"v"},"instructions":"be terse",` +
		`"parallel_tool_calls":false,"truncation":"auto"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "create: %s", rec.Body.String())
	srv.handler.drainSnapshotWrites()

	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/v1/responses/resp_fidelity", nil))
	require.Equal(t, http.StatusOK, getRec.Code, "get: %s", getRec.Body.String())

	want := map[string]string{
		"instructions":        `"be terse"`,
		"metadata":            `{"k":"v"}`,
		"tool_choice":         `"auto"`,
		"parallel_tool_calls": `false`,
		"truncation":          `"auto"`,
	}
	for name, payload := range map[string][]byte{"create": rec.Body.Bytes(), "retrieve": getRec.Body.Bytes()} {
		var got map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(payload, &got), "decode %s response", name)
		for member, value := range want {
			assert.Equal(t, value, string(got[member]), "%s response %q (%s)", name, member, payload)
		}
	}
}

// A request with no input is answered locally with OpenAI's
// missing-required-parameter error instead of being forwarded as {}.
func TestResponses_MissingInputRejectedLocally(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "missing input", body: `{"model":"gpt-5-mini"}`, wantStatus: http.StatusBadRequest},
		{name: "null input", body: `{"model":"gpt-5-mini","input":null}`, wantStatus: http.StatusBadRequest},
		{name: "streaming, missing input", body: `{"model":"gpt-5-mini","stream":true}`, wantStatus: http.StatusBadRequest},
		{name: "input present", body: `{"model":"gpt-5-mini","input":"hi"}`, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(fidelityResponsesProvider(), nil)
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantStatus != http.StatusBadRequest {
				return
			}
			var envelope struct {
				Error struct {
					Message string  `json:"message"`
					Type    string  `json:"type"`
					Param   *string `json:"param"`
					Code    *string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
			assert.Equal(t, "Missing required parameter: 'input'.", envelope.Error.Message)
			assert.Equal(t, "invalid_request_error", envelope.Error.Type)
			require.NotNil(t, envelope.Error.Param)
			assert.Equal(t, "input", *envelope.Error.Param)
			require.NotNil(t, envelope.Error.Code)
			assert.Equal(t, "missing_required_parameter", *envelope.Error.Code)
		})
	}
}
