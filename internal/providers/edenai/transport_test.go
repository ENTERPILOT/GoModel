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
			if err != nil {
				t.Fatalf("url.Parse(%q) = %v", tc.raw, err)
			}
			if got := secureDestination(parsed); got != tc.want {
				t.Errorf("secureDestination(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestCredentialSafeURL_NilIsUnsafe asserts the predicate fails closed. A
// request with no parsable URL must not be treated as a TLS destination.
func TestCredentialSafeURL_NilIsUnsafe(t *testing.T) {
	if secureDestination(nil) {
		t.Error("secureDestination(nil) = true, want false: the predicate must fail closed")
	}
}

// TestSetHeaders_SendsCredentialOverHTTPS asserts the ordinary case still
// authenticates: an HTTPS destination gets the bearer token.
func TestSetHeaders_SendsCredentialOverHTTPS(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, defaultBaseURL+"/chat/completions", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	setHeaders(req, "eden-key")

	if got := req.Header.Get("Authorization"); got != "Bearer eden-key" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer eden-key")
	}
}

// TestSetHeaders_WithholdsCredentialOverCleartext is the base-URL half of the
// credential guarantee: an operator-supplied http:// endpoint must not put the
// Eden key on the wire in plain text.
func TestSetHeaders_WithholdsCredentialOverCleartext(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://eden.example.com/v3/chat/completions", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	setHeaders(req, "eden-key")

	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty: the credential must not be sent in cleartext", got)
	}
}

// TestSetHeaders_SendsCredentialOverLoopback pins the exemption that keeps a
// local Eden-compatible proxy — and every httptest server in this package —
// working. Cleartext to the local machine never reaches a network.
func TestSetHeaders_SendsCredentialOverLoopback(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v3/chat/completions", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	setHeaders(req, "eden-key")

	if got := req.Header.Get("Authorization"); got != "Bearer eden-key" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer eden-key")
	}
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
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	client := &http.Client{Transport: &cleartextRouteTransport{cleartext: target.Host}}

	provider := NewWithHTTPClient("eden-key", "http://eden.example.com/v3", client, llmclient.Hooks{})
	_, err = provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    slashedModel,
		Messages: []core.Message{{Role: "user", Content: "secret-prompt"}},
	})
	if err == nil {
		t.Fatal("ChatCompletion succeeded against a cleartext endpoint, want the request refused")
	}

	if hits != 0 {
		t.Errorf("cleartext endpoint received %d request(s), want 0", hits)
	}
	if gotAuth != "" {
		t.Errorf("cleartext endpoint saw Authorization = %q, want empty", gotAuth)
	}
	if strings.Contains(gotBody, "secret-prompt") {
		t.Errorf("cleartext endpoint received the prompt body %q; the payload must never be sent", gotBody)
	}
	// The refusal must name the destination without quoting the whole URL.
	if !strings.Contains(err.Error(), "eden.example.com") {
		t.Errorf("error %q should name the refused host", err)
	}
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

	provider := NewWithHTTPClient("eden-key", server.URL, server.Client(), llmclient.Hooks{})
	if _, err := provider.Embeddings(context.Background(), embeddingRequest()); err != nil {
		t.Fatalf("Embeddings against a loopback endpoint: %v", err)
	}
	if hits != 1 {
		t.Errorf("loopback endpoint received %d request(s), want 1", hits)
	}
	if gotAuth != "Bearer eden-key" {
		t.Errorf("loopback endpoint saw Authorization = %q, want %q", gotAuth, "Bearer eden-key")
	}
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
			if err != nil {
				t.Fatalf("http.NewRequest: %v", err)
			}
			resp, err := transport.RoundTrip(req)
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}

			if stub.calls != 0 != tc.wantCalled {
				t.Errorf("underlying transport calls = %d, wantCalled = %v", stub.calls, tc.wantCalled)
			}
			if tc.wantCalled && err != nil {
				t.Errorf("RoundTrip(%q) = %v, want the request passed through", tc.target, err)
			}
			if !tc.wantCalled && err == nil {
				t.Errorf("RoundTrip(%q) = nil error, want a refusal", tc.target)
			}
		})
	}
}

// TestSecureTransport_NilBaseUsesDefaultTransport asserts the nil-base fallback
// is wired, since http.DefaultClient carries a nil Transport and Eden's guard
// wraps exactly that on the NewWithHTTPClient nil path.
func TestSecureTransport_NilBaseUsesDefaultTransport(t *testing.T) {
	transport := &secureTransport{}

	// A refused destination never reaches the base, so it proves the guard runs
	// without needing a live server for the delegating case.
	req, err := http.NewRequest(http.MethodGet, "http://eden.example.com/v3", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	if _, err := transport.RoundTrip(req); err == nil {
		t.Error("RoundTrip = nil error for a cleartext target, want a refusal")
	}

	// An allowed loopback destination must reach the network through
	// http.DefaultTransport rather than panicking on the nil base.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	req, err = http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip through the nil base: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
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

// TestCompatibleConfig_InstallsRedirectGuardOnBothPaths asserts neither
// construction path can reach the network without the redirect policy. Both
// New and NewWithHTTPClient build their transport through compatibleConfig, so
// covering it here covers both.
func TestCompatibleConfig_InstallsRedirectGuardOnBothPaths(t *testing.T) {
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
			if cfg.HTTPClient == nil {
				t.Fatal("HTTPClient = nil, want a client carrying the redirect guard")
			}
			if cfg.HTTPClient.CheckRedirect == nil {
				t.Error("CheckRedirect = nil, want Eden's redirect guard installed")
			}
		})
	}
}

// TestGuardedHTTPClient_DoesNotMutateCallerClient asserts the caller's client
// is copied rather than reconfigured. A client shared with another provider
// must keep its own redirect behavior.
func TestGuardedHTTPClient_DoesNotMutateCallerClient(t *testing.T) {
	caller := &http.Client{}
	guarded := guardedHTTPClient(caller)

	if caller.CheckRedirect != nil {
		t.Error("caller's CheckRedirect was set; the guard must be installed on a copy")
	}
	if guarded == caller {
		t.Error("guardedHTTPClient returned the caller's client; want a copy")
	}
	if guarded.CheckRedirect == nil {
		t.Error("guarded client has no CheckRedirect")
	}
}

// TestGuardedHTTPClient_WrapsCallerTransport asserts the guard is layered over
// the caller's transport rather than replacing it, so a caller that configured
// TLS roots or a proxy (and httptest's own client) still reaches its server.
func TestGuardedHTTPClient_WrapsCallerTransport(t *testing.T) {
	transport := &http.Transport{}
	caller := &http.Client{Transport: transport}

	guarded, ok := guardedHTTPClient(caller).Transport.(*secureTransport)
	if !ok {
		t.Fatalf("Transport = %T, want *secureTransport wrapping the caller's", guardedHTTPClient(caller).Transport)
	}
	if guarded.base != transport {
		t.Errorf("wrapped base = %v, want the caller's transport preserved", guarded.base)
	}
	if caller.Transport != transport {
		t.Error("the caller's client was modified; the guard must go on a copy")
	}
}

// TestCheckRedirect_AllowsSameHostHTTPS asserts the one redirect shape a REST
// endpoint plausibly returns keeps working: a path change on the host the
// request was already addressed to.
func TestCheckRedirect_AllowsSameHostHTTPS(t *testing.T) {
	origin := mustRequest(t, "https://api.edenai.run/v3/chat/completions")
	target := mustRequest(t, "https://api.edenai.run/v3/chat/completions-moved")

	if err := checkRedirect(target, []*http.Request{origin}); err != nil {
		t.Errorf("checkRedirect same-host = %v, want nil", err)
	}
	// Host comparison is case-insensitive, as hostnames are.
	upper := mustRequest(t, "https://API.EdenAI.run/v3/chat/completions-moved")
	if err := checkRedirect(upper, []*http.Request{origin}); err != nil {
		t.Errorf("checkRedirect differing-case host = %v, want nil", err)
	}
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
		if err == nil {
			t.Errorf("checkRedirect(%q) = nil, want a refusal", target)
			continue
		}
		if !strings.Contains(err.Error(), "cross-host") {
			t.Errorf("checkRedirect(%q) = %v, want a cross-host refusal", target, err)
		}
	}
}

// TestCheckRedirect_ComparesAgainstOriginalHost asserts a chain cannot walk off
// the configured host one hop at a time: the check is against the request the
// caller made, not the previous hop.
func TestCheckRedirect_ComparesAgainstOriginalHost(t *testing.T) {
	origin := mustRequest(t, "https://api.edenai.run/v3/models")
	hop := mustRequest(t, "https://api.edenai.run/v3/models-moved")
	target := mustRequest(t, "https://elsewhere.edenai.run/v3/models")

	if err := checkRedirect(target, []*http.Request{origin, hop}); err == nil {
		t.Error("checkRedirect = nil for a second hop leaving the original host, want a refusal")
	}
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
	if err := policy(target, []*http.Request{origin}); !errors.Is(err, callerErr) {
		t.Errorf("policy = %v, want the caller's error returned verbatim", err)
	}
	if called != 1 {
		t.Errorf("caller policy invoked %d times, want 1", called)
	}
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

	if err := policy(target, []*http.Request{origin}); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("policy = %v, want http.ErrUseLastResponse propagated", err)
	}
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
			if err == nil {
				t.Fatalf("policy = nil, want a refusal after the callback rewrote the target to %s", target.URL)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("policy = %v, want an error mentioning %q", err, tc.wantErr)
			}
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

	if err := policy(target, []*http.Request{origin}); err != nil {
		t.Errorf("policy = %v, want nil: a same-host path rewrite is fine", err)
	}
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
			if err := policy(target, []*http.Request{origin}); !errors.Is(err, tc.err) {
				t.Errorf("policy = %v, want %v returned unchanged", err, tc.err)
			}
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

	if err := policy(target, []*http.Request{origin}); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("policy = %v, want http.ErrUseLastResponse propagated unchanged", err)
	}
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
		if err := policy(mustRequest(t, target), []*http.Request{origin}); err == nil {
			t.Errorf("policy(%q) = nil, want Eden's refusal to stand", target)
		}
	}
	if called != 0 {
		t.Errorf("caller policy invoked %d times, want 0: Eden refuses before delegating", called)
	}
}

// TestRedirectPolicy_NilCallerAllowsEdenApprovedHop asserts the common case —
// a client with no policy of its own — still follows an Eden-approved redirect.
func TestRedirectPolicy_NilCallerAllowsEdenApprovedHop(t *testing.T) {
	origin := mustRequest(t, "https://api.edenai.run/v3/models")
	target := mustRequest(t, "https://api.edenai.run/v3/models-moved")

	if err := redirectPolicy(nil)(target, []*http.Request{origin}); err != nil {
		t.Errorf("redirectPolicy(nil) = %v, want nil for a same-host TLS hop", err)
	}
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

	if err := guarded.CheckRedirect(target, []*http.Request{origin}); err != nil {
		t.Fatalf("CheckRedirect = %v, want nil", err)
	}
	if !called {
		t.Error("the caller's redirect policy was not invoked; the guard must compose, not replace")
	}
}

// mustRequest builds a GET request for a URL a test controls.
func mustRequest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest(%q): %v", rawURL, err)
	}
	return req
}

// TestCheckRedirect_RefusesSchemeDowngrade is the core of the redirect
// guarantee. net/http drops Authorization only when the redirect target is a
// different host; it does not consider the scheme, so a same-host HTTPS -> HTTP
// redirect would otherwise forward the bearer token in the clear.
func TestCheckRedirect_RefusesSchemeDowngrade(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://api.edenai.run/v3/chat/completions", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}

	err = checkRedirect(req, nil)
	if err == nil {
		t.Fatal("checkRedirect = nil, want a refusal for a cleartext redirect target")
	}
	if !strings.Contains(err.Error(), "api.edenai.run") {
		t.Errorf("error %q should name the refused host", err)
	}
}

// TestCheckRedirect_ErrorOmitsCredentials asserts the refusal message cannot
// leak a secret carried in the redirect URL's userinfo or query string.
func TestCheckRedirect_ErrorOmitsCredentials(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://user:s3cret@api.edenai.run/v3?api_key=leaked", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}

	err = checkRedirect(req, nil)
	if err == nil {
		t.Fatal("checkRedirect = nil, want a refusal")
	}
	for _, secret := range []string{"s3cret", "leaked"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error %q leaked %q from the redirect URL", err, secret)
		}
	}
}

// TestCheckRedirect_EnforcesRedirectBudget asserts installing a policy did not
// silently remove net/http's own protection against endless redirect chains.
func TestCheckRedirect_EnforcesRedirectBudget(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, defaultBaseURL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	via := make([]*http.Request, maxRedirects)

	if err := checkRedirect(req, via); err == nil {
		t.Fatalf("checkRedirect with %d prior hops = nil, want the redirect budget enforced", maxRedirects)
	}
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
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	client := secure.Client()
	client.Transport = &cleartextRouteTransport{
		base:      client.Transport,
		cleartext: cleartextTarget.Host,
	}

	provider := NewWithHTTPClient("eden-key", secure.URL+"/v3", client, llmclient.Hooks{})
	_, err = provider.Embeddings(context.Background(), embeddingRequest())
	if err == nil {
		t.Fatal("Embeddings succeeded through an HTTPS -> HTTP redirect, want the redirect refused")
	}

	if cleartextHits != 0 {
		t.Errorf("cleartext endpoint received %d request(s), want 0", cleartextHits)
	}
	if cleartextAuth != "" {
		t.Errorf("cleartext endpoint saw Authorization = %q, want empty", cleartextAuth)
	}
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

	provider := NewWithHTTPClient("eden-key", server.URL+"/v3", server.Client(), llmclient.Hooks{})
	if _, err := provider.Embeddings(context.Background(), embeddingRequest()); err != nil {
		t.Fatalf("Embeddings through a loopback redirect: %v", err)
	}
	if !served {
		t.Fatal("redirect target was never reached")
	}
	if finalAuth != "Bearer eden-key" {
		t.Errorf("redirect target saw Authorization = %q, want %q", finalAuth, "Bearer eden-key")
	}
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

	provider := NewWithHTTPClient("eden-key", secure.URL+"/v3", secure.Client(), llmclient.Hooks{})
	resp, err := provider.Embeddings(context.Background(), embeddingRequest())
	if err != nil {
		t.Fatalf("Embeddings through an HTTPS -> HTTPS redirect: %v", err)
	}
	if !served {
		t.Fatal("redirect target was never reached")
	}
	if resp == nil {
		t.Fatal("response = nil")
	}
	if finalAuth != "Bearer eden-key" {
		t.Errorf("redirect target saw Authorization = %q, want %q", finalAuth, "Bearer eden-key")
	}
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
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	client := server.Client()
	// Route both hostnames to the one test server; the TLS config from
	// server.Client() already trusts its certificate.
	client.Transport = &hostPinnedTransport{base: client.Transport, addr: target.Host}

	provider := NewWithHTTPClient("eden-key", "https://eden.test/v3", client, llmclient.Hooks{})
	_, err = provider.Embeddings(context.Background(), embeddingRequest())
	if err == nil {
		t.Fatal("Embeddings followed an HTTPS subdomain redirect, want it refused")
	}

	if secondHopHits != 0 {
		t.Errorf("subdomain endpoint received %d request(s), want 0", secondHopHits)
	}
	if secondHopAuth != "" {
		t.Errorf("subdomain endpoint saw Authorization = %q, want empty: the Eden key must not follow an upstream-chosen host", secondHopAuth)
	}
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
	if !strings.HasPrefix(defaultBaseURL, "https://") {
		t.Fatalf("defaultBaseURL = %q, want an https:// endpoint", defaultBaseURL)
	}
}

// TestSetBaseURL_OverridesResolvedEndpoint covers the public base-URL override,
// which the credential guard then reads at request time rather than using the
// endpoint the provider was constructed with.
func TestSetBaseURL_OverridesResolvedEndpoint(t *testing.T) {
	provider, ok := New(providers.ProviderConfig{APIKey: "eden-key"}, providers.ProviderOptions{}).(*Provider)
	if !ok {
		t.Fatal("New did not return *Provider")
	}

	const override = "https://eden.eu.example.com/v3"
	provider.SetBaseURL(override)
	if got := provider.GetBaseURL(); got != override {
		t.Errorf("GetBaseURL() = %q, want %q after SetBaseURL", got, override)
	}
}

// TestGuardedHTTPClient_PreservesDefaultClientSemantics pins what
// NewWithHTTPClient's nil path produces. Every other chat-compatible provider
// documents "if httpClient is nil, http.DefaultClient is used", and gets that
// from NewCompatibleProviderWithHTTPClient; Eden hands that helper an
// already-guarded client, so it substitutes http.DefaultClient itself. Guarding
// that client must leave its transport and its absent timeout alone, or the
// constructor would quietly diverge from every peer.
//
// The guard also has to go on a copy: writing CheckRedirect onto
// http.DefaultClient would change redirect behavior for every other user of
// that global in the process. That is the assertion that matters most here.
//
// The substitution itself is not observable from outside the provider (the
// transport lives on an unexported field of openai.CompatibleProvider), so it
// is pinned by this test together with
// TestGuardedHTTPClient_NilBuildsGatewayDefault, which shows the two callers
// deliberately get different clients.
func TestGuardedHTTPClient_PreservesDefaultClientSemantics(t *testing.T) {
	guarded := guardedHTTPClient(http.DefaultClient)

	if guarded == http.DefaultClient {
		t.Fatal("guardedHTTPClient returned http.DefaultClient itself; want a copy")
	}
	if http.DefaultClient.CheckRedirect != nil {
		t.Error("http.DefaultClient.CheckRedirect was set; the process-wide client must not be modified")
	}
	if guarded.CheckRedirect == nil {
		t.Error("CheckRedirect = nil, want Eden's redirect guard on the copy")
	}
	wrapped, ok := guarded.Transport.(*secureTransport)
	if !ok {
		t.Fatalf("Transport = %T, want *secureTransport", guarded.Transport)
	}
	// http.DefaultClient leaves Transport nil, meaning http.DefaultTransport;
	// the wrapper preserves that by delegating to it when its base is nil.
	if wrapped.base != http.DefaultClient.Transport {
		t.Errorf("wrapped base = %v, want http.DefaultClient's transport (nil)", wrapped.base)
	}
	if guarded.Timeout != http.DefaultClient.Timeout {
		t.Errorf("Timeout = %v, want http.DefaultClient's %v, not the gateway client's", guarded.Timeout, http.DefaultClient.Timeout)
	}
}

// TestGuardedHTTPClient_NilBuildsGatewayDefault covers the other caller: New
// passes nil because the factory path has no client of its own, and must get
// the tuned transport and timeouts llmclient would otherwise have installed —
// not http.DefaultClient's absent timeout.
func TestGuardedHTTPClient_NilBuildsGatewayDefault(t *testing.T) {
	guarded := guardedHTTPClient(nil)

	if guarded.CheckRedirect == nil {
		t.Error("CheckRedirect = nil, want Eden's redirect guard")
	}
	if guarded.Timeout <= 0 {
		t.Errorf("Timeout = %v, want the gateway default client's positive timeout", guarded.Timeout)
	}
}

// TestNewWithHTTPClient_NilClientStillGuardsRedirects proves the nil path is
// wired end to end: a provider built with no client still refuses a
// credential-leaking redirect rather than following it.
func TestNewWithHTTPClient_NilClientStillGuardsRedirects(t *testing.T) {
	provider := NewWithHTTPClient("eden-key", "http://eden.example.com/v3", nil, llmclient.Hooks{})
	if provider == nil {
		t.Fatal("NewWithHTTPClient(..., nil, ...) returned nil")
	}

	req, err := http.NewRequest(http.MethodGet, "http://api.edenai.run/v3/chat/completions", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	if err := checkRedirect(req, nil); err == nil {
		t.Error("checkRedirect = nil for a cleartext target; the guard must apply on the nil-client path too")
	}
}
