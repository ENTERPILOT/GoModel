package mcpgateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/encryption/encryptiontest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

const testRedirectURI = "http://127.0.0.1:8080/admin/mcp-oauth/callback"

// fakeAuthServer is an OAuth 2.1 authorization server that checks PKCE and
// the resource indicator, issues a short-lived token, and rotates refresh
// tokens.
type fakeAuthServer struct {
	*httptest.Server
	registration bool
	resource     string

	mu           sync.Mutex
	challenge    string
	refreshToken string
	accessToken  string
	refreshes    int
}

func newFakeAuthServer(t *testing.T, registration bool) *fakeAuthServer {
	t.Helper()
	as := &fakeAuthServer{registration: registration}
	as.Server = httptest.NewServer(http.HandlerFunc(as.serve))
	t.Cleanup(as.Close)
	return as
}

func writeTestJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (as *fakeAuthServer) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/oauth-authorization-server":
		meta := map[string]any{
			"issuer":                           as.URL,
			"authorization_endpoint":           as.URL + "/authorize",
			"token_endpoint":                   as.URL + "/token",
			"code_challenge_methods_supported": []string{"S256"},
			"scopes_supported":                 []string{"mcp:read", "offline_access"},
			"response_types_supported":         []string{"code"},
		}
		if as.registration {
			meta["registration_endpoint"] = as.URL + "/register"
		}
		writeTestJSON(w, http.StatusOK, meta)
	case "/register":
		var meta map[string]any
		_ = json.NewDecoder(r.Body).Decode(&meta)
		writeTestJSON(w, http.StatusCreated, map[string]any{
			"client_id":                  "dcr-client",
			"redirect_uris":              meta["redirect_uris"],
			"token_endpoint_auth_method": "none",
		})
	case "/token":
		as.token(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (as *fakeAuthServer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeTestJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	if r.PostForm.Get("resource") != as.resource {
		writeTestJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_target"})
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if r.PostForm.Get("code") != "code-1" || base64.RawURLEncoding.EncodeToString(sum[:]) != as.challenge {
			writeTestJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		}
		// Expires inside oauth2's expiry delta, so the first use refreshes.
		as.accessToken, as.refreshToken = "access-1", "refresh-1"
		writeTestJSON(w, http.StatusOK, map[string]any{
			"access_token": "access-1", "refresh_token": "refresh-1", "token_type": "Bearer", "expires_in": 1, "scope": "mcp:read offline_access",
		})
	case "refresh_token":
		if r.PostForm.Get("refresh_token") != as.refreshToken {
			writeTestJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "refresh token was rotated"})
			return
		}
		as.refreshes++
		as.accessToken = "access-2"
		as.refreshToken = "refresh-2"
		writeTestJSON(w, http.StatusOK, map[string]any{
			"access_token": "access-2", "refresh_token": "refresh-2", "token_type": "Bearer", "expires_in": 3600,
		})
	default:
		writeTestJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
	}
}

func (as *fakeAuthServer) validAccessToken(token string) bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.accessToken != "" && token == as.accessToken
}

// newProtectedUpstream serves an MCP server at /mcp that answers 401 with a
// resource_metadata challenge unless the request carries a token as issued.
func newProtectedUpstream(t *testing.T, as *fakeAuthServer) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "protected", Version: "test"}, nil)
	addEchoTool("echo")(server)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-protected-resource/mcp" {
			writeTestJSON(w, http.StatusOK, map[string]any{
				"resource":              ts.URL + "/mcp",
				"authorization_servers": []string{as.URL},
				"scopes_supported":      []string{"mcp:read"},
			})
			return
		}
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !as.validAccessToken(token) {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+ts.URL+`/.well-known/oauth-protected-resource/mcp", scope="mcp:read"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	as.resource = ts.URL + "/mcp"
	return ts.URL + "/mcp"
}

func newOAuthTestService(t *testing.T, serverURL string) (*Service, *oauthStore) {
	t.Helper()
	store, err := NewSQLStore(context.Background(), sqlxtest.NewSQLite(t))
	require.NoError(t, err)
	box, _ := encryptiontest.NewBox(t)
	oauth := newOAuthStore(store, box)
	service, _ := newTestServiceWithOptions(t, Options{
		ConfigServers: map[string]ServerSpec{"notion": testSpec("notion", serverURL, nil)},
		Store:         store,
		oauthStore:    oauth,
	})
	return service, oauth
}

func serverView(t *testing.T, service *Service, name string) ServerView {
	t.Helper()
	for _, view := range service.Views() {
		if view.Spec.Name == name {
			return view
		}
	}
	require.FailNow(t, "server not found", name)
	return ServerView{}
}

func waitForStatus(t *testing.T, service *Service, name string, status ServerStatus) ServerView {
	t.Helper()
	var view ServerView
	require.Eventually(t, func() bool {
		view = serverView(t, service, name)
		return view.Status == status
	}, 10*time.Second, 20*time.Millisecond, "server %s did not become %s", name, status)
	return view
}

// authorize plays the browser and authorization server: it records the PKCE
// challenge and returns the callback the server would redirect to.
func authorize(t *testing.T, as *fakeAuthServer, authURL string, binding string) OAuthCallback {
	t.Helper()
	parsed, err := url.Parse(authURL)
	require.NoError(t, err)
	query := parsed.Query()
	as.mu.Lock()
	as.challenge = query.Get("code_challenge")
	as.mu.Unlock()
	return OAuthCallback{State: query.Get("state"), Code: "code-1", Binding: binding}
}

func TestOAuthSignInFlow(t *testing.T) {
	as := newFakeAuthServer(t, true)
	serverURL := newProtectedUpstream(t, as)
	service, store := newOAuthTestService(t, serverURL)
	ctx := context.Background()

	before := serverView(t, service, "notion")
	assert.Equal(t, StatusDegraded, before.Status)
	assert.True(t, before.AuthRequired, "a 401 handshake marks the server as needing sign-in")
	assert.Nil(t, before.OAuth)

	start, err := service.StartOAuth(ctx, "notion", OAuthStartRequest{RedirectURI: testRedirectURI})
	require.NoError(t, err)
	require.NotEmpty(t, start.Binding)
	authURL, err := url.Parse(start.AuthorizationURL)
	require.NoError(t, err)
	query := authURL.Query()
	assert.Equal(t, as.URL+"/authorize", authURL.Scheme+"://"+authURL.Host+authURL.Path)
	assert.Equal(t, "dcr-client", query.Get("client_id"), "the client came from dynamic registration")
	assert.Equal(t, "S256", query.Get("code_challenge_method"))
	assert.Equal(t, serverURL, query.Get("resource"))
	assert.Equal(t, testRedirectURI, query.Get("redirect_uri"))
	assert.Equal(t, "mcp:read offline_access", query.Get("scope"), "challenge scope plus offline_access")
	assert.NotEmpty(t, query.Get("state"))

	name, err := service.CompleteOAuth(ctx, authorize(t, as, start.AuthorizationURL, start.Binding))
	require.NoError(t, err)
	assert.Equal(t, "notion", name)

	view := waitForStatus(t, service, "notion", StatusConnected)
	assert.False(t, view.AuthRequired)
	assert.Equal(t, 1, view.ToolCount)
	require.NotNil(t, view.OAuth)
	assert.Equal(t, OAuthStatusSignedIn, view.OAuth.Status)
	assert.Equal(t, as.URL, view.OAuth.Issuer)
	assert.Equal(t, []string{"mcp:read", "offline_access"}, view.OAuth.Scopes)

	// The short-lived token was refreshed on first use, with the resource
	// indicator, and the rotated refresh token was persisted.
	as.mu.Lock()
	refreshes := as.refreshes
	as.mu.Unlock()
	assert.GreaterOrEqual(t, refreshes, 1)
	stored, err := store.get(ctx, "notion")
	require.NoError(t, err)
	assert.Equal(t, "refresh-2", stored.RefreshToken)
	assert.Equal(t, "access-2", stored.AccessToken)

	result, err := service.manager.CallTool(ctx, "notion", "echo", json.RawMessage(`{"x":1}`))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	require.NoError(t, service.SignOutOAuth(ctx, "notion"))
	_, err = store.get(ctx, "notion")
	require.ErrorIs(t, err, ErrNotFound)
	view = waitForStatus(t, service, "notion", StatusDegraded)
	assert.Nil(t, view.OAuth)
	assert.True(t, view.AuthRequired)
}

func TestOAuthCallbackRejections(t *testing.T) {
	as := newFakeAuthServer(t, true)
	serverURL := newProtectedUpstream(t, as)
	service, store := newOAuthTestService(t, serverURL)
	ctx := context.Background()

	tests := []struct {
		name    string
		mutate  func(*OAuthCallback)
		wantErr string
	}{
		{name: "other browser", mutate: func(cb *OAuthCallback) { cb.Binding = "other" }, wantErr: "different browser"},
		{name: "unexpected issuer", mutate: func(cb *OAuthCallback) { cb.Issuer = "https://evil.example" }, wantErr: "unexpected issuer"},
		{name: "denied", mutate: func(cb *OAuthCallback) { cb.Code = ""; cb.Error = "access_denied" }, wantErr: "access_denied"},
		{name: "wrong code", mutate: func(cb *OAuthCallback) { cb.Code = "forged" }, wantErr: "invalid_grant"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, err := service.StartOAuth(ctx, "notion", OAuthStartRequest{RedirectURI: testRedirectURI})
			require.NoError(t, err)
			callback := authorize(t, as, start.AuthorizationURL, start.Binding)
			tt.mutate(&callback)

			name, err := service.CompleteOAuth(ctx, callback)
			require.ErrorContains(t, err, tt.wantErr)
			assert.Equal(t, "notion", name)
			_, err = store.get(ctx, "notion")
			require.ErrorIs(t, err, ErrNotFound, "nothing is stored")
			view := serverView(t, service, "notion")
			require.NotNil(t, view.OAuth)
			assert.Equal(t, OAuthStatusError, view.OAuth.Status)
			assert.Contains(t, view.OAuth.LastError, tt.wantErr)

			// The state is single-use.
			_, err = service.CompleteOAuth(ctx, callback)
			require.ErrorIs(t, err, ErrOAuthFlowUnknown)
		})
	}

	_, err := service.CompleteOAuth(ctx, OAuthCallback{State: "unknown", Code: "code-1"})
	require.ErrorIs(t, err, ErrOAuthFlowUnknown)
}

func TestOAuthStartWithoutDynamicRegistration(t *testing.T) {
	as := newFakeAuthServer(t, false)
	serverURL := newProtectedUpstream(t, as)
	service, _ := newOAuthTestService(t, serverURL)
	ctx := context.Background()

	_, err := service.StartOAuth(ctx, "notion", OAuthStartRequest{RedirectURI: testRedirectURI})
	require.ErrorIs(t, err, ErrOAuthClientRequired)

	start, err := service.StartOAuth(ctx, "notion", OAuthStartRequest{RedirectURI: testRedirectURI, ClientID: "pre-registered"})
	require.NoError(t, err)
	authURL, err := url.Parse(start.AuthorizationURL)
	require.NoError(t, err)
	assert.Equal(t, "pre-registered", authURL.Query().Get("client_id"))

	_, err = service.StartOAuth(ctx, "missing", OAuthStartRequest{RedirectURI: testRedirectURI})
	require.ErrorIs(t, err, ErrNotFound)
	_, err = service.StartOAuth(ctx, "notion", OAuthStartRequest{RedirectURI: "https://evil.example/steal"})
	require.ErrorIs(t, err, ErrOAuthInvalidRequest)
}

func TestValidateOAuthRedirectURI(t *testing.T) {
	tests := []struct {
		uri   string
		valid bool
	}{
		{uri: "https://gomodel.example.com/admin/mcp-oauth/callback", valid: true},
		{uri: "http://localhost:8080/admin/mcp-oauth/callback", valid: true},
		{uri: "https://example.com/gomodel/admin/mcp-oauth/callback", valid: true},
		{uri: "/admin/mcp-oauth/callback"},
		{uri: "ftp://example.com/admin/mcp-oauth/callback"},
		{uri: "https://example.com/admin/mcp-oauth/callback?next=x"},
		{uri: "https://example.com/admin/mcp-oauth/callback#x"},
		{uri: "https://user:pass@example.com/admin/mcp-oauth/callback"},
		{uri: "https://example.com/other"},
	}
	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			err := ValidateOAuthRedirectURI(tt.uri)
			if tt.valid {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrOAuthInvalidRequest)
		})
	}
}

func TestValidateAuthorizationIssuer(t *testing.T) {
	tests := []struct {
		name      string
		iss       string
		supported bool
		wantErr   bool
	}{
		{name: "advertised and matching", iss: "https://as.example", supported: true},
		{name: "advertised but absent", supported: true, wantErr: true},
		{name: "advertised and different", iss: "https://other.example", supported: true, wantErr: true},
		{name: "not advertised and absent"},
		{name: "not advertised but matching", iss: "https://as.example"},
		{name: "not advertised and different", iss: "https://other.example", wantErr: true},
		{name: "no normalization", iss: "https://as.example/", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAuthorizationIssuer(tt.iss, "https://as.example", tt.supported)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestProtectedResourceMetadataURLs(t *testing.T) {
	tests := []struct {
		name        string
		metadataURL string
		serverURL   string
		want        []prmCandidate
	}{
		{
			name:      "path endpoint",
			serverURL: "https://mcp.example.com/public/mcp",
			want: []prmCandidate{
				{url: "https://mcp.example.com/.well-known/oauth-protected-resource/public/mcp", resource: "https://mcp.example.com/public/mcp"},
				{url: "https://mcp.example.com/.well-known/oauth-protected-resource", resource: "https://mcp.example.com"},
			},
		},
		{
			name:        "challenge first",
			metadataURL: "https://mcp.example.com/prm",
			serverURL:   "https://mcp.example.com",
			want: []prmCandidate{
				{url: "https://mcp.example.com/prm", resource: "https://mcp.example.com"},
				{url: "https://mcp.example.com/.well-known/oauth-protected-resource", resource: "https://mcp.example.com"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, protectedResourceMetadataURLs(tt.metadataURL, tt.serverURL))
		})
	}
}

func TestBearerRoundTripperStaysOnServerOrigin(t *testing.T) {
	var seen []string
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req.Header.Get("Authorization"))
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
	})
	record := testOAuthRecord()
	transport := &bearerRoundTripper{
		base:   base,
		source: newOAuthSource("notion", record, nil, nil),
		origin: "https://mcp.example.com",
	}
	for _, target := range []string{"https://mcp.example.com/mcp", "https://elsewhere.example.com/mcp"} {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer static")
		resp, err := transport.RoundTrip(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
	}
	assert.Equal(t, []string{"Bearer access-secret", "Bearer static"}, seen)
}

func TestOAuthSourceReportsExpiredWithoutRefreshToken(t *testing.T) {
	record := testOAuthRecord()
	record.RefreshToken = ""
	record.Expiry = time.Now().Add(-time.Minute)
	source := newOAuthSource("notion", record, nil, nil)
	assert.Equal(t, OAuthStatusExpired, source.view().Status)
	_, err := source.accessToken(context.Background())
	require.ErrorIs(t, err, errOAuthExpired)
	assert.Equal(t, OAuthStatusError, source.view().Status)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
