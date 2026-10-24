package server

import (
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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

	c, rec := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]}`)
	err := NewHandler(mock, nil, nil, nil).ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := rec.Body.String()
	for _, want := range []string{`"citations":["https://example.com"]`, `"native_finish_reason":"end_turn"`} {
		require.Contains(t, body, want)
	}
}
