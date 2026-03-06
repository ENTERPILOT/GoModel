package providers

import (
	"bytes"
	"io"
)

var responsesDoneMarker = []byte("data: [DONE]\n\n")

// EnsureResponsesDone normalizes Responses API streams so clients always receive
// a terminal data: [DONE] marker, even when the upstream stream ends at EOF.
func EnsureResponsesDone(stream io.ReadCloser) io.ReadCloser {
	if stream == nil {
		return nil
	}

	return &responsesDoneWrapper{
		ReadCloser: stream,
		tail:       make([]byte, 0, len(responsesDoneMarker)-1),
	}
}

type responsesDoneWrapper struct {
	io.ReadCloser
	tail    []byte
	pending []byte
	sawDone bool
	emitted bool
}

func (w *responsesDoneWrapper) Read(p []byte) (int, error) {
	if len(w.pending) > 0 {
		n := copy(p, w.pending)
		w.pending = w.pending[n:]
		if len(w.pending) == 0 {
			w.emitted = true
		}
		return n, nil
	}

	if w.emitted {
		return 0, io.EOF
	}

	n, err := w.ReadCloser.Read(p)
	if n > 0 {
		w.trackDone(p[:n])
	}

	if err == io.EOF {
		if w.sawDone {
			if n > 0 {
				return n, nil
			}
			return 0, io.EOF
		}

		if n > 0 {
			w.pending = append(w.pending[:0], responsesDoneMarker...)
			return n, nil
		}

		n = copy(p, responsesDoneMarker)
		if n < len(responsesDoneMarker) {
			w.pending = append(w.pending[:0], responsesDoneMarker[n:]...)
			return n, nil
		}

		w.emitted = true
		return n, nil
	}

	return n, err
}

func (w *responsesDoneWrapper) trackDone(data []byte) {
	if w.sawDone {
		return
	}

	combined := append(append([]byte(nil), w.tail...), data...)
	if bytes.Contains(combined, responsesDoneMarker) {
		w.sawDone = true
		return
	}

	overlap := len(responsesDoneMarker) - 1
	if len(combined) > overlap {
		combined = combined[len(combined)-overlap:]
	}

	w.tail = append(w.tail[:0], combined...)
}
