package server

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestNormalizedResponseInputItemsSkipsNilDefaultInput(t *testing.T) {
	var input *core.ResponsesInputElement
	req := &core.ResponsesRequest{Input: input}

	items := normalizedResponseInputItems("resp_1", req)
	require.Empty(t, items)
}

func TestNormalizedResponseInputRawPreservesLargeUnknownIntegers(t *testing.T) {
	item := normalizedResponseInputRaw("resp_1", 0, json.RawMessage(
		`{"type":"future_item","opaque_integer":9007199254740993,"nested":{"value":9007199254740995}}`,
	))
	for _, want := range []string{
		`"opaque_integer":9007199254740993`,
		`"value":9007199254740995`,
	} {
		require.Contains(t, string(item), want)
	}
	require.NotEmpty(t, responseInputItemID(item), "normalized item = %s, want generated id", item)
}

func TestNormalizedResponseInputRawSkipsNullObject(t *testing.T) {
	item := normalizedResponseInputRaw("resp_1", 0, json.RawMessage("null"))
	require.Empty(t, item)
}

func TestNormalizedResponseInputRawDecodesJSONStringFallback(t *testing.T) {
	item := normalizedResponseInputRaw("resp_1", 0, json.RawMessage(`"hello"`))
	require.NotEmpty(t, item)

	var decoded map[string]any
	err := json.Unmarshal(item, &decoded)
	require.NoError(t, err)

	content, ok := decoded["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)

	first, ok := content[0].(map[string]any)
	require.True(t, ok, "content[0] = %T, want object", content[0])
	require.Equal(t, "hello", first["text"])
}
