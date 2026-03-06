package providers

import (
	"bytes"
	"io"
)

var responsesDoneMarker = []byte("data: [DONE]\n\n")

var responsesDoneLine = []byte("data: [DONE]")

var responsesDataPrefix = []byte("data: ")

var responsesCompletionPatterns = [][]byte{
	[]byte(`"type":"response.completed"`),
	[]byte(`"type":"response.done"`),
}

// EnsureResponsesDone normalizes Responses API streams so clients always receive
// a terminal data: [DONE] marker when the upstream stream reaches a completed
// Responses event but closes at EOF before sending the final marker.
func EnsureResponsesDone(stream io.ReadCloser) io.ReadCloser {
	if stream == nil {
		return nil
	}

	return &responsesDoneWrapper{
		ReadCloser:                 stream,
		atEventBoundary:            true,
		currentLineAtEventBoundary: true,
	}
}

type responsesDoneWrapper struct {
	io.ReadCloser
	lineBuf                    []byte
	pending                    []byte
	sawDone                    bool
	eventCompletedCandidate    bool
	completedEventReadyForDone bool
	atEventBoundary            bool
	currentLineAtEventBoundary bool
	emitted                    bool
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
		w.trackStream(p[:n])
	}

	if err == io.EOF {
		if w.sawDone {
			if n > 0 {
				return n, nil
			}
			return 0, io.EOF
		}

		missingSuffix := w.synthesizeDoneSuffix()
		if len(missingSuffix) == 0 {
			if n > 0 {
				return n, nil
			}
			return 0, io.EOF
		}

		if n > 0 {
			w.pending = append(w.pending[:0], missingSuffix...)
			return n, nil
		}

		n = copy(p, missingSuffix)
		if n < len(missingSuffix) {
			w.pending = append(w.pending[:0], missingSuffix[n:]...)
			return n, nil
		}

		w.emitted = true
		return n, nil
	}

	return n, err
}

func (w *responsesDoneWrapper) trackStream(data []byte) {
	start := 0
	for i, b := range data {
		if b != '\n' {
			continue
		}

		w.lineBuf = append(w.lineBuf, data[start:i]...)
		w.processLine(w.lineBuf)
		w.lineBuf = w.lineBuf[:0]
		start = i + 1
		w.currentLineAtEventBoundary = w.atEventBoundary
	}

	if start < len(data) {
		w.lineBuf = append(w.lineBuf, data[start:]...)
	}
}

func (w *responsesDoneWrapper) processLine(line []byte) {
	line = bytes.TrimSuffix(line, []byte("\r"))
	if len(line) == 0 {
		if w.eventCompletedCandidate {
			w.completedEventReadyForDone = true
		}
		w.eventCompletedCandidate = false
		w.atEventBoundary = true
		return
	}

	if w.completedEventReadyForDone && (!w.currentLineAtEventBoundary || !bytes.Equal(line, responsesDoneLine)) {
		w.completedEventReadyForDone = false
	}

	if w.currentLineAtEventBoundary && bytes.Equal(line, responsesDoneLine) {
		w.sawDone = true
	}

	if bytes.HasPrefix(line, responsesDataPrefix) {
		if isCompletedDataLine(line) {
			w.eventCompletedCandidate = true
		}
	}

	w.atEventBoundary = false
}

func (w *responsesDoneWrapper) synthesizeDoneSuffix() []byte {
	if w.sawDone {
		return nil
	}

	if w.eventCompletedCandidate && len(w.lineBuf) == 0 {
		return append([]byte{'\n'}, responsesDoneMarker...)
	}

	if isCompletedDataLine(w.lineBuf) {
		return append([]byte("\n\n"), responsesDoneMarker...)
	}

	if !w.completedEventReadyForDone {
		return nil
	}

	if len(w.lineBuf) == 0 {
		if w.atEventBoundary {
			return append([]byte(nil), responsesDoneMarker...)
		}
		return nil
	}

	if !w.currentLineAtEventBoundary || !isDoneLinePrefix(w.lineBuf) {
		return nil
	}

	suffix := append([]byte(nil), responsesDoneLine[len(w.lineBuf):]...)
	suffix = append(suffix, '\n', '\n')
	return suffix
}

func isDoneLinePrefix(line []byte) bool {
	if len(line) > len(responsesDoneLine) {
		return false
	}

	return bytes.Equal(line, responsesDoneLine[:len(line)])
}

func isCompletedDataLine(line []byte) bool {
	line = bytes.TrimSuffix(line, []byte("\r"))
	if !bytes.HasPrefix(line, responsesDataPrefix) {
		return false
	}

	payload := line[len(responsesDataPrefix):]
	for _, pattern := range responsesCompletionPatterns {
		if bytes.Contains(payload, pattern) {
			return true
		}
	}

	return false
}
