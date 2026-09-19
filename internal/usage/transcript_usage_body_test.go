package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTranscriptUsageBody covers how a relayed transcript's usage is recovered:
// a buffered JSON reply passes through untouched, a server-sent event stream
// yields its usage-bearing event, and a stream without one is left alone so the
// extractors fall back to the upload duration.
func TestTranscriptUsageBody(t *testing.T) {
	const doneEvent = `{"type":"transcript.text.done","text":"hi","usage":{"type":"tokens","input_tokens":7,"output_tokens":3,"total_tokens":10}}`

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "buffered json passes through",
			body: `{"text":"hi","usage":{"type":"tokens","input_tokens":7}}`,
			want: `{"text":"hi","usage":{"type":"tokens","input_tokens":7}}`,
		},
		{
			name: "event stream yields the usage event",
			body: "data: {\"type\":\"transcript.text.delta\",\"delta\":\"hi\"}\n\n" +
				"data: " + doneEvent + "\n\ndata: [DONE]\n\n",
			want: doneEvent,
		},
		{
			// Deltas carry no usage, so nothing is extracted and the caller's
			// own fallback (the uploaded audio's duration) still applies.
			name: "event stream without usage is unchanged",
			body: "data: {\"type\":\"transcript.text.delta\",\"delta\":\"hi\"}\n\ndata: [DONE]\n\n",
			want: "data: {\"type\":\"transcript.text.delta\",\"delta\":\"hi\"}\n\ndata: [DONE]\n\n",
		},
		{
			name: "empty body is unchanged",
			body: "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, string(TranscriptUsageBody([]byte(tt.body))))
		})
	}
}

// TestExtractFromTranscriptionResponse_StreamedTokenUsage pins the end result:
// a streamed transcript is priced from the provider's reported tokens, exactly
// as the buffered reply for the same call would be.
func TestExtractFromTranscriptionResponse_StreamedTokenUsage(t *testing.T) {
	sse := []byte("data: {\"type\":\"transcript.text.delta\",\"delta\":\"hi\"}\n\n" +
		"data: {\"type\":\"transcript.text.done\",\"text\":\"hi\"," +
		"\"usage\":{\"type\":\"tokens\",\"input_tokens\":14,\"output_tokens\":45,\"total_tokens\":59}}\n\n" +
		"data: [DONE]\n\n")

	entry := ExtractFromTranscriptionResponse(TranscriptUsageBody(sse), nil, "req-1", "gpt-4o-transcribe", "openai")
	require.NotNil(t, entry)
	assert.Equal(t, 14, entry.InputTokens)
	assert.Equal(t, 45, entry.OutputTokens)
	assert.Equal(t, 59, entry.TotalTokens, "token counts mismatch: %+v", entry)
}
