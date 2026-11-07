package core

import "io"

// ReadAllSized is io.ReadAll for a reader whose length is declared up front,
// such as an HTTP body with a Content-Length. When size is known (>= 0) and
// at most maxPrealloc, the buffer is allocated once at size+1 bytes, so a body
// of the declared length is read with no growth and no final copy; the extra
// byte lets the terminating EOF be observed without growing.
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
	b := make([]byte, 0, size+1)
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
