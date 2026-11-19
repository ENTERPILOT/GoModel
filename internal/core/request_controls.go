package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// FailoverControlHeader is the request control header that turns failover off
// for one request.
const FailoverControlHeader = "X-GoModel-Failover"

// requestControlHeaders lists every request control header: a header a client
// sends to change a gateway setting for its own request. The gateway reads one
// only when the operator lists it in server.request_control_headers, and never
// forwards any of them to a provider.
var requestControlHeaders = []string{FailoverControlHeader}

// IsRequestControlHeader reports whether name is a request control header,
// ignoring case.
func IsRequestControlHeader(name string) bool {
	return requestControlHeader(name) != ""
}

// NormalizeRequestControlHeaders returns the request control headers named by
// names in their documented spelling, without blanks and duplicates. A name
// that is no request control header is an error.
func NormalizeRequestControlHeaders(names []string) ([]string, error) {
	var out []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		header := requestControlHeader(name)
		if header == "" {
			return nil, fmt.Errorf("unknown request control header %q; supported: %s", name, strings.Join(requestControlHeaders, ", "))
		}
		if !slices.Contains(out, header) {
			out = append(out, header)
		}
	}
	return out, nil
}

func requestControlHeader(name string) string {
	for _, header := range requestControlHeaders {
		if strings.EqualFold(name, header) {
			return header
		}
	}
	return ""
}

// RequestControls holds the settings a client changed for its own request
// through request control headers. The zero value changes nothing.
type RequestControls struct {
	// FailoverOff serves the request without failover. There is no opposite:
	// a client cannot turn on failover that the configuration turned off.
	FailoverOff bool
}

const requestControlsKey contextKey = "request-controls"

// WithRequestControls returns a new context carrying the client's controls.
func WithRequestControls(ctx context.Context, controls RequestControls) context.Context {
	return context.WithValue(ctx, requestControlsKey, controls)
}

// RequestControlsFromContext returns the client's controls for the request.
func RequestControlsFromContext(ctx context.Context) RequestControls {
	if ctx == nil {
		return RequestControls{}
	}
	controls, _ := ctx.Value(requestControlsKey).(RequestControls)
	return controls
}
