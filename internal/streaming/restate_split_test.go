package streaming

import (
	"io"
	"strings"
	"testing"
)

func TestTransformedSSEStream_ResponsesRestatesDoneEventsAfterReplace(t *testing.T) {
	input := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r1\",\"model\":\"m\",\"created_at\":1}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
		"event: response.content_part.added\ndata: {\"type\":\"response.content_part.added\",\"sequence_number\":2,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"delta\":\"key secret \"}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":4,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"delta\":\"ok\"}\n\n" +
		"event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"sequence_number\":5,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"text\":\"key secret ok\"}\n\n" +
		"event: response.content_part.done\ndata: {\"type\":\"response.content_part.done\",\"sequence_number\":6,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"key secret ok\",\"annotations\":[]}}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":7,\"output_index\":0,\"item\":{\"id\":\"msg\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"key secret ok\",\"annotations\":[]}]}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":8,\"response\":{\"id\":\"r1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"m\",\"output\":[{\"id\":\"msg\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"key secret ok\",\"annotations\":[]}]}],\"usage\":{\"total_tokens\":3}}}\n\n" +
		"data: [DONE]\n\n"
	tr := &funcTransformer{onEvent: func(ev *Event) (Decision, error) {
		if ev.Kind == KindTextDelta && strings.Contains(ev.Text, "secret") {
			return Decision{Action: ActionReplace, Text: strings.ReplaceAll(ev.Text, "secret", "[x]")}, nil
		}
		return Decision{Action: ActionPass}, nil
	}}
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ResponsesCodec(), tr, TransformOptions{})
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	if strings.Contains(out, "secret") {
		t.Fatalf("original text restated after replace:\n%s", out)
	}
	for _, want := range []string{
		`"type":"response.output_text.done"`, `"text":"key [x] ok"`,
		`"type":"response.content_part.done"`, `"type":"response.output_item.done"`, `"type":"response.completed"`,
		`"usage":{"total_tokens":3}`, `"sequence_number":8`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %s:\n%s", want, out)
		}
	}
	if strings.Count(out, `"text":"key [x] ok"`) != 4 {
		t.Errorf("want the emitted text in all four restating events:\n%s", out)
	}
	resp, err := AssembleResponsesResponse(decodeResponsesEvents(t, got))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != "completed" || len(resp.Output) != 1 || resp.Output[0].Content[0].Text != "key [x] ok" {
		t.Errorf("assembled = %+v", resp)
	}
}

func TestTransformedSSEStream_ResponsesPassThroughStaysByteIdenticalWithoutEdits(t *testing.T) {
	input := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"msg\",\"type\":\"message\",\"content\":[]}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"output_index\":0,\"content_index\":0,\"delta\":\"hi\"}\n\n" +
		"event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"sequence_number\":5,\"output_index\":0,\"content_index\":0,\"text\":\"hi\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":8,\"response\":{\"id\":\"r1\",\"output\":[{\"id\":\"msg\",\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}]}}\n\n"
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ResponsesCodec(), &funcTransformer{}, TransformOptions{})
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != input {
		t.Fatalf("pass-through changed bytes:\n%s", got)
	}
}

func TestTransformedSSEStream_ChatSplitsMultiChoiceChunks(t *testing.T) {
	input := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a secret\"}},{\"index\":1,\"delta\":{\"content\":\"b secret\"}}],\"usage\":{\"total_tokens\":2}}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"},{\"index\":1,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	tr := &funcTransformer{onEvent: func(ev *Event) (Decision, error) {
		if ev.Kind == KindTextDelta {
			return Decision{Action: ActionReplace, Text: strings.ReplaceAll(ev.Text, "secret", "[x]")}, nil
		}
		return Decision{Action: ActionPass}, nil
	}}
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ChatCodec(), tr, TransformOptions{})
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	if strings.Contains(out, "secret") {
		t.Fatalf("second choice leaked:\n%s", out)
	}
	if got := texts(tr.seen); !equalStrings(got, []string{"a secret", "b secret"}) {
		t.Errorf("transformer saw %v, want both choices", got)
	}
	if strings.Count(out, `"usage":{"total_tokens":2}`) != 1 {
		t.Errorf("usage must appear exactly once:\n%s", out)
	}
	if strings.Count(out, `"finish_reason":"stop"`) != 2 {
		t.Errorf("both finish chunks expected:\n%s", out)
	}
	resp, err := AssembleChatResponse(decodeChatEvents(t, got))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Choices) != 2 || resp.Choices[0].Message.Content != "a [x]" || resp.Choices[1].Message.Content != "b [x]" {
		t.Errorf("assembled = %+v", resp.Choices)
	}
}
