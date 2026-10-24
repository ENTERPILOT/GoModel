package streaming

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func scanAll(t *testing.T, scanner *EventScanner, chunks ...string) []RawEvent {
	t.Helper()
	var events []RawEvent
	for _, chunk := range chunks {
		for _, ev := range scanner.Feed([]byte(chunk)) {
			events = append(events, cloneRawEvent(ev))
		}
	}
	for _, ev := range scanner.Flush() {
		events = append(events, cloneRawEvent(ev))
	}
	return events
}

func cloneRawEvent(ev RawEvent) RawEvent {
	ev.Raw = append([]byte(nil), ev.Raw...)
	if ev.Data != nil {
		ev.Data = append(make([]byte, 0, len(ev.Data)), ev.Data...)
	}
	return ev
}

func TestEventScanner(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   []RawEvent
	}{
		{
			name:   "single line data",
			chunks: []string{"data: {\"a\":1}\n\n"},
			want:   []RawEvent{{Data: []byte(`{"a":1}`), Raw: []byte("data: {\"a\":1}\n\n")}},
		},
		{
			name:   "boundary split across reads",
			chunks: []string{"data: {\"a\":", "1}\n", "\ndata: [DONE]\n\n"},
			want: []RawEvent{
				{Data: []byte(`{"a":1}`), Raw: []byte("data: {\"a\":1}\n\n")},
				{Data: []byte("[DONE]"), Raw: []byte("data: [DONE]\n\n")},
			},
		},
		{
			name:   "crlf boundaries split across reads",
			chunks: []string{"data: x\r\n", "\r\ndata: y\r\n\r\n"},
			want: []RawEvent{
				{Data: []byte("x"), Raw: []byte("data: x\r\n\r\n")},
				{Data: []byte("y"), Raw: []byte("data: y\r\n\r\n")},
			},
		},
		{
			name:   "event name and multi-line data",
			chunks: []string{"event: response.output_text.delta\ndata: {\"delta\":\ndata: \"hi\"}\n\n"},
			want: []RawEvent{{
				Name: "response.output_text.delta",
				Data: []byte("{\"delta\":\n\"hi\"}"),
				Raw:  []byte("event: response.output_text.delta\ndata: {\"delta\":\ndata: \"hi\"}\n\n"),
			}},
		},
		{
			name:   "comment and id-only blocks are relayed as comments",
			chunks: []string{": ping\n\nid: 7\nretry: 100\n\n\n\ndata: a\n\n"},
			want: []RawEvent{
				{Comment: true, Raw: []byte(": ping\n\n")},
				{Comment: true, Raw: []byte("id: 7\nretry: 100\n\n")},
				{Comment: true, Raw: []byte("\n\n")},
				{Data: []byte("a"), Raw: []byte("data: a\n\n")},
			},
		},
		{
			name:   "comment line inside a data event is ignored for parsing",
			chunks: []string{": note\ndata: a\n\n"},
			want:   []RawEvent{{Data: []byte("a"), Raw: []byte(": note\ndata: a\n\n")}},
		},
		{
			name:   "data without space and empty data",
			chunks: []string{"data:x\n\ndata:\n\n"},
			want: []RawEvent{
				{Data: []byte("x"), Raw: []byte("data:x\n\n")},
				{Data: []byte{}, Raw: []byte("data:\n\n")},
			},
		},
		{
			name:   "trailing block without boundary is flushed",
			chunks: []string{"data: a\n\ndata: tail\n"},
			want: []RawEvent{
				{Data: []byte("a"), Raw: []byte("data: a\n\n")},
				{Data: []byte("tail"), Raw: []byte("data: tail\n")},
			},
		},
		{
			name:   "many events in one read",
			chunks: []string{"data: 1\n\ndata: 2\n\ndata: 3\n\n"},
			want: []RawEvent{
				{Data: []byte("1"), Raw: []byte("data: 1\n\n")},
				{Data: []byte("2"), Raw: []byte("data: 2\n\n")},
				{Data: []byte("3"), Raw: []byte("data: 3\n\n")},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scanAll(t, &EventScanner{}, tt.chunks...)
			require.Len(t, got, len(tt.want), "events: %+v", got)

			for i := range got {
				assert.Equal(t, tt.want[i].Name, got[i].Name)
				assert.Equal(t, tt.want[i].Comment, got[i].Comment)
				assert.Equal(t, tt.want[i].Data, got[i].Data)
				assert.Equal(t, tt.want[i].Data == nil, got[i].Data == nil)
				assert.Equal(t, tt.want[i].Raw, got[i].Raw, "event %d = %+v, want %+v", i, got[i], tt.want[i])
			}
			joined := joinRaw(got)
			assert.Equal(t, strings.Join(tt.chunks, ""), joined)
		})
	}
}

func joinRaw(events []RawEvent) string {
	var b strings.Builder
	for _, ev := range events {
		b.Write(ev.Raw)
	}
	return b.String()
}

func TestEventScanner_ByteAtATime(t *testing.T) {
	input := "event: e\ndata: {\"a\":1}\r\n\r\n: c\n\ndata: [DONE]\n\n"
	chunks := make([]string, 0, len(input))
	for i := range input {
		chunks = append(chunks, input[i:i+1])
	}
	got := scanAll(t, &EventScanner{}, chunks...)
	require.Len(t, got, 3)
	assert.Equal(t, "e", got[0].Name)
	assert.Equal(t, `{"a":1}`, string(got[0].Data), "first event = %+v", got[0])
	assert.True(t, got[1].Comment)
	assert.Equal(t, ": c\n\n", string(got[1].Raw), "second event = %+v", got[1])
	assert.Equal(t, "[DONE]", string(got[2].Data), "third event = %+v", got[2])
	assert.Equal(t, input, joinRaw(got))
}

func TestEventScanner_OversizedCompletedEventIsRelayedUnparsed(t *testing.T) {
	scanner := &EventScanner{MaxEventBytes: 16}
	big := "data: " + strings.Repeat("x", 40) + "\n\n"
	got := scanAll(t, scanner, big+"data: ok\n\n")
	require.Len(t, got, 2)
	assert.True(t, got[0].Oversized)
	assert.Nil(t, got[0].Data)
	assert.Equal(t, big, string(got[0].Raw), "completed oversized event should be an unparsed fragment: %+v", got[0])
	assert.False(t, got[1].Oversized)
	assert.Equal(t, "ok", string(got[1].Data), "event after oversized block = %+v", got[1])
}

func TestEventScanner_OversizedEventIsRelayedUnparsed(t *testing.T) {
	scanner := &EventScanner{MaxEventBytes: 16}
	big := "data: " + strings.Repeat("x", 40)
	got := scanAll(t, scanner, big[:10], big[10:30], big[30:]+"\n", "\ndata: ok\n\n")

	var oversized int
	var raw strings.Builder
	for _, ev := range got {
		raw.Write(ev.Raw)
		if ev.Oversized {
			oversized++
			assert.Nil(t, ev.Data)
			assert.False(t, ev.Comment, "oversized fragment should be unparsed: %+v", ev)
		}
	}
	require.NotEqual(t, 0, oversized)

	last := got[len(got)-1]
	assert.False(t, last.Oversized)
	assert.Equal(t, "ok", string(last.Data), "event after oversized block = %+v", last)
	assert.Equal(t, big+"\n\ndata: ok\n\n", raw.String())
}

func TestEventEncode(t *testing.T) {
	tests := []struct {
		name string
		ev   Event
		want string
	}{
		{"data only", Event{Data: []byte(`{"a":1}`)}, "data: {\"a\":1}\n\n"},
		{"named", Event{Name: "response.completed", Data: []byte(`{}`)}, "event: response.completed\ndata: {}\n\n"},
		{"multi-line data", Event{Data: []byte("a\nb")}, "data: a\ndata: b\n\n"},
		{"done", Event{Kind: KindDone, Data: []byte("[DONE]")}, "data: [DONE]\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(tt.ev.Encode())
			assert.Equal(t, tt.want, got)
		})
	}
}
