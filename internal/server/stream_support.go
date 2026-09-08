package server

import (
	"io"
	"net/http"
	"sync"
)

// streamCopyBufferPool reuses 32KB copy buffers across streaming responses so
// each concurrent stream does not allocate (and later garbage-collect) its own
// buffer. Buffers are pooled by pointer to avoid an allocation on Put.
var streamCopyBufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 32*1024)
		return &buf
	},
}

// flushStream relays stream to w chunk by chunk, flushing after each one. A
// stall during a flush is not returned by the flush itself (the wrappers
// above the stall writer drop flush errors), so the stall writer is asked
// after every flush and at end of stream; otherwise a client that stalled on
// the final chunk would be recorded as a completed response.
func flushStream(w io.Writer, stream io.Reader) error {
	flusher, canFlush := w.(http.Flusher)
	stalls := findStallReporter(w)
	if canFlush {
		flusher.Flush()
		if stalls != nil {
			if stallErr := stalls.StallError(); stallErr != nil {
				return stallErr
			}
		}
	}

	bufPtr := streamCopyBufferPool.Get().(*[]byte)
	defer streamCopyBufferPool.Put(bufPtr)
	buf := *bufPtr
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			if canFlush {
				flusher.Flush()
			}
			if stalls != nil {
				if stallErr := stalls.StallError(); stallErr != nil {
					return stallErr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				if stalls != nil {
					return stalls.StallError()
				}
				return nil
			}
			return err
		}
	}
}

// primedStream is a stream whose first chunk was read ahead of the response
// headers being committed.
type primedStream struct {
	io.ReadCloser
	head []byte
	err  error
}

// primeStream reads the first chunk of stream synchronously and returns a
// stream that replays it before the rest.
func primeStream(stream io.ReadCloser) io.ReadCloser {
	buf := make([]byte, 32*1024)
	n, err := stream.Read(buf)
	return &primedStream{ReadCloser: stream, head: buf[:n], err: err}
}

func (p *primedStream) Read(b []byte) (int, error) {
	if len(p.head) > 0 {
		n := copy(b, p.head)
		p.head = p.head[n:]
		return n, nil
	}
	if p.err != nil {
		err := p.err
		p.err = nil
		return 0, err
	}
	return p.ReadCloser.Read(b)
}
