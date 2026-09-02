package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopedValue describes one context value both in its plain-context form and
// through the request scope, so a single table proves the two agree.
type scopedValue struct {
	name  string
	with  func(context.Context) context.Context
	read  func(context.Context) any
	want  any
	empty any
}

func requestScopeValues() []scopedValue {
	snapshot := &RequestSnapshot{Method: "POST", Path: "/v1/chat/completions"}
	prompt := &WhiteBoxPrompt{OperationType: "chat_completions"}
	workflow := &Workflow{RequestID: "wf"}
	strip := map[string]struct{}{"X-Team": {}}
	return []scopedValue{
		{"request id", func(ctx context.Context) context.Context { return WithRequestID(ctx, "req-1") },
			func(ctx context.Context) any { return GetRequestID(ctx) }, "req-1", ""},
		{"snapshot", func(ctx context.Context) context.Context { return WithRequestSnapshot(ctx, snapshot) },
			func(ctx context.Context) any { return GetRequestSnapshot(ctx) }, snapshot, (*RequestSnapshot)(nil)},
		{"white-box prompt", func(ctx context.Context) context.Context { return WithWhiteBoxPrompt(ctx, prompt) },
			func(ctx context.Context) any { return GetWhiteBoxPrompt(ctx) }, prompt, (*WhiteBoxPrompt)(nil)},
		{"workflow", func(ctx context.Context) context.Context { return WithWorkflow(ctx, workflow) },
			func(ctx context.Context) any { return GetWorkflow(ctx) }, workflow, (*Workflow)(nil)},
		{"session id", func(ctx context.Context) context.Context { return WithSessionID(ctx, "sess") },
			func(ctx context.Context) any { return SessionIDFromContext(ctx) }, "sess", ""},
		{"auth key id", func(ctx context.Context) context.Context { return WithAuthKeyID(ctx, "key") },
			func(ctx context.Context) any { return GetAuthKeyID(ctx) }, "key", ""},
		{"allowed models", func(ctx context.Context) context.Context { return WithCredentialAllowedModels(ctx, []string{"gpt-4o"}) },
			func(ctx context.Context) any { return GetCredentialAllowedModels(ctx) }, []string{"gpt-4o"}, []string(nil)},
		{"effective user path", func(ctx context.Context) context.Context { return WithEffectiveUserPath(ctx, "/acme") },
			func(ctx context.Context) any { return GetEffectiveUserPath(ctx) }, "/acme", ""},
		{"user path header name", func(ctx context.Context) context.Context { return WithUserPathHeaderName(ctx, "x-tenant") },
			func(ctx context.Context) any { return UserPathHeaderNameFromContext(ctx) }, "X-Tenant", UserPathHeader},
		{"labels", func(ctx context.Context) context.Context { return WithRequestLabels(ctx, []string{"a", "b"}) },
			func(ctx context.Context) any { return RequestLabelsFromContext(ctx) }, []string{"a", "b"}, []string(nil)},
		{"strip headers", func(ctx context.Context) context.Context { return WithTaggingStripHeaders(ctx, strip) },
			func(ctx context.Context) any { return TaggingStripHeadersFromContext(ctx) }, strip, map[string]struct{}(nil)},
		{"rewrite tokens saved", func(ctx context.Context) context.Context { return WithRewriteTokensSaved(ctx, 42) },
			func(ctx context.Context) any { return RewriteTokensSavedFromContext(ctx) }, 42, 0},
		{"failover used", func(ctx context.Context) context.Context { return WithFailoverUsed(ctx) },
			func(ctx context.Context) any { return GetFailoverUsed(ctx) }, true, false},
	}
}

func TestRequestScopeAccessorsMatchPlainContext(t *testing.T) {
	for _, v := range requestScopeValues() {
		t.Run(v.name, func(t *testing.T) {
			plain := context.Background()
			scoped, _ := WithRequestScope(context.Background())

			assert.Equal(t, v.empty, v.read(plain), "plain: unset")
			assert.Equal(t, v.empty, v.read(scoped), "scoped: unset")
			assert.Equal(t, v.want, v.read(v.with(plain)), "plain: set")
			assert.Equal(t, v.want, v.read(v.with(scoped)), "scoped: set")
		})
	}
}

func TestRequestScopeSeedsFromExistingValues(t *testing.T) {
	// Values installed by an outer middleware before the scope exists must
	// stay visible through the scope.
	for _, v := range requestScopeValues() {
		t.Run(v.name, func(t *testing.T) {
			ctx := v.with(context.Background())
			scoped, scope := WithRequestScope(ctx)
			require.NotNil(t, scope)
			assert.Equal(t, v.want, v.read(scoped))
		})
	}
}

func TestRequestScopeWithKeepsContextValueSemantics(t *testing.T) {
	// With* on a scoped context layers a clone: the parent and any context
	// derived before the call keep the earlier value, and a later With* wins
	// on the derived chain. This is what guardrail-internal calls rely on when
	// they override the effective user path for their own upstream request.
	base, _ := WithRequestScope(context.Background())
	base = WithEffectiveUserPath(base, "/tenant")
	earlier := context.WithValue(base, contextKey("marker"), 1)

	internal := WithEffectiveUserPath(base, "/internal/guardrail")
	assert.Equal(t, "/internal/guardrail", GetEffectiveUserPath(internal))
	assert.Equal(t, "/tenant", GetEffectiveUserPath(base))
	assert.Equal(t, "/tenant", GetEffectiveUserPath(earlier))

	later := WithEffectiveUserPath(internal, "/tenant/child")
	assert.Equal(t, "/tenant/child", GetEffectiveUserPath(later))
	assert.Equal(t, "/internal/guardrail", GetEffectiveUserPath(internal))

	assert.Same(t, RequestScopeFromContext(base), RequestScopeFromContext(earlier))
	assert.NotSame(t, RequestScopeFromContext(base), RequestScopeFromContext(internal))
}

func TestRequestScopeSetMutatesInPlace(t *testing.T) {
	// Set* is the in-place path used by the middleware chain: every context
	// sharing the scope, including ones derived earlier, observes the update.
	ctx, scope := WithRequestScope(context.Background())
	derived := context.WithValue(ctx, contextKey("marker"), 1)

	scope.SetRequestID("req-9")
	scope.SetSessionID("sess")
	scope.SetFailoverUsed()
	assert.Equal(t, "req-9", GetRequestID(derived))
	assert.Equal(t, "sess", SessionIDFromContext(derived))
	assert.True(t, GetFailoverUsed(derived))

	// The same context is returned when a scope is already installed.
	again, sameScope := WithRequestScope(derived)
	assert.Same(t, scope, sameScope)
	assert.Equal(t, derived, again)
}

func TestRequestScopeSettersMirrorWithNoOps(t *testing.T) {
	tests := []struct {
		name  string
		set   func(*RequestScope)
		with  func(context.Context) context.Context
		read  func(context.Context) any
		start func(context.Context) context.Context
	}{
		{"empty session id keeps previous",
			func(s *RequestScope) { s.SetSessionID("") },
			func(ctx context.Context) context.Context { return WithSessionID(ctx, "") },
			func(ctx context.Context) any { return SessionIDFromContext(ctx) },
			func(ctx context.Context) context.Context { return WithSessionID(ctx, "keep") }},
		{"default header name keeps previous",
			func(s *RequestScope) { s.SetUserPathHeaderName(UserPathHeader) },
			func(ctx context.Context) context.Context { return WithUserPathHeaderName(ctx, UserPathHeader) },
			func(ctx context.Context) any { return UserPathHeaderNameFromContext(ctx) },
			func(ctx context.Context) context.Context { return WithUserPathHeaderName(ctx, "X-Tenant") }},
		{"empty labels keep previous",
			func(s *RequestScope) { s.SetLabels(nil) },
			func(ctx context.Context) context.Context { return WithRequestLabels(ctx, nil) },
			func(ctx context.Context) any { return RequestLabelsFromContext(ctx) },
			func(ctx context.Context) context.Context { return WithRequestLabels(ctx, []string{"keep"}) }},
		{"empty strip set keeps previous",
			func(s *RequestScope) { s.SetTaggingStripHeaders(nil) },
			func(ctx context.Context) context.Context { return WithTaggingStripHeaders(ctx, nil) },
			func(ctx context.Context) any { return TaggingStripHeadersFromContext(ctx) },
			func(ctx context.Context) context.Context {
				return WithTaggingStripHeaders(ctx, map[string]struct{}{"X-Keep": {}})
			}},
		{"non-positive rewrite savings keep previous",
			func(s *RequestScope) { s.SetRewriteTokensSaved(0) },
			func(ctx context.Context) context.Context { return WithRewriteTokensSaved(ctx, 0) },
			func(ctx context.Context) any { return RewriteTokensSavedFromContext(ctx) },
			func(ctx context.Context) context.Context { return WithRewriteTokensSaved(ctx, 7) }},
		{"empty allowlist clears",
			func(s *RequestScope) { s.SetCredentialAllowedModels(nil) },
			func(ctx context.Context) context.Context { return WithCredentialAllowedModels(ctx, nil) },
			func(ctx context.Context) any { return GetCredentialAllowedModels(ctx) },
			func(ctx context.Context) context.Context { return WithCredentialAllowedModels(ctx, []string{"gpt-4o"}) }},
		{"empty effective user path clears",
			func(s *RequestScope) { s.SetEffectiveUserPath("") },
			func(ctx context.Context) context.Context { return WithEffectiveUserPath(ctx, "") },
			func(ctx context.Context) any { return GetEffectiveUserPath(ctx) },
			func(ctx context.Context) context.Context { return WithEffectiveUserPath(ctx, "/acme") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plain := tt.with(tt.start(context.Background()))
			scopedCtx, scope := WithRequestScope(tt.start(context.Background()))
			tt.set(scope)
			assert.Equal(t, tt.read(plain), tt.read(scopedCtx), "in-place Set must match plain With")
			assert.Equal(t, tt.read(plain), tt.read(tt.with(scopedCtx)), "scoped With must match plain With")
		})
	}
}

func TestRequestScopeWithWorkflowIsIdempotent(t *testing.T) {
	workflow := &Workflow{RequestID: "wf"}
	ctx, _ := WithRequestScope(context.Background())
	ctx = WithWorkflow(ctx, workflow)
	assert.Equal(t, ctx, WithWorkflow(ctx, workflow), "same workflow must not layer a clone")
}
