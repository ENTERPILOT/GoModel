package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

// TestChatCompletion_RelaysProviderExtraResponseMembers pins that the
// translated chat path forwards response members the gateway does not model,
// at the top level and per choice, instead of dropping them on re-encoding.
func TestChatCompletion_RelaysProviderExtraResponseMembers(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes:   map[string]string{"gpt-4o-mini": "openrouter"},
		response: &core.ChatResponse{
			ID:     "chatcmpl-1",
			Object: "chat.completion",
			Model:  "gpt-4o-mini",
			Choices: []core.Choice{{
				Index:        0,
				FinishReason: "stop",
				Message:      core.ResponseMessage{Role: "assistant", Content: "Hi"},
				ExtraFields:  core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"native_finish_reason": json.RawMessage(`"end_turn"`)}),
			}},
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"citations": json.RawMessage(`["https://example.com"]`)}),
		},
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := NewHandler(mock, nil, nil, nil).ChatCompletion(e.NewContext(req, rec)); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"citations":["https://example.com"]`, `"native_finish_reason":"end_turn"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %s:\n%s", want, body)
		}
	}
}
