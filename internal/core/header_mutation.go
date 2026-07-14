package core

import (
	"context"
	"net/http"
	"slices"
	"strings"
)

// HeaderMutation is one concrete set of outbound provider-request header
// changes computed for a request. Values are fully resolved (conditions
// evaluated, from-header copies taken), so applying a mutation is a pure
// header rewrite. Names are canonical MIME header keys.
type HeaderMutation struct {
	Set    map[string]string
	Remove []string
}

// IsZero reports whether the mutation changes nothing.
func (m *HeaderMutation) IsZero() bool {
	return m == nil || (len(m.Set) == 0 && len(m.Remove) == 0)
}

// Merge folds a later mutation into m: later sets win per header, and a later
// set clears an earlier remove of the same header (and vice versa).
func (m *HeaderMutation) Merge(next *HeaderMutation) {
	if m == nil || next.IsZero() {
		return
	}
	for _, name := range next.Remove {
		delete(m.Set, name)
		m.Remove = appendHeaderNameOnce(m.Remove, name)
	}
	for name, value := range next.Set {
		if m.Set == nil {
			m.Set = make(map[string]string, len(next.Set))
		}
		m.Set[name] = value
		m.Remove = removeHeaderName(m.Remove, name)
	}
}

// Apply rewrites h in place. Protected headers (credentials, hop-by-hop,
// framing, and payload metadata) are never touched regardless of what the
// mutation asks for; validation rejects them at authoring time and this guards
// the runtime path.
func (m *HeaderMutation) Apply(h http.Header) {
	if m.IsZero() || h == nil {
		return
	}
	for _, name := range m.Remove {
		if IsProtectedHeader(name) {
			continue
		}
		h.Del(name)
	}
	for name, value := range m.Set {
		if IsProtectedHeader(name) {
			continue
		}
		h.Set(name, value)
	}
}

// protectedTransportHeaders are headers a header-modification rule may never
// set or remove: message framing, hop-by-hop, and payload metadata headers
// whose values the gateway and Go's HTTP transport own.
var protectedTransportHeaders = map[string]struct{}{
	"host":              {},
	"content-length":    {},
	"content-type":      {},
	"content-encoding":  {},
	"accept-encoding":   {},
	"transfer-encoding": {},
	"connection":        {},
	"keep-alive":        {},
	"te":                {},
	"trailer":           {},
	"upgrade":           {},
}

// IsProtectedHeader reports whether a header may never be read or written by
// header-modification rules: credentials plus transport, framing, and payload
// metadata headers.
func IsProtectedHeader(name string) bool {
	if IsCredentialHeader(name) {
		return true
	}
	_, ok := protectedTransportHeaders[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

func appendHeaderNameOnce(names []string, name string) []string {
	if slices.Contains(names, name) {
		return names
	}
	return append(names, name)
}

func removeHeaderName(names []string, name string) []string {
	for i, existing := range names {
		if existing == name {
			return append(names[:i], names[i+1:]...)
		}
	}
	return names
}

// HeaderMutator is one workflow step that conditionally mutates outbound
// provider-request headers. Evaluation is pure: conditions are checked
// against the inbound client headers and the returned mutation carries fully
// resolved values. A nil result means the step did not match.
type HeaderMutator interface {
	Name() string
	HeaderMutation(inbound http.Header) *HeaderMutation
}

type headerMutationKey struct{}

// WithHeaderMutation returns a new context carrying the merged outbound
// header mutation computed by header-modification workflow steps. Empty
// mutations leave the context unchanged.
func WithHeaderMutation(ctx context.Context, mutation *HeaderMutation) context.Context {
	if mutation.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, headerMutationKey{}, mutation)
}

// WithoutHeaderMutation returns a child context that preserves cancellation
// and all other request metadata while preventing request-scoped header rules
// from leaking into auxiliary provider calls.
func WithoutHeaderMutation(ctx context.Context) context.Context {
	if ctx == nil || HeaderMutationFromContext(ctx) == nil {
		return ctx
	}
	return context.WithValue(ctx, headerMutationKey{}, (*HeaderMutation)(nil))
}

// HeaderMutationFromContext retrieves the request's outbound header mutation,
// or nil when no header-modification step matched.
func HeaderMutationFromContext(ctx context.Context) *HeaderMutation {
	if ctx == nil {
		return nil
	}
	mutation, _ := ctx.Value(headerMutationKey{}).(*HeaderMutation)
	return mutation
}
