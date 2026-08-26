package responsecache

import "testing"

func TestExtractEmbedText_ResponsesInputArray(t *testing.T) {
	body := []byte(`{
  "model": "claude-opus-4-6",
  "reasoning": {"effort": "medium"},
  "input": [
    {"role": "user", "content": "what is the capital of Germany"}
  ]
}`)
	text, n := extractEmbedText(body, false)
	if text != "what is the capital of Germany" {
		t.Fatalf("embed text = %q, want Germany question", text)
	}
	if n != 1 {
		t.Fatalf("nonSystemCount = %d, want 1", n)
	}
}

func TestExtractEmbedText_InputString(t *testing.T) {
	body := []byte(`{"model":"x","input":"hello"}`)
	text, n := extractEmbedText(body, false)
	if text != "hello" || n != 1 {
		t.Fatalf("got %q, n=%d", text, n)
	}
}

func TestConversationInvariantFingerprint_ResponsesInputArray(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":"same"}]}`)
	fp, ok := conversationInvariantFingerprint(body, false)
	if !ok {
		t.Fatal("expected ok")
	}
	if fp == "" {
		t.Fatal("expected non-empty fingerprint for structured input array")
	}
}

func TestConversationInvariantFingerprint_InputString(t *testing.T) {
	body := []byte(`{"input":"hi"}`)
	fp, ok := conversationInvariantFingerprint(body, false)
	if !ok || fp != "" {
		t.Fatalf("fp=%q ok=%v", fp, ok)
	}
}

func TestComputeParamsHash_IncludesReasoning(t *testing.T) {
	low := []byte(`{"model":"m","reasoning":{"effort":"low"}}`)
	high := []byte(`{"model":"m","reasoning":{"effort":"high"}}`)
	h1 := computeParamsHash(low, "/v1/responses", nil, "", "embed")
	h2 := computeParamsHash(high, "/v1/responses", nil, "", "embed")
	if h1 == h2 {
		t.Fatal("expected different params hashes when reasoning differs")
	}
}

// TestComputeParamsHash_IncludesFlatReasoningEffort covers the Chat
// Completions spelling: it shapes the answer just like the nested object, so
// two efforts must not share a cached response.
func TestComputeParamsHash_IncludesFlatReasoningEffort(t *testing.T) {
	low := []byte(`{"model":"m","reasoning_effort":"low"}`)
	high := []byte(`{"model":"m","reasoning_effort":"high"}`)
	none := []byte(`{"model":"m"}`)

	h1 := computeParamsHash(low, "/v1/chat/completions", nil, "", "embed")
	h2 := computeParamsHash(high, "/v1/chat/completions", nil, "", "embed")
	h3 := computeParamsHash(none, "/v1/chat/completions", nil, "", "embed")
	if h1 == h2 {
		t.Fatal("expected different params hashes when reasoning_effort differs")
	}
	if h1 == h3 {
		t.Fatal("expected a different params hash from a request without reasoning_effort")
	}
}
