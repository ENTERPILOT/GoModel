package exchange

import (
	"bytes"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return raw
}

func assertJSONEqual(t *testing.T, want, got any) {
	t.Helper()
	w, g := mustJSON(t, want), mustJSON(t, got)
	if !bytes.Equal(w, g) {
		t.Fatalf("JSON mismatch\nwant: %s\n got: %s", w, g)
	}
}

func decodeChat(t *testing.T, body string) *core.ChatRequest {
	t.Helper()
	var req core.ChatRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decode chat request: %v", err)
	}
	return &req
}

func decodeResponses(t *testing.T, body string) *core.ResponsesRequest {
	t.Helper()
	var req core.ResponsesRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decode responses request: %v", err)
	}
	return &req
}

// messageJSON returns the JSON of one applied message so tests can compare
// against the original wire form.
func messageJSON(t *testing.T, m any) string {
	t.Helper()
	return string(mustJSON(t, m))
}
