package oauthlogin

// Tests for the oauthlogin provider type: one provider that signs into an
// upstream account with the OAuth 2.0 device authorization grant and then
// serves that account. The account kind is configuration, not a separate
// provider type — xAI (Grok), Cursor, and OpenAI Codex share the flow and
// differ in the endpoints the configuration selects.
//
// The upstream is always the fake authorization server these tests start.
// Nothing here dials a real provider.

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/goccy/go-json"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// reLoginRequiredText is the fixed message a rejected refresh token surfaces.
// The provider must return it instead of spending any other credential.
const reLoginRequiredText = loginRequiredMessage

// fakeAuthServer is the account's authorization server and its
// OpenAI-compatible inference endpoint on one loopback listener.
type fakeAuthServer struct {
	t    *testing.T
	mu   sync.Mutex
	kind accountKind
	// approved is nil until the test says the operator approved the device code.
	approved *tokenResponse
	// refreshReject makes the token endpoint refuse refresh_token grants.
	refreshReject bool
}

func (f *fakeAuthServer) approve(access, refresh string, expiresIn int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approved = &tokenResponse{AccessToken: access, RefreshToken: refresh, TokenType: "bearer", ExpiresIn: expiresIn}
}

func (f *fakeAuthServer) rejectRefresh() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshReject = true
}

func (f *fakeAuthServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))

	f.mu.Lock()
	approved := f.approved
	refreshReject := f.refreshReject
	f.mu.Unlock()

	switch r.URL.Path {
	case devicePath(f.kind):
		writeJSON(w, http.StatusOK, deviceCodeResponse{
			DeviceCode: "device-" + f.kind.name, UserCode: "CODE-" + f.kind.name,
			VerificationURI: "https://verify.example/" + f.kind.name,
			ExpiresIn:       30, Interval: 1,
		})
	case tokenPath(f.kind):
		f.serveToken(w, form, approved, refreshReject)
	case "/v1/chat/completions":
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl-1", "object": "chat.completion", "model": "m",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
		})
	default:
		f.t.Errorf("unexpected upstream request to %s", r.URL.Path)
		http.NotFound(w, r)
	}
}

func (f *fakeAuthServer) serveToken(w http.ResponseWriter, form url.Values, approved *tokenResponse, refreshReject bool) {
	switch form.Get("grant_type") {
	case "refresh_token":
		if refreshReject {
			writeJSON(w, http.StatusBadRequest, tokenResponse{Error: "invalid_grant"})
			return
		}
		writeJSON(w, http.StatusOK, tokenResponse{
			AccessToken: "access-refreshed-" + f.kind.name, RefreshToken: "refresh-rotated-" + f.kind.name,
			TokenType: "bearer", ExpiresIn: 3600,
		})
	default:
		if approved == nil {
			writeJSON(w, http.StatusBadRequest, tokenResponse{Error: "authorization_pending"})
			return
		}
		writeJSON(w, http.StatusOK, *approved)
	}
}

// startAuthServer serves one account kind on a loopback server and returns the
// provider config pointed at it.
func startAuthServer(t *testing.T, kind accountKind) (*fakeAuthServer, *providertest.Capture, providers.ProviderConfig) {
	t.Helper()
	fake := &fakeAuthServer{t: t, kind: kind}
	server, capture := providertest.Server(t, fake.serve)
	cfg := providers.ProviderConfig{
		Name:       "oauthlogin-" + kind.name,
		Type:       providerType,
		Backend:    kind.name,
		BaseURL:    server.URL + "/v1",
		APIMode:    server.URL + devicePath(kind),
		APIVersion: server.URL + tokenPath(kind),
	}
	return fake, capture, cfg
}

func devicePath(kind accountKind) string {
	parsed, _ := url.Parse(kind.deviceEndpoint)
	return parsed.Path
}

func tokenPath(kind accountKind) string {
	parsed, _ := url.Parse(kind.tokenEndpoint)
	return parsed.Path
}

// newBuiltProvider builds the provider through the registered constructor, the
// way the factory does.
func newBuiltProvider(t *testing.T, cfg providers.ProviderConfig, client *http.Client) *Provider {
	t.Helper()
	require.NotNil(t, Registration.New)
	opts := providers.ProviderOptions{Name: cfg.Name, HTTPClient: client}
	provider := Registration.New(cfg, opts)
	built, ok := provider.(*Provider)
	require.Truef(t, ok, "New returned %T, want *Provider", provider)
	return built
}

func TestRegistration_OneTypeCoversEveryAccountKind(t *testing.T) {
	assert.Equal(t, "oauthlogin", Registration.Type)
	require.NotNil(t, Registration.New)
	assert.True(t, Registration.Discovery.AllowAPIKeyless, "a device login supplies the credential, so the API key is optional")

	var backend providers.CredentialField
	var found bool
	for _, field := range Registration.Discovery.CredentialFields {
		if field.Name == providers.CredentialFieldBackend {
			backend = field
			found = true
		}
	}
	require.True(t, found, "the account kind is chosen through the backend credential field")
	assert.ElementsMatch(t, []string{accountXAI, accountCursor, accountCodex}, backend.Options)
}

// TestFactory_CreateRejectsTheTypeUntilRegistered guards the failure mode the
// gateway has without this provider: an unregistered type is named in the
// error rather than silently mapped onto another provider.
func TestFactory_CreateRejectsTheTypeUntilRegistered(t *testing.T) {
	_, err := providers.NewProviderFactory().Create(providers.ProviderConfig{Type: providerType, Backend: accountXAI})
	require.EqualError(t, err, "unknown provider type: oauthlogin")
}

// TestStartLogin_ReturnsDeviceCodeAndExpiry: starting a login contacts only the
// account's device endpoint and reports the device code, the page the operator
// opens, and how long the code stays valid. No token exists afterwards.
func TestStartLogin_ReturnsDeviceCodeAndExpiry(t *testing.T) {
	for _, kind := range accountKinds {
		t.Run(kind.name, func(t *testing.T) {
			_, capture, cfg := startAuthServer(t, kind)
			provider := newBuiltProvider(t, cfg, http.DefaultClient)

			before := time.Now()
			auth, err := provider.StartLogin(context.Background())
			require.NoError(t, err)

			assert.Equal(t, "device-"+kind.name, auth.DeviceCode)
			assert.Equal(t, "CODE-"+kind.name, auth.UserCode)
			assert.Equal(t, "https://verify.example/"+kind.name, auth.VerificationURL)
			assert.Equal(t, 30, auth.ExpiresIn)
			assert.Equal(t, 1, auth.Interval)
			assert.Less(t, time.Since(before), 5*time.Second, "starting a login must return without waiting for approval")

			sent := capture.Last(t)
			assert.Equal(t, devicePath(kind), sent.Path)
			form := sentForm(t, sent)
			assert.NotEmpty(t, form.Get("client_id"))
			assert.NotContains(t, requestPaths(capture), tokenPath(kind), "starting a login must not redeem a token")

			stored := provider.session.current
			assert.Empty(t, stored.AccessToken)
			assert.Empty(t, stored.RefreshToken)
		})
	}
}

// TestLoginStatus_PendingUntilApprovalStoresNothing: before the operator
// approves, the login reports pending and the provider holds no token. The
// same login completes once approval arrives, so a pending check must not
// consume the device code.
func TestLoginStatus_PendingUntilApprovalStoresNothing(t *testing.T) {
	for _, kind := range accountKinds {
		t.Run(kind.name, func(t *testing.T) {
			fake, capture, cfg := startAuthServer(t, kind)
			provider := newBuiltProvider(t, cfg, http.DefaultClient)
			_, err := provider.StartLogin(context.Background())
			require.NoError(t, err)

			status := provider.Login()
			require.NoError(t, status.Err)
			assert.True(t, status.Pending, "a login nobody has approved yet must report pending")
			assert.Empty(t, provider.session.current.AccessToken, "pending login stored an access token")
			assert.Empty(t, provider.session.current.RefreshToken, "pending login stored a refresh token")

			fake.approve("access-"+kind.name, "refresh-"+kind.name, 3600)
			waitForLogin(t, provider)

			stored := provider.session.current
			assert.Equal(t, "access-"+kind.name, stored.AccessToken)
			assert.Equal(t, "refresh-"+kind.name, stored.RefreshToken)
			assert.NotContains(t, requestPaths(capture), "/v1/chat/completions")
		})
	}
}

// TestLogin_ApprovalStoresAccessAndRefreshToken: a completed approval persists
// both tokens for that account kind, and the redemption sent to the token
// endpoint is the device-code grant carrying the device code.
func TestLogin_ApprovalStoresAccessAndRefreshToken(t *testing.T) {
	for _, kind := range accountKinds {
		t.Run(kind.name, func(t *testing.T) {
			fake, capture, cfg := startAuthServer(t, kind)
			store := &memoryCredentialStore{}
			SetCredentialStore(store)
			t.Cleanup(func() { SetCredentialStore(nil) })

			provider := newBuiltProvider(t, cfg, http.DefaultClient)
			auth, err := provider.StartLogin(context.Background())
			require.NoError(t, err)

			fake.approve("access-"+kind.name, "refresh-"+kind.name, 3600)
			before := time.Now()
			waitForLogin(t, provider)

			stored := provider.session.current
			assert.Equal(t, "access-"+kind.name, stored.AccessToken)
			assert.Equal(t, "refresh-"+kind.name, stored.RefreshToken)
			assert.Equal(t, kind.name, stored.Account)
			require.False(t, stored.Expiry.IsZero(), "a token with a lifetime must record its expiry")
			assert.WithinDuration(t, before.Add(time.Hour), stored.Expiry, 15*time.Second)

			persisted, err := store.Get(context.Background(), cfg.Name)
			require.NoError(t, err)
			saved, err := parseSession(persisted.ServiceAccountJSON)
			require.NoError(t, err)
			assert.Equal(t, stored.AccessToken, saved.AccessToken)
			assert.Equal(t, stored.RefreshToken, saved.RefreshToken)
			assert.Equal(t, kind.name, saved.Account)

			form := sentForm(t, lastOn(t, capture, tokenPath(kind)))
			assert.Equal(t, "urn:ietf:params:oauth:grant-type:device_code", form.Get("grant_type"))
			assert.Equal(t, auth.DeviceCode, form.Get("device_code"))
		})
	}
}

// TestChatCompletion_SendsStoredAccessToken: a call made with the provider
// attaches the stored access token as a bearer token, and never the refresh
// token.
func TestChatCompletion_SendsStoredAccessToken(t *testing.T) {
	for _, kind := range accountKinds {
		t.Run(kind.name, func(t *testing.T) {
			_, capture, cfg := startAuthServer(t, kind)
			provider := signedInProvider(t, cfg, "access-"+kind.name, "refresh-"+kind.name, time.Now().Add(time.Hour))

			resp, err := provider.ChatCompletion(context.Background(), chatRequest())
			require.NoError(t, err)
			require.NotNil(t, resp)

			sent := lastOn(t, capture, "/v1/chat/completions")
			assert.Equal(t, "Bearer access-"+kind.name, sent.Header.Get("Authorization"))
			assert.NotContains(t, sent.Header.Get("Authorization"), "refresh-")
			assert.NotContains(t, requestPaths(capture), tokenPath(kind), "a valid access token must not be refreshed")
		})
	}
}

// TestChatCompletion_ExpiredAccessTokenIsRefreshed: an access token that has
// expired is exchanged at the token endpoint with the refresh token, the
// replacement access token is what the call carries, and the call succeeds.
func TestChatCompletion_ExpiredAccessTokenIsRefreshed(t *testing.T) {
	for _, kind := range accountKinds {
		t.Run(kind.name, func(t *testing.T) {
			_, capture, cfg := startAuthServer(t, kind)
			provider := signedInProvider(t, cfg, "access-expired-"+kind.name, "refresh-"+kind.name, time.Now().Add(-time.Minute))

			resp, err := provider.ChatCompletion(context.Background(), chatRequest())
			require.NoError(t, err)
			require.NotNil(t, resp)

			form := sentForm(t, lastOn(t, capture, tokenPath(kind)))
			assert.Equal(t, "refresh_token", form.Get("grant_type"))
			assert.Equal(t, "refresh-"+kind.name, form.Get("refresh_token"))

			sent := lastOn(t, capture, "/v1/chat/completions")
			assert.Equal(t, "Bearer access-refreshed-"+kind.name, sent.Header.Get("Authorization"))

			stored := provider.session.current
			assert.Equal(t, "access-refreshed-"+kind.name, stored.AccessToken)
			assert.Equal(t, "refresh-rotated-"+kind.name, stored.RefreshToken,
				"a rotated refresh token must replace the stored one")
		})
	}
}

// TestChatCompletion_RejectedRefreshRequiresReLogin: a refresh the
// authorization server refuses fails the call with the re-login-required
// error. The provider must not attempt the inference with the dead token, and
// must not fall back to the API keys configured beside the login.
func TestChatCompletion_RejectedRefreshRequiresReLogin(t *testing.T) {
	for _, kind := range accountKinds {
		t.Run(kind.name, func(t *testing.T) {
			fake, capture, cfg := startAuthServer(t, kind)
			cfg.APIKey = "static-key-not-for-this-account"
			cfg.APIKeys = []string{"rotated-key-not-for-this-account"}
			fake.rejectRefresh()
			provider := signedInProvider(t, cfg, "access-expired-"+kind.name, "refresh-"+kind.name, time.Now().Add(-time.Minute))

			_, err := provider.ChatCompletion(context.Background(), chatRequest())
			require.Error(t, err)
			assert.ErrorContains(t, err, reLoginRequiredText)

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, core.ErrorTypeAuthentication, gatewayErr.Type)
			assert.Equal(t, http.StatusUnauthorized, gatewayErr.HTTPStatusCode())

			assert.NotContains(t, requestPaths(capture), "/v1/chat/completions",
				"inference must not be attempted once the refresh is refused")
			for _, sent := range capture.All() {
				assert.NotContains(t, sent.Header.Get("Authorization"), cfg.APIKey)
				for _, key := range cfg.APIKeys {
					assert.NotContains(t, sent.Header.Get("Authorization"), key)
				}
			}
		})
	}
}

// signedInProvider builds a provider that already holds a device-login session,
// which is the state a completed login leaves behind.
func signedInProvider(t *testing.T, cfg providers.ProviderConfig, access, refresh string, expiry time.Time) *Provider {
	t.Helper()
	encoded, err := encodeSession(storedSession{
		AccessToken: access, RefreshToken: refresh, Account: cfg.Backend, Expiry: expiry, ClientID: defaultClientID,
	})
	require.NoError(t, err)
	cfg.ServiceAccountJSON = encoded
	return newBuiltProvider(t, cfg, http.DefaultClient)
}

// waitForLogin blocks until the provider's background poll finishes.
func waitForLogin(t *testing.T, provider *Provider) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status := provider.Login()
		if status.Err != nil {
			t.Fatalf("device login failed: %v", status.Err)
		}
		if !status.Pending {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("device login did not complete")
}

// memoryCredentialStore is the credential store a test supplies. It keeps the
// one row the provider persists its session into.
type memoryCredentialStore struct {
	mu  sync.Mutex
	row *providers.ManagedProviderCredential
}

func (s *memoryCredentialStore) Get(_ context.Context, name string) (*providers.ManagedProviderCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row == nil {
		s.row = &providers.ManagedProviderCredential{Name: name, Type: providerType, Enabled: true}
	}
	copy := *s.row
	return &copy, nil
}

func (s *memoryCredentialStore) Upsert(_ context.Context, cred providers.ManagedProviderCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.row = &cred
	return nil
}

func chatRequest() *core.ChatRequest {
	return &core.ChatRequest{Model: "m", Messages: []core.Message{{Role: "user", Content: "hi"}}}
}

func sentForm(t *testing.T, sent providertest.Recorded) url.Values {
	t.Helper()
	form, err := url.ParseQuery(string(sent.Body))
	require.NoError(t, err)
	return form
}

func lastOn(t *testing.T, capture *providertest.Capture, path string) providertest.Recorded {
	t.Helper()
	var found providertest.Recorded
	seen := false
	for _, sent := range capture.All() {
		if sent.Path == path {
			found = sent
			seen = true
		}
	}
	if !seen {
		t.Fatalf("upstream received no %s request; paths: %v", path, requestPaths(capture))
	}
	return found
}

func requestPaths(capture *providertest.Capture) []string {
	var paths []string
	for _, sent := range capture.All() {
		paths = append(paths, sent.Path)
	}
	return paths
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, mustJSON(body))
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
