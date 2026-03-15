package core

import (
	"encoding/json"
	"testing"
)

func TestCloneBatchRequestDeepCopiesNestedFields(t *testing.T) {
	original := &BatchRequest{
		InputFileID:      "file_source",
		Endpoint:         "/v1/chat/completions",
		CompletionWindow: "24h",
		Metadata: map[string]string{
			"provider": "openai",
		},
		Requests: []BatchRequestItem{
			{
				CustomID: "chat-1",
				Method:   "POST",
				URL:      "/v1/chat/completions",
				Body:     json.RawMessage(`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`),
				ExtraFields: map[string]json.RawMessage{
					"x_item": json.RawMessage(`{"trace":true}`),
				},
			},
		},
		ExtraFields: map[string]json.RawMessage{
			"x_top": json.RawMessage(`{"debug":true}`),
		},
	}

	cloned := cloneBatchRequest(original)
	if cloned == nil {
		t.Fatal("cloneBatchRequest() returned nil")
	}

	cloned.Metadata["provider"] = "anthropic"
	cloned.Requests[0].CustomID = "chat-2"
	cloned.Requests[0].Body[10] = 'X'
	itemExtra := cloned.Requests[0].ExtraFields["x_item"]
	itemExtra[9] = 'f'
	topExtra := cloned.ExtraFields["x_top"]
	topExtra[9] = 'f'

	if got := original.Metadata["provider"]; got != "openai" {
		t.Fatalf("original metadata mutated to %q", got)
	}
	if got := original.Requests[0].CustomID; got != "chat-1" {
		t.Fatalf("original custom_id mutated to %q", got)
	}
	if got := string(original.Requests[0].Body); got != `{"model":"smart","messages":[{"role":"user","content":"hi"}]}` {
		t.Fatalf("original body mutated to %s", got)
	}
	if got := string(original.Requests[0].ExtraFields["x_item"]); got != `{"trace":true}` {
		t.Fatalf("original item extra mutated to %s", got)
	}
	if got := string(original.ExtraFields["x_top"]); got != `{"debug":true}` {
		t.Fatalf("original top extra mutated to %s", got)
	}
}
