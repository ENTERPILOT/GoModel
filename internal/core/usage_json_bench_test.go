package core

import (
	"testing"

	"github.com/goccy/go-json"
)

func BenchmarkUsageUnmarshal(b *testing.B) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "openai",
			body: `{"prompt_tokens":17,"completion_tokens":4,"total_tokens":21,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}`,
		},
		{
			name: "unknown_members",
			body: `{"queue_time":0.0899,"prompt_tokens":38,"prompt_time":0.0016,"completion_tokens":4,"completion_time":0.0305,"total_tokens":42,"total_time":0.0322}`,
		},
		{
			name: "minimal",
			body: `{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}`,
		},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			data := []byte(tc.body)
			b.ReportAllocs()
			for b.Loop() {
				var usage Usage
				if err := json.Unmarshal(data, &usage); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
