package core

import (
	"strconv"
	"strings"
	"testing"

	"github.com/goccy/go-json"
)

// benchmarkChatResponseOfSize is a buffered OpenAI chat completion whose
// assistant content is about contentLen bytes, with the members OpenAI sends
// today (refusal, annotations, logprobs, service tier, usage details).
func benchmarkChatResponseOfSize(contentLen int) []byte {
	content := strings.Repeat("abcdefghij", contentLen/10)
	return []byte(`{"id":"chatcmpl-AbC123","object":"chat.completion","created":1700000000,"model":"gpt-4o-mini-2024-07-18",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"` + content + `","refusal":null,"annotations":[]},"logprobs":null,"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":12,"completion_tokens":9,"total_tokens":21,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},` +
		`"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}},` +
		`"service_tier":"default","system_fingerprint":"fp_abc123"}`)
}

// benchmarkChatRequestOfMessages is a multi-turn chat request with a system
// prompt and messages turns of about messageLen bytes of content each.
func benchmarkChatRequestOfMessages(messages, messageLen int) []byte {
	var buf strings.Builder
	buf.WriteString(`{"model":"gpt-4o-mini","temperature":0.2,"messages":[{"role":"system","content":"You are helpful."}`)
	for i := range messages {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		buf.WriteString(`,{"role":"` + role + `","content":"` + strings.Repeat("lorem ipsum ", messageLen/12) + `"}`)
	}
	buf.WriteString(`]}`)
	return []byte(buf.String())
}

func BenchmarkChatResponseDecode(b *testing.B) {
	for _, size := range []int{512, 8 << 10, 64 << 10} {
		body := benchmarkChatResponseOfSize(size)
		b.Run(strconv.Itoa(size)+"B", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				var resp ChatResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkChatRequestDecode(b *testing.B) {
	cases := []struct {
		name       string
		messages   int
		messageLen int
	}{
		{name: "2KB", messages: 4, messageLen: 480},
		{name: "100KB_50msgs", messages: 50, messageLen: 2040},
	}
	for _, tc := range cases {
		body := benchmarkChatRequestOfMessages(tc.messages, tc.messageLen)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				var req ChatRequest
				if err := json.Unmarshal(body, &req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
