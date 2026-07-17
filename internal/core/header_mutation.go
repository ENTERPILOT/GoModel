package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"sort"
	"strings"
)

// HeaderPlan is the immutable, fully resolved set of outbound provider-request
// header changes for one request. Conditions and from-header copies have
// already been evaluated, so applying a plan is a pure egress operation.
// Names are canonical MIME header keys.
type HeaderPlan struct {
	Set    map[string]string
	Remove []string
}

// HeaderMutation is kept as a source-compatible alias for extensions built
// against the initial header-modification preview. New code should use
// HeaderPlan: a plan describes egress intent and is not an ingress mutation.
type HeaderMutation = HeaderPlan

// IsZero reports whether the mutation changes nothing.
func (m *HeaderPlan) IsZero() bool {
	return m == nil || (len(m.Set) == 0 && len(m.Remove) == 0)
}

// Merge folds a later mutation into m: later sets win per header, and a later
// set clears an earlier remove of the same header (and vice versa).
func (m *HeaderPlan) Merge(next *HeaderPlan) {
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
func (m *HeaderPlan) Apply(h http.Header) {
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

// CacheFingerprint returns a stable, non-reversible identity for the effective
// plan. Cache keys vary on resolved values (including from-header values)
// without persisting those values in plaintext.
func (m *HeaderPlan) CacheFingerprint() string {
	if m.IsZero() {
		return ""
	}
	h := sha256.New()
	setNames := make([]string, 0, len(m.Set))
	for name := range m.Set {
		setNames = append(setNames, name)
	}
	sort.Strings(setNames)
	for _, name := range setNames {
		h.Write([]byte("set\x00"))
		h.Write([]byte(strings.ToLower(name)))
		h.Write([]byte{0})
		h.Write([]byte(m.Set[name]))
		h.Write([]byte{0})
	}
	removed := append([]string(nil), m.Remove...)
	sort.Slice(removed, func(i, j int) bool {
		return strings.ToLower(removed[i]) < strings.ToLower(removed[j])
	})
	for _, name := range removed {
		h.Write([]byte("remove\x00"))
		h.Write([]byte(strings.ToLower(name)))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
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

// HeaderPolicy is one workflow egress-policy step. Evaluation is pure:
// conditions are checked against the inbound client headers and the returned
// plan carries fully resolved values. A nil result means the policy did not
// match.
type HeaderPolicy interface {
	Name() string
	ResolveHeaderPlan(input HeaderPolicyInput) *HeaderPlan
}

// HeaderPolicyInput is the immutable request metadata visible to egress-policy
// planning. Policies never receive the mutable inbound *http.Request.
type HeaderPolicyInput struct {
	Headers http.Header
	Method  string
	Path    string
}

// HeaderMutator is kept as a compatibility alias for the preview API.
type HeaderMutator = HeaderPolicy

type headerPlanKey struct{}

// WithHeaderPlan returns a new context carrying the resolved primary-attempt
// header plan. The context seam is intentionally private to gateway execution;
// auxiliary calls and failover attempts strip it explicitly.
func WithHeaderPlan(ctx context.Context, plan *HeaderPlan) context.Context {
	if plan.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, headerPlanKey{}, plan)
}

// WithHeaderMutation returns a new context carrying the merged outbound
// header mutation computed by header-modification workflow steps. Empty
// mutations leave the context unchanged.
func WithHeaderMutation(ctx context.Context, mutation *HeaderMutation) context.Context {
	return WithHeaderPlan(ctx, mutation)
}

// WithoutHeaderPlan prevents a request's primary-attempt plan from leaking
// into an auxiliary call or a failover route.
func WithoutHeaderPlan(ctx context.Context) context.Context {
	if ctx == nil || HeaderPlanFromContext(ctx) == nil {
		return ctx
	}
	return context.WithValue(ctx, headerPlanKey{}, (*HeaderPlan)(nil))
}

// WithoutHeaderMutation returns a child context that preserves cancellation
// and all other request metadata while preventing request-scoped header rules
// from leaking into auxiliary provider calls.
func WithoutHeaderMutation(ctx context.Context) context.Context {
	return WithoutHeaderPlan(ctx)
}

// HeaderPlanFromContext retrieves the resolved primary-attempt plan.
func HeaderPlanFromContext(ctx context.Context) *HeaderPlan {
	if ctx == nil {
		return nil
	}
	plan, _ := ctx.Value(headerPlanKey{}).(*HeaderPlan)
	return plan
}

// HeaderMutationFromContext retrieves the request's outbound header mutation,
// or nil when no header-modification step matched.
func HeaderMutationFromContext(ctx context.Context) *HeaderMutation {
	return HeaderPlanFromContext(ctx)
}
