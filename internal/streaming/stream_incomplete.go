package streaming

import (
	"errors"
	"fmt"
	"io"
)

// ErrStreamIncomplete marks a provider stream that stopped before its
// terminal event. Errors carrying it came from reading the provider, never
// from writing to the client, so they must not be classified as a client
// disconnect even when they wrap a connection reset.
var ErrStreamIncomplete = errors.New("provider stream ended before completion")

// IncompleteStreamError wraps the read failure that ended a provider stream
// early; a clean close is reported as io.ErrUnexpectedEOF. Both
// ErrStreamIncomplete and the underlying error stay visible to errors.Is.
func IncompleteStreamError(err error) error {
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return fmt.Errorf("%w: %w", ErrStreamIncomplete, err)
}
