package edenai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// embeddingRequest is the smallest request that reaches the wire, used by the
// tests that care about which headers the transport attached rather than about
// the response body.
func embeddingRequest() *core.EmbeddingRequest {
	return &core.EmbeddingRequest{Model: slashedModel, Input: "hello"}
}

// TestCredentialSafeURL covers the single predicate both the header setter and
// the redirect policy consult, so the rule is pinned in one place: TLS is
// always safe, cleartext is safe only when it cannot leave the machine.
func TestCredentialSafeURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"default eden endpoint", defaultBaseURL, true},
		{"custom https endpoint", "https://eden.example.com/v3", true},
		{"https uppercase scheme", "HTTPS://eden.example.com/v3", true},
		{"cleartext public host", "http://eden.example.com/v3", false},
		{"cleartext ip", "http://203.0.113.10/v3", false},
		{"cleartext loopback name", "http://localhost:8080/v3", true},
		{"cleartext loopback v4", "http://127.0.0.1:8080/v3", true},
		{"cleartext loopback v4 alt", "http://127.10.20.30:8080/v3", true},
		{"cleartext loopback v6", "http://[::1]:8080/v3", true},
		{"no scheme", "//eden.example.com/v3", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := url.Parse(tc.raw)
			require.NoError(t, err, "url.Parse(%q)", tc.raw)
			assert.Equal(t, tc.want, secureDestination(parsed), "secureDestination(%q)", tc.raw)
		})
	}
}

// TestCredentialSafeURL_NilIsUnsafe asserts the predicate fails closed. A
// request with no parsable URL must not be treated as a TLS destination.
func TestCredentialSafeURL_NilIsUnsafe(t *testing.T) {
	assert.False(t, secureDestination(nil), "secureDestination(nil) = true, want false: the predicate must fail closed")
}

// TestSetHeaders_SendsCredentialOverHTTPS asserts the ordinary case still
// authenticates: an HTTPS destination gets the bearer token.
func TestSetHeaders_SendsCredentialOverHTTPS(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, defaultBaseURL+"/chat/completions", nil)
	require.NoError(t, err, "http.NewRequest")
	setHeaders(req, "eden-key")

	assert.Equal(t, "Bearer eden-key", req.Header.Get("Authorization"))
}

// TestSetHeaders_WithholdsCredentialOverCleartext is the base-URL half of the
// credential guarantee: an operator-supplied http:// endpoint must not put the
// Eden key on the wire in plain text.
func TestSetHeaders_WithholdsCredentialOverCleartext(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://eden.example.com/v3/chat/completions", nil)
	require.NoError(t, err, "http.NewRequest")
	setHeaders(req, "eden-key")

	assert.Empty(t, req.Header.Get("Authorization"), "the credential must not be sent in cleartext")
}

// TestSetHeaders_SendsCredentialOverLoopback pins the exemption that keeps a
// local Eden-compatible proxy — and every httptest server in this package —
// working. Cleartext to the local machine never reaches a network.
func TestSetHeaders_SendsCredentialOverLoopback(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v3/chat/completions", nil)
	require.NoError(t, err, "http.NewRequest")
	setHeaders(req, "eden-key")

	assert.Equal(t, "Bearer eden-key", req.Header.Get("Authorization"))
}

// TestCleartextBaseURL_RequestRefusedBeforeSending is the payload half of the
// cleartext guarantee, and the reason withholding the credential is not enough
// on its own.
//
// A request to a non-loopback http:// endpoint is written to the socket in
// full before the upstream can reject it, so an unauthenticated send still
// discloses the prompt — and on the embeddings surface, the input text — to
// anyone on the path. Nothing may reach the endpoint at all.
//
// The server answers on a loopback address but is addressed through a
// public-looking host, which is what a misconfigured or hijacked
// EDENAI_BASE_URL would look like.
func TestCleartextBaseURL_RequestRefusedBeforeSending(t *testing.T) {
	var hits int
	var gotAuth, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[]}`))
	}))
	defer server.Close()

	target, err := url.Parse(server.URL)
	require.NoError(t, err, "url.Parse")
	client := &http.Client{Transport: &cleartextRouteTransport{cleartext: target.Host}}

	provider := newTestProvider("eden-key", "http://eden.example.com/v3", client, llmclient.Hooks{})
	_, err = provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    slashedModel,
		Messages: []core.Message{{Role: "user", Content: "secret-prompt"}},
	})
	require.Error(t, err, "ChatCompletion succeeded against a cleartext endpoint, want the request refused")

	assert.Zero(t, hits, "cleartext endpoint received requests, want 0")
	assert.Empty(t, gotAuth, "cleartext endpoint saw an Authorization header, want empty")
	assert.NotContains(t, gotBody, "secret-prompt", "cleartext endpoint received the prompt body; the payload must never be sent")
	// The refusal must name the destination without quoting the whole URL. It
	// is the guard's own error, reached through the *url.Error net/http wraps
	// it in: llmclient deliberately keeps upstream details out of the
	// client-facing message and retains the cause only through Unwrap.
	var urlErr *url.Error
	require.ErrorAs(t, err, &urlErr, "want the transport refusal retained as the cause")
	assert.ErrorContains(t, urlErr.Err, "eden.example.com", "the refusal should name the refused host")
}

// TestCleartextLoopback_RequestStillSent pins the other side of the exemption:
// a cleartext endpoint on the local machine is allowed through and still
// authenticates, which is what keeps a local Eden-compatible proxy usable and
// what every httptest-backed test in this package relies on.
func TestCleartextLoopback_RequestStillSent(t *testing.T) {
	var hits int
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","model":"openai/text-embedding-3-small","data":[]}`))
	}))
	defer server.Close()

	provider := newTestProvider("eden-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.Embeddings(context.Background(), embeddingRequest())
	require.NoError(t, err, "Embeddings against a loopback endpoint")
	assert.Equal(t, 1, hits, "loopback endpoint request count")
	assert.Equal(t, "Bearer eden-key", gotAuth, "loopback endpoint Authorization header")
}

// TestSecureTransport_RoundTrip covers the guard directly, including that an
// allowed request is handed to the underlying transport unchanged and that a
// nil base delegates to http.DefaultTransport the way net/http does.
func TestSecureTransport_RoundTrip(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		wantCalled bool
	}{
		{"https", "https://api.edenai.run/v3/models", true},
		{"loopback http", "http://127.0.0.1:9999/v3/models", true},
		{"loopback name", "http://localhost:9999/v3/models", true},
		{"public cleartext", "http://eden.example.com/v3/models", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &recordingRoundTripper{}
			transport := &secureTransport{base: stub}

			req, err := http.NewRequest(http.MethodGet, tc.target, nil)
			require.NoError(t, err, "http.NewRequest")
			resp, err := transport.RoundTrip(req)
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}

			if tc.wantCalled {
				assert.NotZero(t, stub.calls, "underlying transport was never called, want the request passed through")
				require.NoError(t, err, "RoundTrip(%q), want the request passed through", tc.target)
			} else {
				assert.Zero(t, stub.calls, "underlying transport was called, want the request refused before it")
				require.Error(t, err, "RoundTrip(%q) = nil error, want a refusal", tc.target)
			}
		})
	}
}

// TestSecureTransport_NilBaseUsesDefaultTransport asserts the nil-base fallback
// is wired: http.DefaultClient carries a nil Transport, and Eden's guard wraps
// exactly that when a caller hands it in through ProviderOptions.HTTPClient.
func TestSecureTransport_NilBaseUsesDefaultTransport(t *testing.T) {
	transport := &secureTransport{}

	// A refused destination never reaches the base, so it proves the guard runs
	// without needing a live server for the delegating case.
	req, err := http.NewRequest(http.MethodGet, "http://eden.example.com/v3", nil)
	require.NoError(t, err, "http.NewRequest")
	_, err = transport.RoundTrip(req)
	require.Error(t, err, "RoundTrip = nil error for a cleartext target, want a refusal")

	// An allowed loopback destination must reach the network through
	// http.DefaultTransport rather than panicking on the nil base.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	req, err = http.NewRequest(http.MethodGet, server.URL, nil)
	require.NoError(t, err, "http.NewRequest")
	resp, err := transport.RoundTrip(req)
	require.NoError(t, err, "RoundTrip through the nil base")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

// recordingRoundTripper counts the requests that made it past the guard.
type recordingRoundTripper struct {
	calls int
}

func (r *recordingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls++
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Header:     make(http.Header),
	}, nil
}

// cleartextRouteTransport routes cleartext requests to a fixed address while
// leaving the request URL's host alone, and sends everything else through base
// (http.DefaultTransport when nil). It lets a test address a public-looking
// http:// host — which is what the credential guard and the redirect guard both
// screen on — while still reaching a local test server, with no DNS involved.
type cleartextRouteTransport struct {
	base      http.RoundTripper
	cleartext string
}

func (t *cleartextRouteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	routed := req.Clone(req.Context())
	if routed.URL.Scheme == "http" {
		routed.URL.Host = t.cleartext
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(routed)
}

// TestCompatibleConfig_InstallsRedirectGuardForDefaultAndCallerClients asserts
// neither kind of client can reach the network without the redirect policy.
// New builds its transport through compatibleConfig whether opts.HTTPClient is
// nil (the gateway default) or caller-supplied, so covering compatibleConfig
// here covers both.
func TestCompatibleConfig_InstallsRedirectGuardForDefaultAndCallerClients(t *testing.T) {
	tests := []struct {
		name   string
		client *http.Client
	}{
		{"gateway default client", nil},
		{"caller-supplied client", &http.Client{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := compatibleConfig(defaultBaseURL, tc.client)
			require.NotNil(t, cfg.HTTPClient, "HTTPClient = nil, want a client carrying the redirect guard")
			assert.NotNil(t, cfg.HTTPClient.CheckRedirect, "CheckRedirect = nil, want Eden's redirect guard installed")
		})
	}
}

// TestGuardedHTTPClient_DoesNotMutateCallerClient asserts the caller's client
// is copied rather than reconfigured. A client shared with another provider
// must keep its own redirect behavior.
func TestGuardedHTTPClient_DoesNotMutateCallerClient(t *testing.T) {
	caller := &http.Client{}
	guarded := guardedHTTPClient(caller)

	assert.Nil(t, caller.CheckRedirect, "caller's CheckRedirect was set; the guard must be installed on a copy")
	assert.NotSame(t, caller, guarded, "guardedHTTPClient returned the caller's client; want a copy")
	assert.NotNil(t, guarded.CheckRedirect, "guarded client has no CheckRedirect")
}

// TestGuardedHTTPClient_WrapsCallerTransport asserts the guard is layered over
// the caller's transport rather than replacing it, so a caller that configured
// TLS roots or a proxy (and httptest's own client) still reaches its server.
func TestGuardedHTTPClient_WrapsCallerTransport(t *testing.T) {
	transport := &http.Transport{}
	caller := &http.Client{Transport: transport}

	guarded := guardedHTTPClient(caller)
	wrapped, ok := guarded.Transport.(*secureTransport)
	require.True(t, ok, "Transport = %T, want *secureTransport wrapping the caller's", guarded.Transport)
	assert.Same(t, transport, wrapped.base, "wrapped base should be the caller's transport preserved")
	assert.Same(t, transport, caller.Transport, "the caller's client was modified; the guard must go on a copy")
}

// TestCheckRedirect_AllowsSameHostHTTPS asserts the one redirect shape a REST
// endpoint plausibly returns keeps working: a path change on the host the
// request was already addressed to.
func TestCheckRedirect_AllowsSameHostHTTPS(t *testing.T) {
	origin := mustRequest(t, "https://api.edenai.run/v3/chat/completions")
	target := mustRequest(t, "https://api.edenai.run/v3/chat/completions-moved")

	require.NoError(t, checkRedirect(target, []*http.Request{origin}), "checkRedirect same-host")
	// Host comparison is case-insensitive, as hostnames are.
	upper := mustRequest(t, "https://API.EdenAI.run/v3/chat/completions-moved")
	require.NoError(t, checkRedirect(upper, []*http.Request{origin}), "checkRedirect differing-case host")
}

// TestCheckRedirect_RefusesCrossHostHTTPS covers the credential-exposure path
// that TLS alone does not close. Go's default policy forwards Authorization to
// a subdomain of the original host, so an upstream-chosen Location is otherwise
// enough to hand the Eden key to a neighbouring name — and following the hop
// would ship the request body there too.
func TestCheckRedirect_RefusesCrossHostHTTPS(t *testing.T) {
	origin := mustRequest(t, "https://api.edenai.run/v3/chat/completions")

	for _, target := range []string{
		"https://evil.api.edenai.run/v3/chat/completions", // subdomain: Go would forward the token
		"https://eu.edenai.run/v3/chat/completions",       // sibling host
		"https://attacker.example.com/v3/chat/completions",
	} {
		req := mustRequest(t, target)
		err := checkRedirect(req, []*http.Request{origin})
		assert.ErrorContains(t, err, "cross-host", "checkRedirect(%q), want a cross-host refusal", target)
	}
}

// TestCheckRedirect_ComparesAgainstOriginalHost asserts a chain cannot walk off
// the configured host one hop at a time: the check is against the request the
// caller made, not the previous hop.
func TestCheckRedirect_ComparesAgainstOriginalHost(t *testing.T) {
	origin := mustRequest(t, "https://api.edenai.run/v3/models")
	hop := mustRequest(t, "https://api.edenai.run/v3/models-moved")
	target := mustRequest(t, "https://elsewhere.edenai.run/v3/models")

	require.Error(t, checkRedirect(target, []*http.Request{origin, hop}), "checkRedirect = nil for a second hop leaving the original host, want a refusal")
}

// TestRedirectPolicy_PreservesCallerPolicy asserts the guard composes with the
// policy a caller's client already carried instead of replacing it. Overwriting
// the field would silently drop the caller's rules, letting an upstream Location
// reach a destination the caller had rejected.
func TestRedirectPolicy_PreservesCallerPolicy(t *testing.T) {
	origin := mustRequest(t, "https://api.edenai.run/v3/models")
	target := mustRequest(t, "https://api.edenai.run/v3/models-moved")
	callerErr := errors.New("caller rejected this destination")

	var called int
	policy := redirectPolicy(func(*http.Request, []*http.Request) error {
		called++
		return callerErr
	})

	// Eden allows this same-host hop, so the caller's policy decides.
	err := policy(target, []*http.Request{origin})
	require.ErrorIs(t, err, callerErr, "want the caller's error returned verbatim")
	assert.Equal(t, 1, called, "caller policy invocation count")
}

// TestRedirectPolicy_PropagatesErrUseLastResponse asserts the sentinel a caller
// uses to stop following redirects survives composition. Swallowing it would
// turn "hand me the redirect response" into "follow the redirect".
func TestRedirectPolicy_PropagatesErrUseLastResponse(t *testing.T) {
	origin := mustRequest(t, "https://api.edenai.run/v3/models")
	target := mustRequest(t, "https://api.edenai.run/v3/models-moved")

	policy := redirectPolicy(func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	})

	err := policy(target, []*http.Request{origin})
	assert.ErrorIs(t, err, http.ErrUseLastResponse, "want http.ErrUseLastResponse propagated")
}

// TestRedirectPolicy_RecheckAfterCallerMutation is the check-then-mutate case.
//
// net/http passes CheckRedirect the very request it is about to send and
// honors edits to it, so a callback that approves a hop and rewrites req.URL
// on the way out would move the request past checks that already ran. Eden's
// rules are therefore re-applied to whatever the callback leaves behind.
func TestRedirectPolicy_RecheckAfterCallerMutation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*http.Request)
		wantErr string
	}{
		{
			name: "rewritten to another https host",
			mutate: func(req *http.Request) {
				req.URL.Host = "attacker.example.com"
			},
			wantErr: "cross-host",
		},
		{
			name: "rewritten to a subdomain of the original host",
			mutate: func(req *http.Request) {
				req.URL.Host = "evil.api.edenai.run"
			},
			wantErr: "cross-host",
		},
		{
			name: "downgraded to cleartext",
			mutate: func(req *http.Request) {
				req.URL.Scheme = "http"
			},
			wantErr: "cleartext",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			origin := mustRequest(t, "https://api.edenai.run/v3/models")
			// A target Eden approves on its own, so only the mutation can
			// make it fail.
			target := mustRequest(t, "https://api.edenai.run/v3/models-moved")

			policy := redirectPolicy(func(req *http.Request, _ []*http.Request) error {
				tc.mutate(req)
				return nil
			})

			err := policy(target, []*http.Request{origin})
			require.Error(t, err, "policy = nil, want a refusal after the callback rewrote the target to %s", target.URL)
			assert.ErrorContains(t, err, tc.wantErr, "want an error mentioning %q", tc.wantErr)
		})
	}
}

// TestRedirectPolicy_AllowsHarmlessCallerMutation asserts the re-check is not
// blanket paranoia: a callback that rewrites only the path, staying on the
// configured host over TLS, is still allowed through.
func TestRedirectPolicy_AllowsHarmlessCallerMutation(t *testing.T) {
	origin := mustRequest(t, "https://api.edenai.run/v3/models")
	target := mustRequest(t, "https://api.edenai.run/v3/models-moved")

	policy := redirectPolicy(func(req *http.Request, _ []*http.Request) error {
		req.URL.Path = "/v3/models-rewritten"
		return nil
	})

	require.NoError(t, policy(target, []*http.Request{origin}), "a same-host path rewrite is fine")
}

// TestRedirectPolicy_CallerErrorsPropagateUnchanged asserts every non-nil
// result from the callback reaches net/http exactly as returned, so the
// re-check cannot convert a caller's decision into a different outcome.
// http.ErrUseLastResponse matters most: net/http reads it as "stop here and
// return the redirect response", not as a failure.
func TestRedirectPolicy_CallerErrorsPropagateUnchanged(t *testing.T) {
	sentinel := errors.New("caller rejected this destination")

	tests := []struct {
		name string
		err  error
	}{
		{"use last response", http.ErrUseLastResponse},
		{"caller error", sentinel},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			origin := mustRequest(t, "https://api.edenai.run/v3/models")
			target := mustRequest(t, "https://api.edenai.run/v3/models-moved")

			policy := redirectPolicy(func(*http.Request, []*http.Request) error {
				return tc.err
			})
			err := policy(target, []*http.Request{origin})
			assert.ErrorIs(t, err, tc.err, "want %v returned unchanged", tc.err)
		})
	}
}

// TestRedirectPolicy_CallerErrorSurvivesAMutation asserts the error path wins
// over the re-check: a callback that both rewrites the target and returns a
// sentinel must have its sentinel propagated, not replaced by Eden's refusal.
func TestRedirectPolicy_CallerErrorSurvivesAMutation(t *testing.T) {
	origin := mustRequest(t, "https://api.edenai.run/v3/models")
	target := mustRequest(t, "https://api.edenai.run/v3/models-moved")

	policy := redirectPolicy(func(req *http.Request, _ []*http.Request) error {
		req.URL.Host = "attacker.example.com"
		return http.ErrUseLastResponse
	})

	err := policy(target, []*http.Request{origin})
	assert.ErrorIs(t, err, http.ErrUseLastResponse, "want http.ErrUseLastResponse propagated unchanged")
}

// TestRedirectPolicy_EdenRefusalWinsOverPermissiveCaller asserts a caller
// cannot waive Eden's guarantees: Eden's rules run first and a refusal is
// final, so a policy that approves everything still cannot allow a cleartext or
// cross-host hop.
func TestRedirectPolicy_EdenRefusalWinsOverPermissiveCaller(t *testing.T) {
	origin := mustRequest(t, "https://api.edenai.run/v3/models")

	var called int
	policy := redirectPolicy(func(*http.Request, []*http.Request) error {
		called++
		return nil
	})

	for _, target := range []string{
		"http://api.edenai.run/v3/models",       // cleartext downgrade
		"https://evil.api.edenai.run/v3/models", // cross-host
	} {
		require.Error(t, policy(mustRequest(t, target), []*http.Request{origin}), "policy(%q) = nil, want Eden's refusal to stand", target)
	}
	assert.Zero(t, called, "caller policy was invoked, want 0 calls: Eden refuses before delegating")
}

// TestRedirectPolicy_NilCallerAllowsEdenApprovedHop asserts the common case —
// a client with no policy of its own — still follows an Eden-approved redirect.
func TestRedirectPolicy_NilCallerAllowsEdenApprovedHop(t *testing.T) {
	origin := mustRequest(t, "https://api.edenai.run/v3/models")
	target := mustRequest(t, "https://api.edenai.run/v3/models-moved")

	require.NoError(t, redirectPolicy(nil)(target, []*http.Request{origin}), "redirectPolicy(nil), want nil for a same-host TLS hop")
}

// TestGuardedHTTPClient_ComposesCallerRedirectPolicy asserts the composition is
// actually wired by the constructor, not only available as a helper.
func TestGuardedHTTPClient_ComposesCallerRedirectPolicy(t *testing.T) {
	var called bool
	caller := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		called = true
		return nil
	}}

	guarded := guardedHTTPClient(caller)
	origin := mustRequest(t, "https://api.edenai.run/v3/models")
	target := mustRequest(t, "https://api.edenai.run/v3/models-moved")

	require.NoError(t, guarded.CheckRedirect(target, []*http.Request{origin}), "CheckRedirect")
	assert.True(t, called, "the caller's redirect policy was not invoked; the guard must compose, not replace")
}

// mustRequest builds a GET request for a URL a test controls.
func mustRequest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	require.NoError(t, err, "http.NewRequest(%q)", rawURL)
	return req
}

// TestCheckRedirect_RefusesSchemeDowngrade is the core of the redirect
// guarantee. net/http drops Authorization only when the redirect target is a
// different host; it does not consider the scheme, so a same-host HTTPS -> HTTP
// redirect would otherwise forward the bearer token in the clear.
func TestCheckRedirect_RefusesSchemeDowngrade(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://api.edenai.run/v3/chat/completions", nil)
	require.NoError(t, err, "http.NewRequest")

	err = checkRedirect(req, nil)
	require.Error(t, err, "checkRedirect = nil, want a refusal for a cleartext redirect target")
	assert.ErrorContains(t, err, "api.edenai.run", "the error should name the refused host")
}

// TestCheckRedirect_ErrorOmitsCredentials asserts the refusal message cannot
// leak a secret carried in the redirect URL's userinfo or query string.
func TestCheckRedirect_ErrorOmitsCredentials(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://user:s3cret@api.edenai.run/v3?api_key=leaked", nil)
	require.NoError(t, err, "http.NewRequest")

	err = checkRedirect(req, nil)
	require.Error(t, err, "checkRedirect = nil, want a refusal")
	for _, secret := range []string{"s3cret", "leaked"} {
		assert.NotContains(t, err.Error(), secret, "error leaked %q from the redirect URL", secret)
	}
}

// TestCheckRedirect_EnforcesRedirectBudget asserts installing a policy did not
// silently remove net/http's own protection against endless redirect chains.
func TestCheckRedirect_EnforcesRedirectBudget(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, defaultBaseURL, nil)
	require.NoError(t, err, "http.NewRequest")
	via := make([]*http.Request, maxRedirects)

	require.Error(t, checkRedirect(req, via), "checkRedirect with %d prior hops = nil, want the redirect budget enforced", maxRedirects)
}

// TestRedirect_HTTPSToHTTPDoesNotForwardCredential exercises the guard through
// a real transport: an HTTPS Eden endpoint that redirects to a cleartext host
// must not deliver the bearer token there.
//
// The redirect names a non-loopback host on purpose. Cleartext to loopback is
// deliberately allowed (see TestRedirect_CleartextLoopbackStillFollowed), so
// pointing this at the test server's own 127.0.0.1 address would exercise the
// exemption instead of the guard.
func TestRedirect_HTTPSToHTTPDoesNotForwardCredential(t *testing.T) {
	var cleartextAuth string
	var cleartextHits int
	cleartext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleartextHits++
		cleartextAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer cleartext.Close()

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://eden.example.com/v3/embeddings", http.StatusFound)
	}))
	defer secure.Close()

	cleartextTarget, err := url.Parse(cleartext.URL)
	require.NoError(t, err, "url.Parse")
	client := secure.Client()
	client.Transport = &cleartextRouteTransport{
		base:      client.Transport,
		cleartext: cleartextTarget.Host,
	}

	provider := newTestProvider("eden-key", secure.URL+"/v3", client, llmclient.Hooks{})
	_, err = provider.Embeddings(context.Background(), embeddingRequest())
	require.Error(t, err, "Embeddings succeeded through an HTTPS -> HTTP redirect, want the redirect refused")

	assert.Zero(t, cleartextHits, "cleartext endpoint received requests, want 0")
	assert.Empty(t, cleartextAuth, "cleartext endpoint saw an Authorization header, want empty")
}

// TestRedirect_CleartextLoopbackStillFollowed pins the exemption's scope: a
// cleartext redirect that stays on the local machine is followed and still
// authenticates, which is what keeps a local Eden-compatible proxy usable.
func TestRedirect_CleartextLoopbackStillFollowed(t *testing.T) {
	var finalAuth string
	var served bool

	mux := http.NewServeMux()
	mux.HandleFunc("/v3/embeddings", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/v3/embeddings-moved", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/v3/embeddings-moved", func(w http.ResponseWriter, r *http.Request) {
		served = true
		finalAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","model":"openai/text-embedding-3-small","data":[]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	provider := newTestProvider("eden-key", server.URL+"/v3", server.Client(), llmclient.Hooks{})
	_, err := provider.Embeddings(context.Background(), embeddingRequest())
	require.NoError(t, err, "Embeddings through a loopback redirect")
	require.True(t, served, "redirect target was never reached")
	assert.Equal(t, "Bearer eden-key", finalAuth, "redirect target Authorization header")
}

// TestRedirect_HTTPSToHTTPSStillFollowed asserts the guard is narrow: a
// same-host TLS redirect is followed and still authenticates, so a legitimate
// custom HTTPS endpoint that redirects keeps working.
func TestRedirect_HTTPSToHTTPSStillFollowed(t *testing.T) {
	var finalAuth string
	var served bool

	mux := http.NewServeMux()
	mux.HandleFunc("/v3/embeddings", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/v3/embeddings-moved", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/v3/embeddings-moved", func(w http.ResponseWriter, r *http.Request) {
		served = true
		finalAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","model":"openai/text-embedding-3-small","data":[],"usage":{"prompt_tokens":9,"total_tokens":9}}`))
	})
	secure := httptest.NewTLSServer(mux)
	defer secure.Close()

	provider := newTestProvider("eden-key", secure.URL+"/v3", secure.Client(), llmclient.Hooks{})
	resp, err := provider.Embeddings(context.Background(), embeddingRequest())
	require.NoError(t, err, "Embeddings through an HTTPS -> HTTPS redirect")
	require.True(t, served, "redirect target was never reached")
	require.NotNil(t, resp, "response = nil")
	assert.Equal(t, "Bearer eden-key", finalAuth, "redirect target Authorization header")
}

// TestRedirect_HTTPSSubdomainDoesNotForwardCredential is the end-to-end form of
// the cross-host rule, against a real TLS server and a real redirect.
//
// Both hops are served by the same httptest instance, addressed through two
// different hostnames so Go sees a genuine subdomain redirect — the case where
// its default policy forwards Authorization. Neither the credential nor the
// request may reach the second name.
func TestRedirect_HTTPSSubdomainDoesNotForwardCredential(t *testing.T) {
	var secondHopHits int
	var secondHopAuth string

	mux := http.NewServeMux()
	mux.HandleFunc("/v3/embeddings", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.eden.test/v3/stolen", http.StatusFound)
	})
	mux.HandleFunc("/v3/stolen", func(w http.ResponseWriter, r *http.Request) {
		secondHopHits++
		secondHopAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()

	target, err := url.Parse(server.URL)
	require.NoError(t, err, "url.Parse")
	client := server.Client()
	// Route both hostnames to the one test server; the TLS config from
	// server.Client() already trusts its certificate.
	client.Transport = &hostPinnedTransport{base: client.Transport, addr: target.Host}

	provider := newTestProvider("eden-key", "https://eden.test/v3", client, llmclient.Hooks{})
	_, err = provider.Embeddings(context.Background(), embeddingRequest())
	require.Error(t, err, "Embeddings followed an HTTPS subdomain redirect, want it refused")

	assert.Zero(t, secondHopHits, "subdomain endpoint received requests, want 0")
	assert.Empty(t, secondHopAuth, "subdomain endpoint saw an Authorization header, want empty: the Eden key must not follow an upstream-chosen host")
}

// hostPinnedTransport dials one fixed address whatever hostname the request
// carries, so a test can exercise multi-host redirect rules against a single
// server without DNS. The request URL's host is left intact, which is what the
// redirect policy inspects.
type hostPinnedTransport struct {
	base http.RoundTripper
	addr string
}

func (t *hostPinnedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	routed := req.Clone(req.Context())
	routed.URL.Host = t.addr
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(routed)
}

// TestDefaultBaseURLIsHTTPS guards the default every deployment uses. The
// credential guard keys off the request scheme, so a default that regressed to
// http:// would silently withhold the API key on ordinary traffic.
// TestNew_DefaultsBaseURL already covers New resolving to this value.
func TestDefaultBaseURLIsHTTPS(t *testing.T) {
	require.True(t, strings.HasPrefix(defaultBaseURL, "https://"), "defaultBaseURL = %q, want an https:// endpoint", defaultBaseURL)
}

// TestSetBaseURL_OverridesResolvedEndpoint covers the public base-URL override,
// which the credential guard then reads at request time rather than using the
// endpoint the provider was constructed with.
func TestSetBaseURL_OverridesResolvedEndpoint(t *testing.T) {
	provider, ok := New(providers.ProviderConfig{APIKey: "eden-key"}, providers.ProviderOptions{}).(*Provider)
	require.True(t, ok, "New did not return *Provider")

	const override = "https://eden.eu.example.com/v3"
	provider.SetBaseURL(override)
	assert.Equal(t, override, provider.GetBaseURL(), "GetBaseURL() after SetBaseURL")
}

// TestGuardedHTTPClient_PreservesDefaultClientSemantics pins what happens when
// a caller hands New http.DefaultClient through ProviderOptions.HTTPClient.
// Guarding that client must leave its transport and its absent timeout alone:
// the caller chose those semantics, and the guard's only job is to add the
// redirect and cleartext policies on top of them.
//
// The guard also has to go on a copy: writing CheckRedirect onto
// http.DefaultClient would change redirect behavior for every other user of
// that global in the process. That is the assertion that matters most here.
//
// The guarded client itself is not observable from outside the provider (the
// transport lives on an unexported field of openai.CompatibleProvider), so it
// is pinned by this test together with
// TestGuardedHTTPClient_NilBuildsGatewayDefault, which shows the nil path
// deliberately gets a different client.
func TestGuardedHTTPClient_PreservesDefaultClientSemantics(t *testing.T) {
	guarded := guardedHTTPClient(http.DefaultClient)

	require.NotSame(t, http.DefaultClient, guarded, "guardedHTTPClient returned http.DefaultClient itself; want a copy")
	assert.Nil(t, http.DefaultClient.CheckRedirect, "http.DefaultClient.CheckRedirect was set; the process-wide client must not be modified")
	assert.NotNil(t, guarded.CheckRedirect, "CheckRedirect = nil, want Eden's redirect guard on the copy")
	wrapped, ok := guarded.Transport.(*secureTransport)
	require.True(t, ok, "Transport = %T, want *secureTransport", guarded.Transport)
	// http.DefaultClient leaves Transport nil, meaning http.DefaultTransport;
	// the wrapper preserves that by delegating to it when its base is nil.
	assert.Equal(t, http.DefaultClient.Transport, wrapped.base, "wrapped base should be http.DefaultClient's transport (nil)")
	assert.Equal(t, http.DefaultClient.Timeout, guarded.Timeout, "Timeout should be http.DefaultClient's, not the gateway client's")
}

// TestGuardedHTTPClient_NilBuildsGatewayDefault covers the nil path: New
// passes opts.HTTPClient, which is nil unless the factory applied an outbound
// proxy or a test injected a client, and that path must produce the tuned
// gateway client — the transport and timeouts llmclient would otherwise have
// installed — not http.DefaultClient's absent timeout.
func TestGuardedHTTPClient_NilBuildsGatewayDefault(t *testing.T) {
	guarded := guardedHTTPClient(nil)

	assert.NotNil(t, guarded.CheckRedirect, "CheckRedirect = nil, want Eden's redirect guard")
	assert.Positive(t, guarded.Timeout, "Timeout should be the gateway default client's positive timeout")
}

// TestNew_NilClientStillGuardsRedirects proves the nil-client path is wired end
// to end: a provider built with no client of its own runs on the guarded
// gateway default client, so a credential-leaking redirect is refused rather
// than followed. The guard refuses the hop before dialing, so no DNS lookup of
// the redirect target ever happens.
//
// llmclient treats the refusal as a transport error and retries it, so the
// loopback server sees one request per attempt; every attempt stops at the
// same guard, and none of them is a followed redirect.
func TestNew_NilClientStillGuardsRedirects(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, "http://eden.example.com/v3/embeddings", http.StatusFound)
	}))
	defer server.Close()

	provider := newTestProvider("eden-key", server.URL+"/v3", nil, llmclient.Hooks{})
	_, err := provider.Embeddings(context.Background(), embeddingRequest())
	require.Error(t, err, "Embeddings followed a cleartext redirect on the nil-client path, want it refused")
	// The client-facing message hides upstream details by design; the guard's
	// refusal is the cause net/http wrapped in a *url.Error.
	var urlErr *url.Error
	require.ErrorAs(t, err, &urlErr, "want the redirect refusal retained as the cause")
	require.ErrorContains(t, urlErr.Err, "follow redirect", "want the redirect guard, not the request guard, to have refused")
	require.ErrorContains(t, urlErr.Err, "cleartext", "want the redirect guard's cleartext refusal")
	attempts := 1 + providertest.Resilience().Retry.MaxRetries
	assert.Equal(t, attempts, hits, "the loopback server should see exactly the original request once per attempt")
}
