package streaming

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventPacedStream(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   []string
	}{
		{
			name:   "one event per read",
			stream: "data: 1\n\ndata: 2\n\ndata: [DONE]\n\n",
			want:   []string{"data: 1\n\n", "data: 2\n\n", "data: [DONE]\n\n"},
		},
		{
			name:   "CRLF framing",
			stream: "data: 1\r\n\r\nevent: x\r\ndata: 2\r\n\r\n",
			want:   []string{"data: 1\r\n\r\n", "event: x\r\ndata: 2\r\n\r\n"},
		},
		{
			name:   "unterminated tail",
			stream: "data: 1\n\ndata: 2",
			want:   []string{"data: 1\n\n", "data: 2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := NewEventPacedStream(io.NopCloser(strings.NewReader(tt.stream)))
			buf := make([]byte, 1024)
			var got []string
			for {
				n, err := stream.Read(buf)
				if n > 0 {
					got = append(got, string(buf[:n]))
				}
				if errors.Is(err, io.EOF) {
					break
				}
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// piecewiseReader returns one piece per Read, the way an upstream's reads
// split a stream.
type piecewiseReader struct{ pieces []string }

func (r *piecewiseReader) Read(p []byte) (int, error) {
	if len(r.pieces) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.pieces[0])
	if r.pieces[0] = r.pieces[0][n:]; r.pieces[0] == "" {
		r.pieces = r.pieces[1:]
	}
	return n, nil
}

func (r *piecewiseReader) Close() error { return nil }

// readPaced returns what each Read of the paced stream over pieces returns.
func readPaced(t *testing.T, pieces ...string) []string {
	t.Helper()
	stream := NewEventPacedStream(&piecewiseReader{pieces: pieces})
	buf := make([]byte, 1024)
	var got []string
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			got = append(got, string(buf[:n]))
		}
		if errors.Is(err, io.EOF) {
			return got
		}
		require.NoError(t, err)
	}
}

// An upstream read may end inside the blank line that ends an event. The rest
// of that blank line is handed out alone, so the event before it is complete
// before a byte of the next one is read.
func TestEventPacedStream_BlankLineSplitAcrossReads(t *testing.T) {
	tests := []struct {
		name   string
		pieces []string
		want   []string
	}{
		{
			name:   "LF",
			pieces: []string{"data: A\n", "\ndata: B\n\n"},
			want:   []string{"data: A\n", "\n", "data: B\n\n"},
		},
		{
			name:   "CRLF, split after the first line ending",
			pieces: []string{"data: A\r\n", "\r\ndata: B\r\n\r\n"},
			want:   []string{"data: A\r\n", "\r\n", "data: B\r\n\r\n"},
		},
		{
			name:   "CRLF, split inside the second line ending",
			pieces: []string{"data: A\r\n\r", "\ndata: B\r\n\r\n"},
			want:   []string{"data: A\r\n\r", "\n", "data: B\r\n\r\n"},
		},
		{
			name:   "CRLF, split inside the first line ending",
			pieces: []string{"data: A\r", "\n\r\ndata: B\r\n\r\n"},
			want:   []string{"data: A\r", "\n\r\n", "data: B\r\n\r\n"},
		},
		{
			name:   "bare CR",
			pieces: []string{"data: A\r", "\rdata: B\r\rdata: C\r\r"},
			want:   []string{"data: A\r", "\r", "data: B\r\r", "data: C\r\r"},
		},
		{
			name:   "several events in the read that completes one",
			pieces: []string{"data: A\n", "\ndata: B\n\ndata: C\n", "\n"},
			want:   []string{"data: A\n", "\n", "data: B\n\n", "data: C\n", "\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, readPaced(t, tt.pieces...))
		})
	}
}

// Wherever two upstream reads split a stream, no Read of the paced stream
// returns bytes of two events.
func TestEventPacedStream_NeverJoinsTwoEvents(t *testing.T) {
	for _, blank := range []string{"\n\n", "\r\n\r\n", "\r\r"} {
		events := []string{"data: A" + blank, "event: x" + blank[:len(blank)/2] + "data: B" + blank, "data: C" + blank}
		stream := strings.Join(events, "")
		for cut := 0; cut <= len(stream); cut++ {
			got := readPaced(t, stream[:cut], stream[cut:])

			require.Equal(t, stream, strings.Join(got, ""), "cut at %d", cut)
			at := 0
			for _, read := range got {
				at += len(read)
				crosses := false
				for end := 0; end < len(stream); {
					for _, event := range events {
						end += len(event)
						crosses = crosses || (at > end && at-len(read) < end)
					}
				}
				require.False(t, crosses, "%q holds bytes of two events (cut at %d)", read, cut)
			}
		}
	}
}

// With a stream that ends an event in one read and the blank line's last byte
// in the next, an observer sees each event before the next one is read: the
// value it derives for an event is that event's, not the next one's.
func TestEventPacedStream_ObserverSeesEachEventOfASplitBlankLine(t *testing.T) {
	observer := &collectingObserver{}
	stream := NewObservedSSEStream(NewEventPacedStream(&piecewiseReader{pieces: []string{
		"data: {\"n\":1}\n", "\ndata: {\"n\":2}\n\n",
	}}), observer)
	buf := make([]byte, 1024)
	seen := []int{}
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			seen = append(seen, len(observer.events))
		}
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}

	assert.Equal(t, []int{0, 1, 2}, seen, "events observed after each read")
}

// Observers wrapped above the paced stream see an event only once the events
// before it were read out.
func TestEventPacedStream_ObserversSeeEventsAsTheyAreRead(t *testing.T) {
	observer := &collectingObserver{}
	stream := NewObservedSSEStream(NewEventPacedStream(io.NopCloser(strings.NewReader(
		"data: {\"n\":1}\n\ndata: {\"n\":2}\n\n"))), observer)
	buf := make([]byte, 1024)

	_, err := stream.Read(buf)
	require.NoError(t, err)
	assert.Len(t, observer.events, 1)
	_, err = stream.Read(buf)
	require.NoError(t, err)
	assert.Len(t, observer.events, 2)
}

type collectingObserver struct{ events []map[string]any }

func (o *collectingObserver) OnJSONEvent(payload map[string]any) {
	o.events = append(o.events, payload)
}
func (o *collectingObserver) OnStreamClose() {}
