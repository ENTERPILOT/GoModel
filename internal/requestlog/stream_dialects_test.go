package requestlog

import (
	"io"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/streaming"
)

// rebuildStream runs an SSE body through the real stream parser and returns
// the response body the request log would store for it.
func rebuildStream(t *testing.T, path string, events ...string) (string, bool) {
	t.Helper()

	builder := &streamResponseBuilder{IsResponsesAPI: strings.HasPrefix(path, "/v1/responses")}
	body := "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
	stream := streaming.NewObservedSSEStream(io.NopCloser(strings.NewReader(body)), &cachedStreamObserver{builder: builder})
	_, err := io.Copy(io.Discard, stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	rebuilt, err := json.Marshal(builder.build())
	require.NoError(t, err)
	return string(rebuilt), builder.truncated
}

// Every dialect a stream can speak must rebuild into the reply the client
// read. The path alone does not name the dialect: /v1/messages carries chat
// chunks when translated and Anthropic's events when forwarded natively.
func TestStreamResponseBuilderDialects(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		events []string
		want   string
	}{
		{
			name: "anthropic thinking, text and a tool call",
			path: "/v1/messages",
			events: []string{
				`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":12,"cache_read_input_tokens":5,"output_tokens":1}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
				`{"type":"ping"}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Paris, "}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"today."}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig=="}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"","citations":[]}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Checking "}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"citations_delta","citation":{"type":"char_location","cited_text":"Paris","document_index":0}}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"the weather."}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
				`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
				`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`,
				`{"type":"content_block_stop","index":2}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":42}}`,
				`{"type":"message_stop"}`,
			},
			want: `{
				"id":"msg_1","type":"message","role":"assistant","model":"claude-test",
				"content":[
					{"type":"thinking","thinking":"Paris, today.","signature":"sig=="},
					{"type":"text","text":"Checking the weather.","citations":[{"type":"char_location","cited_text":"Paris","document_index":0}]},
					{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris"}}
				],
				"stop_reason":"tool_use","stop_sequence":null,
				"usage":{"input_tokens":12,"cache_read_input_tokens":5,"output_tokens":42}
			}`,
		},
		{
			name: "anthropic stream cut inside a tool call",
			path: "/v1/messages",
			events: []string{
				`{"type":"message_start","message":{"id":"msg_2","role":"assistant","model":"claude-test","usage":{"input_tokens":3,"output_tokens":1}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_2","name":"get_weather","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Pa"}}`,
			},
			want: `{
				"id":"msg_2","type":"message","role":"assistant","model":"claude-test",
				"content":[{"type":"tool_use","id":"toolu_2","name":"get_weather","input":"{\"city\":\"Pa"}],
				"stop_reason":null,"stop_sequence":null,
				"usage":{"input_tokens":3,"output_tokens":1}
			}`,
		},
		{
			name: "chat chunks on the messages path stay a chat completion",
			path: "/v1/messages",
			events: []string{
				`{"id":"chatcmpl-1","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
				// A keepalive is not evidence of an Anthropic stream.
				`{"type":"ping"}`,
				`{"id":"chatcmpl-1","model":"gpt-test","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
			},
			want: `{
				"id":"chatcmpl-1","object":"chat.completion","model":"gpt-test","created":0,
				"choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}]
			}`,
		},
		{
			name: "legacy text completion",
			path: "/v1/completions",
			events: []string{
				`{"id":"cmpl-1","object":"text_completion","model":"llama-test","choices":[{"index":0,"text":"Hel","finish_reason":null}]}`,
				`{"id":"cmpl-1","object":"text_completion","model":"llama-test","choices":[{"index":0,"text":"lo","finish_reason":"stop"}]}`,
			},
			want: `{
				"id":"cmpl-1","object":"chat.completion","model":"llama-test","created":0,
				"choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}]
			}`,
		},
		{
			name: "responses turn that only calls a tool",
			path: "/v1/responses",
			events: []string{
				`{"type":"response.created","response":{"id":"resp_1","status":"in_progress","model":"gpt-test","created_at":7,"output":[]}}`,
				`{"type":"response.function_call_arguments.delta","delta":"{\"city\":\"Paris\"}"}`,
				`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-test","created_at":7,"output":[{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}]}}`,
			},
			want: `{
				"id":"resp_1","object":"response","model":"gpt-test","created_at":7,"status":"completed",
				"output":[{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}]
			}`,
		},
		{
			name: "responses stream cut before its closing event",
			path: "/v1/responses",
			events: []string{
				`{"type":"response.created","response":{"id":"resp_2","status":"in_progress","model":"gpt-test","created_at":7,"output":[]}}`,
				`{"type":"response.output_text.delta","delta":"Hel"}`,
				`{"type":"response.output_text.delta","delta":"lo"}`,
			},
			want: `{
				"id":"resp_2","object":"response","model":"gpt-test","created_at":7,"status":"in_progress",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}]
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, truncated := rebuildStream(t, tt.path, tt.events...)
			assert.JSONEq(t, tt.want, got)
			assert.False(t, truncated)
		})
	}
}

// An Anthropic stream is held to the same capture budget as a chat stream,
// including a block that arrives whole in its opening event.
func TestAnthropicStreamRespectsCaptureBudget(t *testing.T) {
	oversized := strings.Repeat("x", MaxContentCapture)
	got, truncated := rebuildStream(t, "/v1/messages",
		`{"type":"message_start","message":{"id":"msg_3","role":"assistant","model":"claude-test"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"kept"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":"`+oversized+`"}}`,
	)

	assert.True(t, truncated)
	assert.JSONEq(t, `{
		"id":"msg_3","type":"message","role":"assistant","model":"claude-test",
		"content":[{"type":"text","text":"kept"},{"type":"web_search_tool_result"}],
		"stop_reason":null,"stop_sequence":null
	}`, got)
}

// A tool input is logged as the client received it: a number too large for a
// float64 keeps every digit.
func TestAnthropicStreamKeepsToolInputNumbersExact(t *testing.T) {
	got, _ := rebuildStream(t, "/v1/messages",
		`{"type":"message_start","message":{"id":"msg_4","role":"assistant","model":"claude-test"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_4","name":"get_order","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"id\":9007199254740993}"}}`,
	)

	assert.Contains(t, got, `"input":{"id":9007199254740993}`)
}
