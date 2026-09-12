package exchange

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err, "marshal %T: %v", v, err)

	return raw
}

func assertJSONEqual(t *testing.T, want, got any) {
	t.Helper()
	w, g := mustJSON(t, want), mustJSON(t, got)
	require.Equal(t, w, g)
}

func decodeChat(t *testing.T, body string) *core.ChatRequest {
	t.Helper()
	var req core.ChatRequest
	err := json.Unmarshal([]byte(body), &req)
	require.NoError(t, err)

	return &req
}

func decodeResponses(t *testing.T, body string) *core.ResponsesRequest {
	t.Helper()
	var req core.ResponsesRequest
	err := json.Unmarshal([]byte(body), &req)
	require.NoError(t, err)

	return &req
}

// messageJSON returns the JSON of one applied message so tests can compare
// against the original wire form.
func messageJSON(t *testing.T, m any) string {
	t.Helper()
	return string(mustJSON(t, m))
}
