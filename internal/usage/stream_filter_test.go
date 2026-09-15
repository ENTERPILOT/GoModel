package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasUsageObject(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "empty", raw: ``, want: false},
		{name: "content chunk without usage", raw: `{"choices":[{"delta":{"content":"Hi"}}]}`, want: false},
		{name: "include_usage null chunk", raw: `{"choices":[{"delta":{"content":"Hi"}}],"usage":null}`, want: false},
		{name: "null with spaces", raw: `{"usage" : null}`, want: false},
		{name: "usage word as a string value", raw: `{"delta":{"content":"usage"},"x":"usage"}`, want: false},
		{name: "truncated after key", raw: `{"usage":`, want: false},
		{name: "final openai chunk", raw: `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3}}`, want: true},
		{name: "object with spaces", raw: "{\"usage\" :\n\t{\"input_tokens\":1}}", want: true},
		{name: "responses completed nests usage", raw: `{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1}}}`, want: true},
		{name: "anthropic message_start nests usage", raw: `{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":9}}}`, want: true},
		{name: "null before a later object", raw: `{"usage":null,"response":{"usage":{"input_tokens":1}}}`, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, HasUsageObject([]byte(tc.raw)))
		})
	}
}

// The stream observer must keep asking for every event it could extract usage
// from; the filter only under-approximates disinterest.
func TestStreamUsageObserverFilterMatchesExtraction(t *testing.T) {
	observer := &StreamUsageObserver{}
	assert.False(t, observer.WantsJSONEvent([]byte(`{"choices":[{"delta":{"content":"Hi"}}],"usage":null}`)))
	assert.True(t, observer.WantsJSONEvent([]byte(`{"choices":[],"usage":{"prompt_tokens":5}}`)))
}
