package mistral

import (
	"bytes"
	"io"

	"github.com/enterpilot/gomodel/internal/streaming"
)

// thinkingStreamMaxEventBytes bounds one SSE event the stream rewrites.
// A larger event is relayed verbatim (unconverted), same as the passthrough
// behavior before this wrapper existed. 4 MiB matches the plugin stream
// transformer's default and far exceeds any real chat delta.
const thinkingStreamMaxEventBytes = 4 << 20

// thinkingChunkStream relays an upstream chat SSE stream, rewriting every
// chunk whose choices[].delta.content is a Mistral chunk array into the
// standard string content + reasoning_content shape. Comments, [DONE],
// oversized events, and chunks that already carry a string content pass
// through byte for byte.
type thinkingChunkStream struct {
	upstream io.ReadCloser
	scanner  streaming.EventScanner
	readBuf  []byte

	out    []byte
	outPos int

	upstreamEOF bool
	readErr     error
	closed      bool
}

func newThinkingChunkStream(upstream io.ReadCloser) io.ReadCloser {
	s := &thinkingChunkStream{
		upstream: upstream,
		readBuf:  make([]byte, 16*1024),
	}
	s.scanner.MaxEventBytes = thinkingStreamMaxEventBytes
	return s
}

func (s *thinkingChunkStream) Read(p []byte) (int, error) {
	if s.closed {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	for s.outPos >= len(s.out) {
		if s.upstreamEOF {
			if s.readErr != nil {
				err := s.readErr
				s.readErr = nil
				return 0, err
			}
			return 0, io.EOF
		}
		s.pump()
	}
	n := copy(p, s.out[s.outPos:])
	s.outPos += n
	return n, nil
}

func (s *thinkingChunkStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.upstream.Close()
}

// pump reads one upstream chunk and emits the events it completes.
func (s *thinkingChunkStream) pump() {
	n, err := s.upstream.Read(s.readBuf)
	if n > 0 {
		for _, raw := range s.scanner.Feed(s.readBuf[:n]) {
			s.emit(raw)
		}
	}
	if err != nil {
		for _, raw := range s.scanner.Flush() {
			s.emit(raw)
		}
		s.upstreamEOF = true
		if err != io.EOF {
			s.readErr = err
		}
	}
}

func (s *thinkingChunkStream) emit(raw streaming.RawEvent) {
	if raw.Comment || raw.Oversized || len(raw.Data) == 0 {
		s.out = append(s.out, raw.Raw...)
		return
	}
	converted := flattenThinkingChunks(raw.Data)
	if bytes.Equal(converted, raw.Data) {
		s.out = append(s.out, raw.Raw...)
		return
	}
	ev := streaming.Event{Name: raw.Name, Data: converted}
	s.out = append(s.out, ev.Encode()...)
}
