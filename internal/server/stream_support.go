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

func flushStream(w io.Writer, stream io.Reader) error {
	flusher, canFlush := w.(http.Flusher)
	if canFlush {
		flusher.Flush()
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
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
