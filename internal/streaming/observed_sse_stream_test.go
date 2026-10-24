package streaming

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type trackingObserver struct {
	eventCount  int
	lastID      string
	lastPayload map[string]any
	closed      bool
}

func (o *trackingObserver) OnJSONEvent(payload map[string]any) {
	o.eventCount++
	o.lastPayload = payload
	if id, _ := payload["id"].(string); id != "" {
		o.lastID = id
	}
}

func (o *trackingObserver) OnStreamClose() {
	o.closed = true
}

func TestObservedSSEStream_PassesThroughAndFansOut(t *testing.T) {
	streamData := `data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"hi"}}]}

data: {"id":"chatcmpl-2","usage":{"total_tokens":3}}

data: [DONE]

`
	first := &trackingObserver{}
	second := &trackingObserver{}
	stream := NewObservedSSEStream(io.NopCloser(strings.NewReader(streamData)), first, second)

	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.Equal(t, streamData, string(data))
	err = stream.Close()
	require.NoError(t, err)

	for i, observer := range []*trackingObserver{first, second} {
		require.Equal(t, 2, observer.eventCount, "observer %d", i)
		require.Equal(t, "chatcmpl-2", observer.lastID, "observer %d", i)
		require.True(t, observer.closed, "observer %d was not closed", i)
	}
}

func TestObservedSSEStream_ParsesFragmentedFinalEventOnClose(t *testing.T) {
	streamData := `data: {"id":"chatcmpl-frag","usage":{"total_tokens":8}}`
	observer := &trackingObserver{}
	stream := NewObservedSSEStream(io.NopCloser(strings.NewReader(streamData)), observer)

	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.Equal(t, streamData, string(data))
	err = stream.Close()
	require.NoError(t, err)
	require.Equal(t, 1, observer.eventCount)
	require.Equal(t, "chatcmpl-frag", observer.lastID)
	require.True(t, observer.closed)
}

func TestObservedSSEStream_ReassemblesMultilineDataEvent(t *testing.T) {
	streamData := "data: {\"id\":\"chatcmpl-multiline\",\n" +
		"data: \"usage\":{\"total_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	observer := &trackingObserver{}
	stream := NewObservedSSEStream(io.NopCloser(strings.NewReader(streamData)), observer)

	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.Equal(t, streamData, string(data))
	err = stream.Close()
	require.NoError(t, err)
	require.Equal(t, 1, observer.eventCount)
	require.Equal(t, "chatcmpl-multiline", observer.lastID)

	usage, ok := observer.lastPayload["usage"].(map[string]any)
	require.True(t, ok, "usage = %#v, want object", observer.lastPayload["usage"])
	got := usage["total_tokens"]
	require.Equal(t, float64(3), got)
}

func TestObservedSSEStream_DetectsBoundarySplitAcrossReads(t *testing.T) {
	observer := &trackingObserver{}
	s := &ObservedSSEStream{
		observers: []Observer{observer},
	}

	s.processChunk([]byte("data:{\"id\":\"chatcmpl-1\"}\r\n\r"))
	s.processChunk([]byte("\ndata:{\"id\":\"chatcmpl-2\"}\r\n\r\n"))

	require.Equal(t, 2, observer.eventCount)
	require.Equal(t, "chatcmpl-2", observer.lastID)
	require.Empty(t, s.pending)
}

func TestObservedSSEStream_DiscardsOversizedPendingDataWithoutTailCapping(t *testing.T) {
	s := &ObservedSSEStream{
		pending: bytes.Repeat([]byte("a"), maxPendingEventBytes),
	}
	data := bytes.Repeat([]byte("b"), maxPendingEventBytes+1024)

	s.processChunk(data)
	got := len(s.pending)
	require.Equal(t, 0, got)
	require.True(t, s.discarding)
}

func TestObservedSSEStream_DropsOversizedBufferedEventAndResumesWithinSameChunk(t *testing.T) {
	observer := &trackingObserver{}
	s := &ObservedSSEStream{
		observers: []Observer{observer},
		pending: append(
			[]byte("data: {\"id\":\"too-big\",\"payload\":\""),
			bytes.Repeat([]byte("a"), maxPendingEventBytes/2)...,
		),
	}
	data := append(
		append(
			append(
				bytes.Repeat([]byte("b"), maxPendingEventBytes/2+1),
				[]byte("\"}\n\ndata: {\"id\":\"fresh\"}\n\n")...,
			),
			bytes.Repeat([]byte("c"), maxPendingEventBytes+1)...,
		),
		[]byte("ignored-trailer")...,
	)

	s.processChunk(data)

	require.Equal(t, 1, observer.eventCount)
	require.Equal(t, "fresh", observer.lastID)
}

func TestObservedSSEStream_ResumesAfterDiscardWhenBoundarySplitsAcrossReads(t *testing.T) {
	observer := &trackingObserver{}
	s := &ObservedSSEStream{
		observers: []Observer{observer},
	}

	oversized := append(
		append(
			[]byte("data:{\"id\":\"too-big\",\"payload\":\""),
			bytes.Repeat([]byte("x"), maxPendingEventBytes)...,
		),
		[]byte("\"}\r\n\r")...,
	)

	s.processChunk(oversized)
	s.processChunk([]byte("\ndata:{\"id\":\"fresh\"}\r\n\r\n"))

	require.Equal(t, 1, observer.eventCount)
	require.Equal(t, "fresh", observer.lastID)
	require.False(t, s.discarding)
}

func TestObservedSSEStream_DropsOversizedPendingPrefixBeforeCombining(t *testing.T) {
	observer := &trackingObserver{}
	s := &ObservedSSEStream{
		observers: []Observer{observer},
		pending: append(
			[]byte("data: {\"id\":\"stale\"}\n\n"),
			bytes.Repeat([]byte("x"), maxPendingEventBytes)...,
		),
	}

	s.processChunk([]byte("\n\ndata: {\"id\":\"fresh\"}\n\n"))

	require.Equal(t, 1, observer.eventCount)
	require.Equal(t, "fresh", observer.lastID)
	require.Empty(t, s.pending)
}

func TestObservedSSEStream_HandlesCRLFAndDataWithoutSpace(t *testing.T) {
	streamData := "data:{\"id\":\"chatcmpl-1\"}\r\n\r\ndata: {\"id\":\"chatcmpl-2\"}\r\n\r\ndata:[DONE]\r\n\r\n"
	observer := &trackingObserver{}
	stream := NewObservedSSEStream(io.NopCloser(strings.NewReader(streamData)), observer)

	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.Equal(t, streamData, string(data))
	err = stream.Close()
	require.NoError(t, err)
	require.Equal(t, 2, observer.eventCount)
	require.Equal(t, "chatcmpl-2", observer.lastID)
	require.True(t, observer.closed)
}

func TestObservedSSEStream_ParsesCRLFBufferedEventsOnClose(t *testing.T) {
	streamData := "data:{\"id\":\"chatcmpl-1\"}\r\n\r\ndata:{\"id\":\"chatcmpl-2\"}"
	observer := &trackingObserver{}
	stream := NewObservedSSEStream(io.NopCloser(strings.NewReader(streamData)), observer)

	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.Equal(t, streamData, string(data))
	err = stream.Close()
	require.NoError(t, err)
	require.Equal(t, 2, observer.eventCount)
	require.Equal(t, "chatcmpl-2", observer.lastID)
	require.True(t, observer.closed)
}

func TestJoinedSuffix(t *testing.T) {
	tests := []struct {
		name   string
		prefix []byte
		data   []byte
		n      int
		want   []byte
	}{
		{
			name: "returns nil for non-positive length",
			data: []byte("abc"),
			n:    0,
			want: nil,
		},
		{
			name: "returns suffix from data when data is long enough",
			data: []byte("abcdef"),
			n:    3,
			want: []byte("def"),
		},
		{
			name:   "combines prefix tail and data",
			prefix: []byte("abcd"),
			data:   []byte("ef"),
			n:      4,
			want:   []byte("cdef"),
		},
		{
			name:   "uses available prefix bytes only",
			prefix: []byte("ab"),
			data:   []byte("cd"),
			n:      5,
			want:   []byte("abcd"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinedSuffix(tt.prefix, tt.data, tt.n)
			require.Equal(t, tt.want, got)
		})
	}
}

// filteringObserver is a trackingObserver that additionally implements
// EventFilter with a configurable predicate.
type filteringObserver struct {
	trackingObserver
	wants func(raw []byte) bool
}

func (o *filteringObserver) WantsJSONEvent(raw []byte) bool {
	return o.wants(raw)
}

func TestObservedSSEStreamSkipsDecodingWhenNoObserverWantsEvent(t *testing.T) {
	uninterested := &filteringObserver{wants: func([]byte) bool { return false }}

	stream := NewObservedSSEStream(
		io.NopCloser(strings.NewReader("data: {\"a\":1}\n\ndata: {\"b\":2}\n\ndata: [DONE]\n\n")),
		uninterested,
	)
	_, err := io.Copy(io.Discard, stream)
	require.NoError(t, err)
	err = stream.Close()
	require.NoError(t, err)
	got := uninterested.eventCount
	require.Equal(t, 0, got)
	require.True(t, uninterested.closed)
}

func TestObservedSSEStreamDeliversToAllObserversWhenAnyWantsEvent(t *testing.T) {
	uninterested := &filteringObserver{wants: func([]byte) bool { return false }}
	selective := &filteringObserver{wants: func(raw []byte) bool {
		return strings.Contains(string(raw), `"usage"`)
	}}
	plain := &trackingObserver{}

	stream := NewObservedSSEStream(
		io.NopCloser(strings.NewReader(
			"data: {\"a\":1}\n\ndata: {\"usage\":{\"total_tokens\":7}}\n\ndata: [DONE]\n\n",
		)),
		uninterested, selective, plain,
	)
	_, err := io.Copy(io.Discard, stream)
	require.NoError(t, err)
	err = stream.Close()
	require.NoError(t, err)

	// The unfiltered observer forces decoding of every event, so all three
	// observers see both payloads: filters gate decoding, not delivery.
	for name, observer := range map[string]*trackingObserver{
		"uninterested": &uninterested.trackingObserver,
		"selective":    &selective.trackingObserver,
		"plain":        plain,
	} {
		got := observer.eventCount
		require.Equal(t, 2, got, "%s observer", name)
	}
}

func TestObservedSSEStreamDecodesOnlyWantedEventsForFilteredObservers(t *testing.T) {
	selective := &filteringObserver{wants: func(raw []byte) bool {
		return strings.Contains(string(raw), `"usage"`)
	}}

	stream := NewObservedSSEStream(
		io.NopCloser(strings.NewReader(
			"data: {\"a\":1}\n\ndata: {\"usage\":{\"total_tokens\":7}}\n\ndata: [DONE]\n\n",
		)),
		selective,
	)
	_, err := io.Copy(io.Discard, stream)
	require.NoError(t, err)
	err = stream.Close()
	require.NoError(t, err)
	got := selective.eventCount
	require.Equal(t, 1, got)
	_, ok := selective.lastPayload["usage"]
	require.True(t, ok, "delivered event = %#v, want the usage payload", selective.lastPayload)
}
