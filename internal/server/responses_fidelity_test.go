package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	srv.handler.drainSnapshotWrites()

	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/v1/responses/resp_fidelity", nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200 (%s)", getRec.Code, getRec.Body.String())
	}

	want := map[string]string{
		"instructions":        `"be terse"`,
		"metadata":            `{"k":"v"}`,
		"tool_choice":         `"auto"`,
		"parallel_tool_calls": `false`,
		"truncation":          `"auto"`,
	}
	for name, payload := range map[string][]byte{"create": rec.Body.Bytes(), "retrieve": getRec.Body.Bytes()} {
		var got map[string]json.RawMessage
		if err := json.Unmarshal(payload, &got); err != nil {
			t.Fatalf("decode %s response: %v", name, err)
		}
		for member, value := range want {
			if string(got[member]) != value {
				t.Fatalf("%s response %q = %s, want %s (%s)", name, member, got[member], value, payload)
			}
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

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
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
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if envelope.Error.Message != "Missing required parameter: 'input'." {
				t.Fatalf("message = %q", envelope.Error.Message)
			}
			if envelope.Error.Type != "invalid_request_error" {
				t.Fatalf("type = %q, want invalid_request_error", envelope.Error.Type)
			}
			if envelope.Error.Param == nil || *envelope.Error.Param != "input" {
				t.Fatalf("param = %v, want input", envelope.Error.Param)
			}
			if envelope.Error.Code == nil || *envelope.Error.Code != "missing_required_parameter" {
				t.Fatalf("code = %v, want missing_required_parameter", envelope.Error.Code)
			}
		})
	}
}
