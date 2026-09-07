package plugins

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"sync"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// RequestState is the per-request plugin state shared by every Exchange built
// for one request: the Values bag, the response headers plugins add, and the
// decisions taken so far.
type RequestState struct {
	mu              sync.Mutex
	Values          pluginapi.Values
	ResponseHeaders http.Header
	Decisions       []DecisionRecord
	upstreamLogged  bool
	// requestHeaders is the editable, redacted copy of the inbound headers
	// shared by every Exchange; originalHeaders is what it started as, so
	// ApplyRequestHeaders can replay only the differences.
	requestHeaders  http.Header
	originalHeaders http.Header
}

// DecisionRecord is one recorded plugin decision.
type DecisionRecord struct {
	Phase    pluginapi.Kind
	Instance string
	Decision pluginapi.Decision
	Err      error
	// Edited reports that the instance's step changed the request or
	// response.
	Edited bool
	// BytesBefore and BytesAfter are the encoded request sizes around the
	// edit, when known.
	BytesBefore int
	BytesAfter  int
}

type requestStateKey struct{}

// WithRequestState ensures ctx carries a RequestState and returns it. The
// request path does not need it: RequestStateFor creates the state on the
// request's workflow, so only tests and callers without a workflow use this.
func WithRequestState(ctx context.Context) (context.Context, *RequestState) {
	if ctx == nil {
		ctx = context.Background()
	}
	if state := RequestStateFromContext(ctx); state != nil {
		return ctx, state
	}
	state := NewRequestState()
	return context.WithValue(ctx, requestStateKey{}, state), state
}

// RequestStateFor returns the request's state, creating it on the request's
// workflow the first time a plugin runs. Every later phase of the request
// finds the same state through RequestStateFromContext. Without a workflow
// the state is not shared; it lives only for the caller.
func RequestStateFor(ctx context.Context) *RequestState {
	if state := RequestStateFromContext(ctx); state != nil {
		return state
	}
	state := NewRequestState()
	if workflow := core.GetWorkflow(ctx); workflow != nil {
		workflow.PluginState = state
	}
	return state
}

// NewRequestState allocates an empty state. Its maps are created on first
// use so a request that runs no plugin pays for nothing but the pointer.
func NewRequestState() *RequestState {
	return &RequestState{}
}

// ensureMaps creates the shared maps; the caller holds s.mu.
func (s *RequestState) ensureMaps() {
	if s.Values == nil {
		s.Values = pluginapi.Values{}
	}
	if s.ResponseHeaders == nil {
		s.ResponseHeaders = http.Header{}
	}
}

// RequestStateFromContext returns the request's state, or nil when no plugin
// has run for the request yet.
func RequestStateFromContext(ctx context.Context) *RequestState {
	if ctx == nil {
		return nil
	}
	if state, ok := ctx.Value(requestStateKey{}).(*RequestState); ok {
		return state
	}
	if workflow := core.GetWorkflow(ctx); workflow != nil {
		state, _ := workflow.PluginState.(*RequestState)
		return state
	}
	return nil
}

// Record appends decisions.
func (s *RequestState) Record(records ...DecisionRecord) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Decisions = append(s.Decisions, records...)
}

// Snapshot returns a copy of the recorded decisions.
func (s *RequestState) Snapshot() []DecisionRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]DecisionRecord(nil), s.Decisions...)
}

// AddResponseHeader appends a response header value.
func (s *RequestState) AddResponseHeader(key, value string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMaps()
	s.ResponseHeaders.Add(key, value)
}

// ApplyResponseHeaders applies the collected headers to a response. A
// header whose only value is the empty string is removed from dst (see
// pluginapi.Headers.Response).
func (s *RequestState) ApplyResponseHeaders(dst http.Header) {
	if s == nil || dst == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, values := range s.ResponseHeaders {
		if len(values) == 0 {
			continue
		}
		canonical := http.CanonicalHeaderKey(key)
		if len(values) == 1 && values[0] == "" {
			delete(dst, canonical)
			continue
		}
		dst[canonical] = append([]string(nil), values...)
	}
}

// NewExchange builds an Exchange bound to the request state: Values and
// Headers.Response are shared with every other Exchange of the request;
// Headers.Request is a redacted copy of the inbound headers.
func (s *RequestState) NewExchange(ctx context.Context, meta pluginapi.Meta) *pluginapi.Exchange {
	if s == nil {
		s = NewRequestState()
	}
	request := s.requestHeadersFor(ctx)
	s.mu.Lock()
	s.ensureMaps()
	values, response := s.Values, s.ResponseHeaders
	s.mu.Unlock()
	return &pluginapi.Exchange{
		Meta:    meta,
		Values:  values,
		Headers: &pluginapi.Headers{Request: request, Response: response},
	}
}

// requestHeadersFor returns the shared editable request header copy, built
// from the inbound snapshot on first use.
func (s *RequestState) requestHeadersFor(ctx context.Context) http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requestHeaders == nil {
		s.originalHeaders = inboundHeaders(ctx)
		s.requestHeaders = s.originalHeaders.Clone()
	}
	return s.requestHeaders
}

// ApplyRequestHeaders replays the request header edits plugins made onto
// dst (the live request headers) and returns the names it changed. Credential
// headers and redacted placeholders are never written.
func (s *RequestState) ApplyRequestHeaders(dst http.Header) []string {
	if s == nil || dst == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requestHeaders == nil {
		return nil
	}
	var changed []string
	for key, values := range s.requestHeaders {
		canonical := http.CanonicalHeaderKey(key)
		if _, secret := credentialHeaders[canonical]; secret || slices.Equal(values, s.originalHeaders[canonical]) {
			continue
		}
		if slices.Contains(values, "[redacted]") {
			continue
		}
		dst[canonical] = append([]string(nil), values...)
		changed = append(changed, canonical)
	}
	for key := range s.originalHeaders {
		if _, secret := credentialHeaders[key]; secret {
			continue
		}
		if _, still := s.requestHeaders[key]; !still {
			delete(dst, key)
			changed = append(changed, key)
		}
	}
	slices.Sort(changed)
	return changed
}

// Finish folds an exchange back into the state after a chain ran: upstream
// headers are logged (not applied in this version).
func (s *RequestState) Finish(x *pluginapi.Exchange) {
	if s == nil || x == nil || x.Headers == nil || len(x.Headers.Upstream) == 0 {
		return
	}
	s.mu.Lock()
	logged := s.upstreamLogged
	s.upstreamLogged = true
	s.mu.Unlock()
	if !logged {
		slog.Debug("plugin set upstream headers; not applied in this version", "request_id", x.Meta.RequestID, "count", len(x.Headers.Upstream))
	}
}

// credentialHeaders are never exposed to plugins.
var credentialHeaders = map[string]struct{}{
	"Authorization":       {},
	"X-Api-Key":           {},
	"Proxy-Authorization": {},
	"Cookie":              {},
}

func inboundHeaders(ctx context.Context) http.Header {
	snapshot := core.GetRequestSnapshot(ctx)
	if snapshot == nil {
		return http.Header{}
	}
	source := snapshot.HeadersView()
	out := make(http.Header, len(source))
	for key, values := range source {
		canonical := http.CanonicalHeaderKey(key)
		if _, secret := credentialHeaders[canonical]; secret {
			out[canonical] = []string{"[redacted]"}
			continue
		}
		out[canonical] = append([]string(nil), values...)
	}
	return out
}
