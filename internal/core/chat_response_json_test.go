package core

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestChatResponseJSON_RoundTripsUnknownMembers(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-4o-mini","provider":"upstream","created":1,` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"Hi","annotations":[]},"finish_reason":"stop","native_finish_reason":"end_turn"}],` +
		`"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"cost":0.0001},"citations":["https://example.com"]}`)

	var resp ChatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := string(lookupUnknownField(t, resp.ExtraFields, "citations")); got != `["https://example.com"]` {
		t.Fatalf("citations = %s", got)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(resp.Choices))
	}
	if got := string(lookupUnknownField(t, resp.Choices[0].ExtraFields, "native_finish_reason")); got != `"end_turn"` {
		t.Fatalf("native_finish_reason = %s", got)
	}
	if resp.Choices[0].FinishReason != "stop" || resp.Usage.TotalTokens != 3 {
		t.Fatalf("typed fields lost: %+v", resp)
	}

	// The gateway overwrites the provider member the way the translated path
	// does, so the re-encoded body must carry that value, not the upstream's.
	resp.Provider = "openai"
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, want := range []string{`"provider":"openai"`, `"citations":["https://example.com"]`, `"native_finish_reason":"end_turn"`, `"cost":0.0001`, `"annotations":[]`} {
		if !bytes.Contains(encoded, []byte(want)) {
			t.Fatalf("encoded body missing %s:\n%s", want, encoded)
		}
	}
	if bytes.Contains(encoded, []byte(`"upstream"`)) {
		t.Fatalf("upstream provider member survived re-encoding:\n%s", encoded)
	}
}

func TestChatResponseJSON_NullUsageAndChoiceDecode(t *testing.T) {
	var resp ChatResponse
	if err := json.Unmarshal([]byte(`{"id":"x","choices":[null],"usage":null}`), &resp); err != nil {
		t.Fatalf("Unmarshal with nulls: %v", err)
	}
	if !resp.ExtraFields.IsEmpty() || len(resp.Choices) != 1 || !resp.Choices[0].ExtraFields.IsEmpty() {
		t.Fatalf("unexpected extras on null members: %+v", resp)
	}
}
