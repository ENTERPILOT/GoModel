package core

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
)

// benchmarkChatResponseBody is an OpenAI chat completion of the shape the
// upstream returns today: refusal, annotations and logprobs are members the
// gateway does not model, so every object level carries ExtraFields.
func benchmarkChatResponseBody(contentLen int) []byte {
	content := strings.Repeat("abcdefghij", contentLen/10)
	return []byte(`{"id":"chatcmpl-AbC123","object":"chat.completion","created":1700000000,"model":"gpt-4o-mini-2024-07-18","choices":[{"index":0,"message":{"role":"assistant","content":"` + content + `","refusal":null,"annotations":[]},"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":9,"total_tokens":21,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}},"service_tier":"default","system_fingerprint":"fp_abc123"}`)
}

// benchmarkChatRequestBody is a conversation of the given number of turns,
// each about turnLen bytes, with a client-specific member on every user
// message and on the request itself.
func benchmarkChatRequestBody(turns, turnLen int) []byte {
	var sb strings.Builder
	sb.WriteString(`{"model":"gpt-4o-mini","temperature":0.2,"metadata":{"tenant":"acme"},"messages":[{"role":"system","content":"You are helpful."}`)
	text := strings.Repeat("lorem ipsum ", turnLen/12)
	for i := range turns {
		if i%2 == 0 {
			sb.WriteString(`,{"role":"user","name":"alice","content":"` + text + `"}`)
		} else {
			sb.WriteString(`,{"role":"assistant","content":"` + text + `"}`)
		}
	}
	sb.WriteString(`]}`)
	return []byte(sb.String())
}

func benchmarkMarshal[T any](b *testing.B, body []byte) {
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		out, err := json.Marshal(&v)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) == 0 {
			b.Fatal("expected output")
		}
	}
}

func BenchmarkChatResponseMarshal60B(b *testing.B) {
	benchmarkMarshal[ChatResponse](b, benchmarkChatResponseBody(60))
}

func BenchmarkChatResponseMarshal8KB(b *testing.B) {
	benchmarkMarshal[ChatResponse](b, benchmarkChatResponseBody(8000))
}

func BenchmarkChatResponseMarshal64KB(b *testing.B) {
	benchmarkMarshal[ChatResponse](b, benchmarkChatResponseBody(64000))
}

func BenchmarkChatRequestMarshal2KB(b *testing.B) {
	benchmarkMarshal[ChatRequest](b, benchmarkChatRequestBody(4, 450))
}

func BenchmarkChatRequestMarshal100KB(b *testing.B) {
	benchmarkMarshal[ChatRequest](b, benchmarkChatRequestBody(50, 2040))
}
