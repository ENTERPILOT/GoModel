package core

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

// A native Responses provider reports truncation as status "incomplete" with
// incomplete_details; both must survive the round trip to the client.
func TestResponsesResponse_IncompleteDetailsRoundTrip(t *testing.T) {
	var resp ResponsesResponse
	err := json.Unmarshal([]byte(`{
		"id": "resp_1",
		"object": "response",
		"status": "incomplete",
		"model": "gpt-5-mini",
		"output": [],
		"incomplete_details": {"reason": "max_output_tokens"}
	}`), &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.IncompleteDetails)
	require.Equal(t, "max_output_tokens", resp.IncompleteDetails.Reason)

	encoded, err := json.Marshal(resp)
	require.NoError(t, err)

	var payload map[string]any
	err = json.Unmarshal(encoded, &payload)
	require.NoError(t, err)

	details, ok := payload["incomplete_details"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "max_output_tokens", details["reason"])
}

// A completed response must not carry an empty incomplete_details object.
func TestResponsesResponse_CompletedOmitsIncompleteDetails(t *testing.T) {
	encoded, err := json.Marshal(ResponsesResponse{ID: "resp_1", Object: "response", Status: "completed"})
	require.NoError(t, err)

	var payload map[string]any
	err = json.Unmarshal(encoded, &payload)
	require.NoError(t, err)
	_, exists := payload["incomplete_details"]
	require.False(t, exists, "did not expect incomplete_details on a completed response: %s", encoded)
}
