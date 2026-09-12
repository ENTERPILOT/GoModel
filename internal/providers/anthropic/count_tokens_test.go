package anthropic

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Anthropic counts tokens exactly through /v1/messages/count_tokens. The
// original request body goes upstream unchanged apart from two things: the
// model is the resolved one, and fields the count endpoint does not accept
// (max_tokens, stream, sampling) are left out, because Anthropic rejects
// unknown fields rather than ignoring them.
func TestCountMessagesTokens(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"input_tokens":2414}`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body := []byte(`{"model":"anthropic/claude-haiku-4-5","max_tokens":64,"stream":true,"temperature":0.2,"metadata":{"user_id":"u"},
		"system":"be terse","messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"t","input_schema":{"type":"object"}}],"tool_choice":{"type":"auto"},"thinking":{"type":"enabled","budget_tokens":1024}}`)
	count, err := provider.CountMessagesTokens(context.Background(), "claude-haiku-4-5", body)
	require.NoError(t, err)
	assert.Equal(t, 2414, count)

	sent := capture.Last(t)
	assert.Equal(t, "/messages/count_tokens", sent.Path)
	var gotBody map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(sent.Body, &gotBody))
	assert.Equal(t, `"claude-haiku-4-5"`, string(gotBody["model"]))

	keys := slices.Sorted(maps.Keys(gotBody))
	assert.Equal(t, []string{"messages", "model", "system", "thinking", "tool_choice", "tools"}, keys, "forwarded fields")
}

// An upstream failure is returned as an error so the caller can fall back.
func TestCountMessagesTokens_UpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)
	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	_, err := provider.CountMessagesTokens(context.Background(), "claude-haiku-4-5", []byte(`{"model":"m","messages":[]}`))
	require.Error(t, err)
}
