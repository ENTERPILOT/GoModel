package server

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/conversationstore"
	"github.com/enterpilot/gomodel/internal/core"
)

type maxReadCloser struct {
	reader *strings.Reader
	max    int
}

func (r *maxReadCloser) Read(p []byte) (int, error) {
	if len(p) > r.max {
		p = p[:r.max]
	}
	return r.reader.Read(p)
}

func (*maxReadCloser) Close() error { return nil }

func TestConversationPersistingStreamSuppressesEntireFragmentedCompletion(t *testing.T) {
	const createdEvent = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fragmented\"}}\n\n"
	const completedEvent = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fragmented\",\"output\":[]}}"

	for _, suffix := range []string{"\n\n", ""} {
		name := "at EOF"
		if suffix != "" {
			name = "with boundary"
		}
		t.Run(name, func(t *testing.T) {
			storeErr := errors.New("append unavailable")
			store := &appendFailingConversationStore{
				MemoryStore: conversationstore.NewMemoryStore(),
				err:         storeErr,
			}
			turn := &conversationTurn{store: store, id: "conv_fragmented", input: "hello"}
			stream := turn.persistingStream(context.Background(), &maxReadCloser{
				reader: strings.NewReader(createdEvent + completedEvent + suffix),
				max:    1,
			})
			t.Cleanup(func() { _ = stream.Close() })

			got, err := io.ReadAll(stream)
			if !errors.Is(err, storeErr) {
				t.Fatalf("read error = %v, want append unavailable", err)
			}
			if string(got) != createdEvent {
				t.Fatalf("stream body = %q, want only complete non-terminal event %q", got, createdEvent)
			}
		})
	}
}

func TestConversationPersistingStreamPreservesFragmentedSuccessfulStream(t *testing.T) {
	const streamData = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fragmented\"}}\r\n\r\n" +
		"data: {\"type\":\"response.completed\",\r\n" +
		"data: \"response\":{\"id\":\"resp_fragmented\",\"output\":[{\"id\":\"future_1\",\"type\":\"future_item\",\"future_counter\":9007199254740993,\"future_payload\":{\"preserved\":true}}]}}\r\n\r\n" +
		"data: [DONE]\r\n\r\n"

	ctx := context.Background()
	store := conversationstore.NewMemoryStore()
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Create(ctx, &conversationstore.StoredConversation{
		Conversation: &core.Conversation{ID: "conv_fragmented", Object: "conversation", Metadata: map[string]string{}},
	}); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	turn := &conversationTurn{store: store, id: "conv_fragmented", input: "hello"}
	stream := turn.persistingStream(ctx, &maxReadCloser{
		reader: strings.NewReader(streamData),
		max:    1,
	})
	t.Cleanup(func() { _ = stream.Close() })

	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if string(got) != streamData {
		t.Fatalf("stream body changed:\n got: %q\nwant: %q", got, streamData)
	}
	stored, err := store.Get(ctx, "conv_fragmented")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if len(stored.Items) != 2 {
		t.Fatalf("stored items = %d, want input and output items", len(stored.Items))
	}
	output, err := decodeRawJSONObject(stored.Items[1])
	if err != nil {
		t.Fatalf("decode stored output: %v", err)
	}
	if got := string(output["future_counter"]); got != "9007199254740993" {
		t.Fatalf("stored future_counter = %s, want exact large integer", got)
	}
	if got := string(output["future_payload"]); got != `{"preserved":true}` {
		t.Fatalf("stored future_payload = %s, want unknown field preserved", got)
	}
}
