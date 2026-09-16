package streaming

import "net/http"

// StallReporter is implemented by the server's stall deadline writer, which
// sits beneath the response wrappers whose flush methods return no error.
// It is looked up through the Unwrap chain of whatever writer a handler
// holds, and asked after a flush whether the client stopped reading.
type StallReporter interface {
	StallError() error
}

// FindStallReporter walks the Unwrap chain from w down to the stall writer,
// or returns nil when the route runs without one.
func FindStallReporter(w any) StallReporter {
	for w != nil {
		if reporter, ok := w.(StallReporter); ok {
			return reporter
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = unwrapper.Unwrap()
	}
	return nil
}
