package responsecache

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateCacheableSSE(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  []byte
		want bool
	}{
		{
			name: "rejects empty input",
			raw:  []byte(""),
			want: false,
		},
		{
			name: "valid chat stream",
			raw: []byte(
				"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
					"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
					"data: [DONE]\n\n",
			),
			want: true,
		},
		{
			name: "accepts eof terminated done marker",
			raw: []byte(
				"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
					"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
					"data: [DONE]\n",
			),
			want: true,
		},
		{
			name: "rejects truncated stream without done",
			raw: []byte(
				"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n",
			),
			want: false,
		},
		{
			name: "rejects malformed json payload",
			raw: []byte(
				"data: {\"id\":\"chatcmpl-1\"\n\n" +
					"data: [DONE]\n\n",
			),
			want: false,
		},
		{
			name: "rejects json that is malformed past the type field",
			raw: []byte(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":01}}\n\n" +
					"data: [DONE]\n\n",
			),
			want: false,
		},
		{
			name: "accepts a non-object json payload",
			raw:  []byte("data: [1]\n\ndata: {\"type\":7}\n\ndata: [DONE]\n\n"),
			want: true,
		},
		{
			name: "rejects comment only stream",
			raw: []byte(
				": keep-alive\n\n" +
					": still-alive\n\n" +
					"data: [DONE]\n\n",
			),
			want: false,
		},
		{
			name: "rejects incomplete responses stream",
			raw: []byte(
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
					"event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_1\",\"status\":\"incomplete\"}}\n\n" +
					"data: [DONE]\n\n",
			),
			want: false,
		},
		{
			name: "rejects failed responses stream",
			raw: []byte(
				"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_1\",\"status\":\"failed\"}}\n\n" +
					"data: [DONE]\n\n",
			),
			want: false,
		},
		{
			name: "rejects incomplete event with whitespace formatting",
			raw: []byte(
				"event: response.incomplete\ndata: { \"response\" : {\"id\":\"resp_1\"}, \"type\" : \"response.incomplete\" }\n\n" +
					"data: [DONE]\n\n",
			),
			want: false,
		},
		{
			name: "accepts responses stream ending on response.completed without done",
			raw: []byte(
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
					"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n",
			),
			want: true,
		},
		{
			name: "rejects responses stream with events after response.completed and no done",
			raw: []byte(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
					"data: {\"type\":\"response.output_text.delta\",\"delta\":\"late\"}\n\n",
			),
			want: false,
		},
		{
			name: "accepts keep-alive comments after response.completed",
			raw: []byte(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
					": keep-alive\n\n",
			),
			want: true,
		},
		{
			name: "rejects undispatched response.completed",
			raw:  []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n"),
			want: false,
		},
		{
			name: "rejects an empty-data event after response.completed",
			raw: []byte(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
					"event: error\ndata:\n\n",
			),
			want: false,
		},
		{
			name: "rejects an indented field after response.completed",
			raw: []byte(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
					" event: error\n\n",
			),
			want: false,
		},
		{
			name: "rejects a named error event even with done",
			raw: []byte(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
					"event: error\ndata: {\"type\":\"response.completed\"}\n\n" +
					"data: [DONE]\n\n",
			),
			want: false,
		},
		{
			name: "rejects a partial event after response.completed",
			raw: []byte(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
					"event: resp",
			),
			want: false,
		},
		{
			name: "rejects responses stream cut before response.completed",
			raw: []byte(
				"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
					"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n",
			),
			want: false,
		},
		{
			name: "rejects payload after done",
			raw: []byte(
				"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n" +
					"data: [DONE]\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\"}}\n\n",
			),
			want: false,
		},
		{
			name: "rejects keepalive after done",
			raw: []byte(
				"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n" +
					"data: [DONE]\n\n" +
					": keep-alive\n\n",
			),
			want: false,
		},
		{
			name: "rejects trailing partial event",
			raw: []byte(
				"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
					"data: [DONE]\n\n" +
					": trailing keepalive",
			),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := validateCacheableSSE(tt.raw)
			require.Equal(t, tt.want, got)
		})
	}
}

func BenchmarkValidateCacheableSSE(b *testing.B) {
	stream := func(event, terminator string) []byte {
		return []byte(strings.Repeat(event, 400) + terminator)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"chat", stream(
			"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" token\"},\"finish_reason\":null}]}\n\n",
			"data: [DONE]\n\n",
		)},
		{"responses", stream(
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\" token\"}\n\n",
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n",
		)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			if !validateCacheableSSE(tc.raw) {
				b.Fatal("stream must be cacheable")
			}
			b.SetBytes(int64(len(tc.raw)))
			b.ReportAllocs()
			for b.Loop() {
				validateCacheableSSE(tc.raw)
			}
		})
	}
}
