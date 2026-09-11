package edenai

import (
	"context"
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
			if got := credentialSafeURL(parsed); got != tc.want {
				t.Errorf("credentialSafeURL(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestCredentialSafeURL_NilIsUnsafe asserts the predicate fails closed. A
// request with no parsable URL must not be treated as a TLS destination.
func TestCredentialSafeURL_NilIsUnsafe(t *testing.T) {
	if credentialSafeURL(nil) {
		t.Error("credentialSafeURL(nil) = true, want false: the predicate must fail closed")
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

// TestCleartextBaseURL_RequestCarriesNoCredential proves the guarantee end to
// end rather than only at the header setter: a provider configured with a
// non-loopback cleartext base URL reaches the upstream unauthenticated.
//
// The server here answers on a loopback address but is addressed through a
// public-looking host, which is what a misconfigured or hijacked
// EDENAI_BASE_URL would look like.
func TestCleartextBaseURL_RequestCarriesNoCredential(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","model":"openai/text-embedding-3-small","data":[]}`))
	}))
	defer server.Close()

	// Rewrite the loopback host to a name that is not loopback, while still
	// dialing the test server, by pointing the transport at it explicitly.
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	client := &http.Client{Transport: &cleartextRouteTransport{cleartext: target.Host}}

	provider := NewWithHTTPClient("eden-key", "http://eden.example.com/v3", client, llmclient.Hooks{})
	if _, err := provider.Embeddings(context.Background(), embeddingRequest()); err != nil {
		t.Fatalf("Embeddings: %v", err)
	}

	if gotAuth != "" {
		t.Errorf("upstream saw Authorization = %q, want empty: the Eden key must never travel in cleartext", gotAuth)
	}
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

// TestGuardedHTTPClient_PreservesCallerTransport asserts copying the client
// keeps the transport, so a caller that configured TLS roots or a proxy (and
// httptest's own client) still works.
func TestGuardedHTTPClient_PreservesCallerTransport(t *testing.T) {
	transport := &http.Transport{}
	caller := &http.Client{Transport: transport}

	if got := guardedHTTPClient(caller).Transport; got != transport {
		t.Errorf("Transport = %v, want the caller's transport preserved", got)
	}
}

// TestCheckRedirect_AllowsHTTPSTargets asserts TLS redirects keep working,
// including to a different host: that is an ordinary API redirect and the
// credential stays encrypted.
func TestCheckRedirect_AllowsHTTPSTargets(t *testing.T) {
	for _, target := range []string{
		"https://api.edenai.run/v3/chat/completions",
		"https://eu.edenai.run/v3/chat/completions",
	} {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
		if err := checkRedirect(req, nil); err != nil {
			t.Errorf("checkRedirect(%q) = %v, want nil", target, err)
		}
	}
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
	if guarded.Transport != http.DefaultClient.Transport {
		t.Errorf("Transport = %v, want http.DefaultClient's transport preserved", guarded.Transport)
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
