package core

import "io"

// maxInitialReservation bounds the buffer ReadAllSized reserves before any
// bytes arrive. A declared length is only a claim: a client could declare a
// large body, send a prefix and stall while the reservation is retained.
// Longer bodies grow past it as they are read.
const maxInitialReservation = 1 << 20

// ReadAllSized is io.ReadAll for a reader whose length is declared up front,
// such as an HTTP body with a Content-Length. When size is known (>= 0) and
// at most maxPrealloc, the buffer is allocated up front at size+1 bytes, capped
// at maxInitialReservation+1. A body of the declared length up to that cap is
// read with no growth and no final copy; the extra byte lets the terminating
// EOF be observed without growing. Larger declared bodies grow past the
// reservation as they are read.
//
// The declared size is only a hint: the reader is still consumed to EOF and
// the result, including the data returned alongside an error, is exactly what
// io.ReadAll would return. A shorter body yields fewer bytes and a longer one
// grows the buffer. size < 0 (unknown) or size > maxPrealloc reads with
// io.ReadAll, so a client cannot make the caller reserve memory for a length
// it declares but never sends.
func ReadAllSized(r io.Reader, size, maxPrealloc int64) ([]byte, error) {
	if size < 0 || size > maxPrealloc {
		return io.ReadAll(r)
	}
	b := make([]byte, 0, min(size, maxInitialReservation)+1)
	for {
		n, err := r.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if err != nil {
			// Like io.ReadAll, only the bare sentinel ends the read cleanly.
			if err == io.EOF {
				err = nil
			}
			return b, err
		}
		if len(b) == cap(b) {
			// Longer than declared: grow as io.ReadAll does.
			b = append(b, 0)[:len(b)]
		}
	}
}
