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

func (o *collectingObserver) OnJSONEvent(payload map[string]any) { o.events = append(o.events, payload) }
func (o *collectingObserver) OnStreamClose()                     {}
