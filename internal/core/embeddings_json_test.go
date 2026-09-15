package core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddingRequestJSON_RoundTripPreservesUnknownFields(t *testing.T) {
	body := []byte(`{
		"model":"text-embedding-3-small",
		"provider":"openai",
		"input":["hello","world"],
		"encoding_format":"float",
		"dimensions":256,
		"x_trace":{"id":"trace-1"},
		"x_mode":"keep-me"
	}`)

	wantExtra, err := extractUnknownJSONFields(body,
		"model",
		"provider",
		"input",
		"encoding_format",
		"dimensions",
	)
	require.NoError(t, err)

	var req EmbeddingRequest
	err = json.Unmarshal(body, &req)
	require.NoError(t, err)
	require.Equal(t, "text-embedding-3-small", req.Model)
	require.Equal(t, "openai", req.Provider)

	input, ok := req.Input.([]any)
	require.True(t, ok)
	require.Len(t, input, 2, "Input = %#v, want len=2", req.Input)
	require.Equal(t, "float", req.EncodingFormat)
	require.NotNil(t, req.Dimensions)
	require.Equal(t, 256, *req.Dimensions)

	traceField := lookupUnknownField(t, req.ExtraFields, "x_trace")
	require.Equal(t, string(wantExtra.Lookup("x_trace")), string(traceField))

	modeField := lookupUnknownField(t, req.ExtraFields, "x_mode")
	require.Equal(t, string(wantExtra.Lookup("x_mode")), string(modeField))

	roundTrip, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(roundTrip, &decoded)
	require.NoError(t, err)

	xTraceMap, ok := decoded["x_trace"].(map[string]any)
	require.True(t, ok, "x_trace = %#v, want object", decoded["x_trace"])
	require.Equal(t, "trace-1", xTraceMap["id"])
	require.Equal(t, "keep-me", decoded["x_mode"])
}
