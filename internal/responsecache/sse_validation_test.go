package responsecache

import (
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
