package streaming

import (
	"bytes"
	"strings"
	"testing"
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
			if len(got) != len(tt.want) {
				t.Fatalf("got %d events, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i].Name != tt.want[i].Name || got[i].Comment != tt.want[i].Comment ||
					!bytes.Equal(got[i].Data, tt.want[i].Data) || (got[i].Data == nil) != (tt.want[i].Data == nil) ||
					!bytes.Equal(got[i].Raw, tt.want[i].Raw) {
					t.Errorf("event %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
			if joined := joinRaw(got); joined != strings.Join(tt.chunks, "") {
				t.Errorf("raw bytes not preserved: %q", joined)
			}
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
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(got), got)
	}
	if got[0].Name != "e" || string(got[0].Data) != `{"a":1}` {
		t.Errorf("first event = %+v", got[0])
	}
	if !got[1].Comment || string(got[1].Raw) != ": c\n\n" {
		t.Errorf("second event = %+v", got[1])
	}
	if string(got[2].Data) != "[DONE]" {
		t.Errorf("third event = %+v", got[2])
	}
	if joinRaw(got) != input {
		t.Errorf("raw bytes not preserved: %q", joinRaw(got))
	}
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
			if ev.Data != nil || ev.Comment {
				t.Errorf("oversized fragment should be unparsed: %+v", ev)
			}
		}
	}
	if oversized == 0 {
		t.Fatal("expected oversized fragments")
	}
	last := got[len(got)-1]
	if last.Oversized || string(last.Data) != "ok" {
		t.Errorf("event after oversized block = %+v", last)
	}
	if raw.String() != big+"\n\ndata: ok\n\n" {
		t.Errorf("raw bytes not preserved: %q", raw.String())
	}
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
			if got := string(tt.ev.Encode()); got != tt.want {
				t.Errorf("Encode() = %q, want %q", got, tt.want)
			}
		})
	}
}
