package streaming

import (
	"io"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// responsesSeqFixture is a well-formed Responses stream numbered 0..9.
const responsesSeqFixture = "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r1\",\"model\":\"m\",\"created_at\":1}}\n\n" +
	"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
	"event: response.content_part.added\ndata: {\"type\":\"response.content_part.added\",\"sequence_number\":2,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"delta\":\"my key is \"}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":4,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"delta\":\"secret\"}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":5,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"delta\":\" ok\"}\n\n" +
	"event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"sequence_number\":6,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"text\":\"my key is secret ok\"}\n\n" +
	"event: response.content_part.done\ndata: {\"type\":\"response.content_part.done\",\"sequence_number\":7,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"my key is secret ok\",\"annotations\":[]}}\n\n" +
	"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":8,\"output_index\":0,\"item\":{\"id\":\"msg\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"my key is secret ok\",\"annotations\":[]}]}}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":9,\"response\":{\"id\":\"r1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"m\",\"output\":[{\"id\":\"msg\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"my key is secret ok\",\"annotations\":[]}]}],\"usage\":{\"total_tokens\":3}}}\n\n" +
	"data: [DONE]\n\n"

// sequenceNumbers extracts the sequence_number of every event of an SSE
// stream, in order, plus the type of the last numbered event.
func sequenceNumbers(t *testing.T, out []byte) ([]int, string) {
	t.Helper()
	var numbers []int
	last := ""
	for block := range strings.SplitSeq(string(out), "\n\n") {
		data := ""
		for line := range strings.SplitSeq(block, "\n") {
			if rest, ok := strings.CutPrefix(line, "data: "); ok {
				data = rest
			}
		}
		if data == "" || data == "[DONE]" {
			continue
		}
		var view struct {
			Type           string `json:"type"`
			SequenceNumber *int   `json:"sequence_number"`
		}
		err := json.Unmarshal([]byte(data), &view)
		require.NoError(t, err, "decode %q", data)
		require.NotNil(t, view.SequenceNumber, "event %q has no sequence_number:\n%s", view.Type, out)

		numbers = append(numbers, *view.SequenceNumber)
		last = view.Type
	}
	return numbers, last
}

func assertContiguous(t *testing.T, out []byte) {
	t.Helper()
	numbers, _ := sequenceNumbers(t, out)
	require.NotEmpty(t, numbers, "no numbered events:\n%s", out)

	for i, n := range numbers {
		require.Equal(t, i, n)
	}
}

// A transform plugin may drop, merge or add events; the client must still see
// sequence_number 0..N with no gaps, and the terminal event must carry the
// last number.
func TestTransformedSSEStream_ResponsesSequenceNumbersStayContiguous(t *testing.T) {
	tests := []struct {
		name     string
		opts     TransformOptions
		onEvent  func(ev *Event) (Decision, error)
		lastType string
	}{
		{
			name:     "pass through",
			lastType: "response.completed",
		},
		{
			name: "replace",
			onEvent: func(ev *Event) (Decision, error) {
				if ev.Kind == KindTextDelta {
					return Decision{Action: ActionReplace, Text: strings.ReplaceAll(ev.Text, "secret", "[x]")}, nil
				}
				return Decision{Action: ActionPass}, nil
			},
			lastType: "response.completed",
		},
		{
			name: "drop",
			onEvent: func(ev *Event) (Decision, error) {
				if ev.Kind == KindTextDelta {
					return Decision{Action: ActionDrop}, nil
				}
				return Decision{Action: ActionPass}, nil
			},
			lastType: "response.completed",
		},
		{
			name:     "coalesced deltas",
			opts:     TransformOptions{MinChunkChars: 12, LookbehindChars: 4},
			lastType: "response.completed",
		},
		{
			name: "split into more deltas",
			opts: TransformOptions{MinChunkChars: 12},
			onEvent: func(ev *Event) (Decision, error) {
				if ev.Kind == KindTextDelta {
					return Decision{Action: ActionReplace, Text: strings.ReplaceAll(ev.Text, "secret", "s e c r e t")}, nil
				}
				return Decision{Action: ActionPass}, nil
			},
			lastType: "response.completed",
		},
		{
			name: "terminate",
			onEvent: func(ev *Event) (Decision, error) {
				if ev.Kind == KindTextDelta && strings.Contains(ev.Text, "secret") {
					return Decision{Action: ActionTerminate, Terminate: &Termination{ErrorCode: "blocked", ErrorMessage: "no"}}, nil
				}
				return Decision{Action: ActionPass}, nil
			},
			lastType: "response.failed",
		},
		{
			name: "terminate on the first event",
			onEvent: func(ev *Event) (Decision, error) {
				return Decision{Action: ActionTerminate, Terminate: &Termination{ErrorCode: "blocked", ErrorMessage: "no"}}, nil
			},
			lastType: "response.failed",
		},
		{
			name: "terminate with replacement text",
			onEvent: func(ev *Event) (Decision, error) {
				if ev.Kind == KindTextDelta && strings.Contains(ev.Text, "secret") {
					return Decision{Action: ActionTerminate, Terminate: &Termination{Text: "redacted"}}, nil
				}
				return Decision{Action: ActionPass}, nil
			},
			lastType: "response.incomplete",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := &funcTransformer{onEvent: tc.onEvent}
			stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(responsesSeqFixture)), ResponsesCodec(), tr, tc.opts)
			got, err := io.ReadAll(stream)
			require.NoError(t, err)

			assertContiguous(t, got)
			_, last := sequenceNumbers(t, got)
			assert.Equal(t, tc.lastType, last)
			assert.True(t, strings.HasSuffix(string(got), "data: [DONE]\n\n"), "stream does not end with [DONE]:\n%s", got)
		})
	}
}

// An upstream stream that is already gapped, or that leaves an event
// unnumbered, is numbered for the client all the same.
func TestTransformedSSEStream_ResponsesRenumbersMalformedUpstream(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "gapped", input: strings.ReplaceAll(responsesSeqFixture, "\"sequence_number\":9", "\"sequence_number\":42")},
		{name: "unnumbered event", input: strings.ReplaceAll(responsesSeqFixture, "\"sequence_number\":4,", "")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(tc.input)), ResponsesCodec(), &funcTransformer{}, TransformOptions{})
			got, err := io.ReadAll(stream)
			require.NoError(t, err)

			assertContiguous(t, got)
		})
	}
}

// Chat streams carry no sequence numbers, so a pass-through stays byte
// identical.
func TestTransformedSSEStream_ChatPassThroughUnnumbered(t *testing.T) {
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(chatFixture)), ChatCodec(), &funcTransformer{}, TransformOptions{})
	got, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.Equal(t, chatFixture, string(got), "chat pass-through changed bytes")
}
