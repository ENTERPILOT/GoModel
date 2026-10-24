package streaming

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SSE parsing strips one space after "data:", so "data:  {...}" keeps a
// leading space; every classifier must still see the JSON object.
func TestCodecs_ClassifyPayloadWithLeadingWhitespace(t *testing.T) {
	chat := ` {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"secret"}}]}`
	ev := ChatCodec().Decode(rawData("", chat), 0)
	assert.Equal(t, KindTextDelta, ev.Kind)
	assert.Equal(t, "secret", ev.Text)

	responses := "\t{\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"secret\"}"
	ev = ResponsesCodec().Decode(rawData("response.output_text.delta", responses), 0)
	assert.Equal(t, KindTextDelta, ev.Kind)
	assert.Equal(t, "secret", ev.Text)

	two := ` {"id":"c1","choices":[{"index":0,"delta":{"content":"a"}},{"index":1,"delta":{"content":"b"}}]}`
	parts := ChatCodec().Split(rawData("", two))
	assert.Len(t, parts, 2)

	added := ` {"type":"response.output_item.added","output_index":0,"item":{"id":"msg","type":"message"}}`
	done := ` {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"secret"}`
	codec := ResponsesCodec()
	codec.Track(codec.Decode(rawData("response.output_item.added", added), 0))
	delta := codec.Decode(rawData("response.output_text.delta", responses), 1)
	rewritten, err := codec.RewriteText(delta, "safe")
	require.NoError(t, err)

	codec.Track(rewritten)
	restated, changed := codec.Restate(codec.Decode(rawData("response.output_text.done", done), 2))
	assert.True(t, changed)
	assert.Contains(t, string(restated.Data), `"text":"safe"`, "responses restate = %v %s, want text rewritten", changed, restated.Data)
}

func TestAssemble_AcceptsLeadingWhitespace(t *testing.T) {
	chat, err := AssembleChatResponse([]Event{
		{Data: []byte(` {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`)},
		{Data: []byte(`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)},
	})
	require.NoError(t, err)
	require.Len(t, chat.Choices, 1)
	assert.Equal(t, "hi", chat.Choices[0].Message.Content, "chat assembled = %+v", chat)

	resp, err := AssembleResponsesResponse([]Event{
		{Name: "response.output_item.added", Data: []byte(` {"type":"response.output_item.added","output_index":0,"item":{"id":"msg","type":"message","role":"assistant"}}`)},
		{Name: "response.output_text.delta", Data: []byte(` {"type":"response.output_text.delta","output_index":0,"delta":"hi"}`)},
	})
	require.NoError(t, err)
	require.Len(t, resp.Output, 1)
	require.Len(t, resp.Output[0].Content, 1)
	assert.Equal(t, "hi", resp.Output[0].Content[0].Text, "responses assembled = %+v", resp)
}
