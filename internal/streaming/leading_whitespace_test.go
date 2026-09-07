package streaming

import (
	"strings"
	"testing"
)

// SSE parsing strips one space after "data:", so "data:  {...}" keeps a
// leading space; every classifier must still see the JSON object.
func TestCodecs_ClassifyPayloadWithLeadingWhitespace(t *testing.T) {
	chat := ` {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"secret"}}]}`
	if ev := ChatCodec().Decode(rawData("", chat), 0); ev.Kind != KindTextDelta || ev.Text != "secret" {
		t.Errorf("chat decode = %s %q, want text delta", ev.Kind, ev.Text)
	}
	responses := "\t{\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"secret\"}"
	if ev := ResponsesCodec().Decode(rawData("response.output_text.delta", responses), 0); ev.Kind != KindTextDelta || ev.Text != "secret" {
		t.Errorf("responses decode = %s %q, want text delta", ev.Kind, ev.Text)
	}
	two := ` {"id":"c1","choices":[{"index":0,"delta":{"content":"a"}},{"index":1,"delta":{"content":"b"}}]}`
	if parts := ChatCodec().Split(rawData("", two)); len(parts) != 2 {
		t.Errorf("chat split = %d parts, want 2", len(parts))
	}
	added := ` {"type":"response.output_item.added","output_index":0,"item":{"id":"msg","type":"message"}}`
	done := ` {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"secret"}`
	codec := ResponsesCodec()
	codec.Track(codec.Decode(rawData("response.output_item.added", added), 0))
	delta := codec.Decode(rawData("response.output_text.delta", responses), 1)
	rewritten, err := codec.RewriteText(delta, "safe")
	if err != nil {
		t.Fatal(err)
	}
	codec.Track(rewritten)
	restated, changed := codec.Restate(codec.Decode(rawData("response.output_text.done", done), 2))
	if !changed || !strings.Contains(string(restated.Data), `"text":"safe"`) {
		t.Errorf("responses restate = %v %s, want text rewritten", changed, restated.Data)
	}
}

func TestAssemble_AcceptsLeadingWhitespace(t *testing.T) {
	chat, err := AssembleChatResponse([]Event{
		{Data: []byte(` {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`)},
		{Data: []byte(`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.Choices) != 1 || chat.Choices[0].Message.Content != "hi" {
		t.Errorf("chat assembled = %+v", chat)
	}
	resp, err := AssembleResponsesResponse([]Event{
		{Name: "response.output_item.added", Data: []byte(` {"type":"response.output_item.added","output_index":0,"item":{"id":"msg","type":"message","role":"assistant"}}`)},
		{Name: "response.output_text.delta", Data: []byte(` {"type":"response.output_text.delta","output_index":0,"delta":"hi"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Output) != 1 || len(resp.Output[0].Content) != 1 || resp.Output[0].Content[0].Text != "hi" {
		t.Errorf("responses assembled = %+v", resp)
	}
}
