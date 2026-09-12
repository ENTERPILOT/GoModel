package core

import (
	"testing"

	"github.com/goccy/go-json"
)

// A native Responses provider reports truncation as status "incomplete" with
// incomplete_details; both must survive the round trip to the client.
func TestResponsesResponse_IncompleteDetailsRoundTrip(t *testing.T) {
	var resp ResponsesResponse
	if err := json.Unmarshal([]byte(`{
		"id": "resp_1",
		"object": "response",
		"status": "incomplete",
		"model": "gpt-5-mini",
		"output": [],
		"incomplete_details": {"reason": "max_output_tokens"}
	}`), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if resp.IncompleteDetails == nil || resp.IncompleteDetails.Reason != "max_output_tokens" {
		t.Fatalf("IncompleteDetails = %+v, want reason max_output_tokens", resp.IncompleteDetails)
	}

	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	details, ok := payload["incomplete_details"].(map[string]any)
	if !ok || details["reason"] != "max_output_tokens" {
		t.Fatalf("incomplete_details = %#v, want reason max_output_tokens", payload["incomplete_details"])
	}
}

// A completed response must not carry an empty incomplete_details object.
func TestResponsesResponse_CompletedOmitsIncompleteDetails(t *testing.T) {
	encoded, err := json.Marshal(ResponsesResponse{ID: "resp_1", Object: "response", Status: "completed"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if _, exists := payload["incomplete_details"]; exists {
		t.Fatalf("did not expect incomplete_details on a completed response: %s", encoded)
	}
}
