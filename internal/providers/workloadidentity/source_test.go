package workloadidentity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
)

// tokenServer is a fake token endpoint. providertest cannot be used here: it
// imports the providers package, which imports this one.
type tokenServer struct {
	*httptest.Server

	mu       sync.Mutex
	requests []map[string]string
	headers  []http.Header
	// respond answers the n-th (0-based) exchange.
	respond func(n int) (int, string)
}

func newTokenServer(t *testing.T, respond func(n int) (int, string)) *tokenServer {
	t.Helper()
	ts := &tokenServer{respond: respond}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		var body map[string]string
		assert.NoError(t, json.Unmarshal(raw, &body))

		ts.mu.Lock()
		n := len(ts.requests)
		ts.requests = append(ts.requests, body)
		ts.headers = append(ts.headers, r.Header.Clone())
		ts.mu.Unlock()

		status, payload := ts.respond(n)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("request-id", "req_123")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *tokenServer) count() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.requests)
}

func (ts *tokenServer) request(t *testing.T, n int) (map[string]string, http.Header) {
	t.Helper()
	ts.mu.Lock()
	defer ts.mu.Unlock()
	require.Greater(t, len(ts.requests), n)
	return ts.requests[n], ts.headers[n]
}

// fakeClock is a settable time source, safe to read from the background
// refresh while a test advances it.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

// waitForRefresh blocks until the exchange running in the background, if
// any, has finished.
func waitForRefresh(s *Source) {
	s.mu.Lock()
	done := s.inflight
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

func tokenJSON(token string, expiresIn int) string {
	return `{"access_token":"` + token + `","token_type":"Bearer","expires_in":` + itoa(expiresIn) + `}`
}

func itoa(n int) string { return strconv.Itoa(n) }

func writeIdentityToken(t *testing.T, path, token string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(token+"\n"), 0o600))
}

func newSource(t *testing.T, cfg Config, clock *fakeClock) *Source {
	t.Helper()
	source, err := New(cfg, nil)
	require.NoError(t, err)
	source.now = clock.Now
	return source
}

// anthropicConfig returns a complete jwt-bearer configuration reading the
// literal identity token "jwt".
func anthropicConfig(endpoint string) Config {
	return Config{
		Provider:         "anthropic",
		Grant:            JWTBearer,
		TokenEndpoint:    endpoint,
		IdentityToken:    "jwt",
		FederationRuleID: "fdrl_1",
		OrganizationID:   "00000000-0000-0000-0000-000000000000",
		ServiceAccountID: "svac_1",
	}
}

func TestSourceExchangeRequestBody(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.WorkloadIdentityConfig
		gr   Grant
		want map[string]string
	}{
		{
			name: "openai token exchange",
			gr:   TokenExchange,
			cfg: config.WorkloadIdentityConfig{
				IdentityToken:      "jwt-literal",
				IdentityProviderID: "idp_1",
				ServiceAccountID:   "sa_1",
			},
			want: map[string]string{
				"grant_type":           "urn:ietf:params:oauth:grant-type:token-exchange",
				"subject_token_type":   "urn:ietf:params:oauth:token-type:jwt",
				"subject_token":        "jwt-literal",
				"identity_provider_id": "idp_1",
				"service_account_id":   "sa_1",
			},
		},
		{
			name: "anthropic jwt bearer with workspace",
			gr:   JWTBearer,
			cfg: config.WorkloadIdentityConfig{
				IdentityToken:    "jwt-literal",
				FederationRuleID: "fdrl_1",
				OrganizationID:   "org-uuid",
				ServiceAccountID: "svac_1",
				WorkspaceID:      "wrkspc_1",
			},
			want: map[string]string{
				"grant_type":         "urn:ietf:params:oauth:grant-type:jwt-bearer",
				"assertion":          "jwt-literal",
				"federation_rule_id": "fdrl_1",
				"organization_id":    "org-uuid",
				"service_account_id": "svac_1",
				"workspace_id":       "wrkspc_1",
			},
		},
		{
			name: "anthropic jwt bearer omits an unset workspace",
			gr:   JWTBearer,
			cfg: config.WorkloadIdentityConfig{
				IdentityToken:    "jwt-literal",
				FederationRuleID: "fdrl_1",
				OrganizationID:   "org-uuid",
				ServiceAccountID: "svac_1",
			},
			want: map[string]string{
				"grant_type":         "urn:ietf:params:oauth:grant-type:jwt-bearer",
				"assertion":          "jwt-literal",
				"federation_rule_id": "fdrl_1",
				"organization_id":    "org-uuid",
				"service_account_id": "svac_1",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTokenServer(t, func(int) (int, string) { return http.StatusOK, tokenJSON("access-1", 3600) })
			source := newSource(t, Config{Grant: tt.gr, TokenEndpoint: server.URL, WorkloadIdentityConfig: tt.cfg}, newClock())

			token, err := source.Token(t.Context())
			require.NoError(t, err)
			assert.Equal(t, "access-1", token)

			body, header := server.request(t, 0)
			assert.Equal(t, tt.want, body)
			assert.Equal(t, "application/json", header.Get("Content-Type"))
		})
	}
}

func TestSourceCachesAndRefreshesAheadOfExpiry(t *testing.T) {
	server := newTokenServer(t, func(n int) (int, string) {
		return http.StatusOK, tokenJSON("access-"+itoa(n+1), 3600)
	})
	tokenFile := filepath.Join(t.TempDir(), "token")
	writeIdentityToken(t, tokenFile, "jwt-1")
	clock := newClock()
	cfg := anthropicConfig(server.URL)
	cfg.IdentityTokenFile = tokenFile
	source := newSource(t, cfg, clock)

	token, err := source.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "access-1", token)

	// Inside the token's lifetime, before the refresh window: cached.
	clock.Advance(57 * time.Minute)
	token, err = source.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "access-1", token)
	assert.Equal(t, 1, server.count())

	// The projected token rotated on disk; the refresh window opens two
	// minutes before expiry. The still-valid token is served while the
	// background exchange re-reads the file.
	writeIdentityToken(t, tokenFile, "jwt-2")
	clock.Advance(90 * time.Second)
	token, err = source.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "access-1", token)
	waitForRefresh(source)
	token, err = source.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "access-2", token)
	require.Equal(t, 2, server.count())
	body, _ := server.request(t, 1)
	assert.Equal(t, "jwt-2", body["assertion"])
}

func TestSourcePrefersAbsoluteExpiry(t *testing.T) {
	clock := newClock()
	expiresAt := clock.Now().Add(10 * time.Minute).Unix()
	server := newTokenServer(t, func(n int) (int, string) {
		// expires_in claims an hour; expires_at, which wins, says ten minutes.
		return http.StatusOK, `{"access_token":"access-` + itoa(n+1) + `","expires_in":3600,"expires_at":` + itoa(int(expiresAt)) + `}`
	})
	source := newSource(t, Config{Grant: TokenExchange, TokenEndpoint: server.URL,
		IdentityToken: "jwt", IdentityProviderID: "idp_1", ServiceAccountID: "sa_1"}, clock)

	_, err := source.Token(t.Context())
	require.NoError(t, err)
	clock.Advance(9 * time.Minute)
	_, err = source.Token(t.Context())
	require.NoError(t, err)
	waitForRefresh(source)
	token, err := source.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "access-2", token)
}

func TestSourceScalesRefreshMarginForShortLivedTokens(t *testing.T) {
	server := newTokenServer(t, func(n int) (int, string) {
		return http.StatusOK, tokenJSON("access-"+itoa(n+1), 60)
	})
	clock := newClock()
	source := newSource(t, anthropicConfig(server.URL), clock)

	_, err := source.Token(t.Context())
	require.NoError(t, err)
	// A 60s token refreshes at half its lifetime, not on every request.
	clock.Advance(20 * time.Second)
	_, err = source.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, server.count())
	clock.Advance(15 * time.Second)
	_, err = source.Token(t.Context())
	require.NoError(t, err)
	waitForRefresh(source)
	token, err := source.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "access-2", token)
}

func TestSourceServesCachedTokenWhileRefreshFails(t *testing.T) {
	server := newTokenServer(t, func(n int) (int, string) {
		if n == 0 {
			return http.StatusOK, tokenJSON("access-1", 3600)
		}
		return http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}`
	})
	clock := newClock()
	source := newSource(t, anthropicConfig(server.URL), clock)

	_, err := source.Token(t.Context())
	require.NoError(t, err)

	// Refresh window: the exchange fails, the cached token is still served.
	clock.Advance(59 * time.Minute)
	token, err := source.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "access-1", token)
	waitForRefresh(source)
	require.Equal(t, 2, server.count())

	// Within the failure backoff no new exchange is attempted.
	clock.Advance(2 * time.Second)
	_, err = source.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 2, server.count())

	// Too close to expiry: the cached token is no longer served.
	clock.Advance(40 * time.Second)
	token, err = source.Token(t.Context())
	require.Error(t, err)
	assert.Empty(t, token)
	assert.Contains(t, err.Error(), "HTTP 503")
	assert.Contains(t, err.Error(), "temporarily_unavailable")
	assert.Equal(t, 3, server.count())
}

func TestSourceErrorsNeverCarryTokens(t *testing.T) {
	const assertion = "eyJhbGciOiJSUzI1NiJ9.secret-assertion.sig"
	server := newTokenServer(t, func(int) (int, string) {
		return http.StatusUnauthorized, `{"type":"error","error":{"type":"authentication_error","message":"Authentication failed"},"echo":"` + assertion + `"}`
	})
	cfg := anthropicConfig(server.URL)
	cfg.IdentityToken = assertion
	source := newSource(t, cfg, newClock())

	_, err := source.Token(t.Context())
	require.Error(t, err)
	assert.Equal(t, "token endpoint returned HTTP 401: authentication_error: Authentication failed (request id req_123)", err.Error())
	assert.NotContains(t, err.Error(), assertion)
}

func TestSourceRedactsEchoedIdentityToken(t *testing.T) {
	const assertion = "eyJhbGciOiJSUzI1NiJ9.secret-assertion.sig"
	server := newTokenServer(t, func(int) (int, string) {
		return http.StatusBadRequest, `{"error":"invalid_grant","error_description":"rejected subject_token ` + assertion + `"}`
	})
	cfg := anthropicConfig(server.URL)
	cfg.IdentityToken = assertion
	source := newSource(t, cfg, newClock())

	_, err := source.Token(t.Context())
	require.Error(t, err)
	assert.Equal(t, "token endpoint returned HTTP 400: invalid_grant: rejected subject_token [redacted] (request id req_123)", err.Error())
}

func TestSourceRefreshDoesNotBlockCallersWithUsableToken(t *testing.T) {
	release := make(chan struct{})
	server := newTokenServer(t, func(n int) (int, string) {
		if n > 0 {
			<-release // the refresh hangs until the test lets it finish
		}
		return http.StatusOK, tokenJSON("access-"+itoa(n+1), 3600)
	})
	clock := newClock()
	source := newSource(t, anthropicConfig(server.URL), clock)
	_, err := source.Token(t.Context())
	require.NoError(t, err)

	clock.Advance(59 * time.Minute)
	for range 3 {
		token, err := source.Token(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "access-1", token, "a usable token is served while the refresh hangs")
	}
	close(release)
	waitForRefresh(source)
	token, err := source.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "access-2", token)
	assert.Equal(t, 2, server.count())
}

func TestSourceWaitEndsWithCallerContext(t *testing.T) {
	release := make(chan struct{})
	server := newTokenServer(t, func(int) (int, string) {
		<-release
		return http.StatusOK, tokenJSON("access-1", 3600)
	})
	source := newSource(t, anthropicConfig(server.URL), newClock())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := source.Token(ctx)
	require.ErrorIs(t, err, context.Canceled)

	// The shared exchange keeps running for the next caller.
	close(release)
	token, err := source.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "access-1", token)
	assert.Equal(t, 1, server.count())
}

func TestSourceRejectsUnusableResponses(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"missing access token", `{"expires_in":3600}`, "no access_token"},
		{"not json", `<html>`, "unreadable response"},
		{"already expired", `{"access_token":"a","expires_at":1}`, "expired access token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTokenServer(t, func(int) (int, string) { return http.StatusOK, tt.body })
			source := newSource(t, anthropicConfig(server.URL), newClock())

			_, err := source.Token(t.Context())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestSourceReportsMissingIdentityTokenFile(t *testing.T) {
	server := newTokenServer(t, func(int) (int, string) { return http.StatusOK, tokenJSON("a", 3600) })
	cfg := anthropicConfig(server.URL)
	cfg.IdentityTokenFile = filepath.Join(t.TempDir(), "missing")
	source := newSource(t, cfg, newClock())

	_, err := source.Token(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read identity_token_file")
	assert.Equal(t, 0, server.count())
}

func TestConfigValidate(t *testing.T) {
	anthropic := anthropicConfig("https://api.anthropic.com/v1/oauth/token")
	openai := Config{Grant: TokenExchange, TokenEndpoint: OpenAITokenEndpoint,
		IdentityToken: "jwt", IdentityProviderID: "idp_1", ServiceAccountID: "sa_1"}
	tests := []struct {
		name    string
		mutate  func(Config) Config
		base    Config
		wantErr string
	}{
		{"anthropic complete", func(c Config) Config { return c }, anthropic, ""},
		{"openai complete", func(c Config) Config { return c }, openai, ""},
		{"no identity token", func(c Config) Config { c.IdentityToken = ""; return c }, anthropic, "identity_token_file or identity_token is required"},
		{"anthropic without rule", func(c Config) Config { c.FederationRuleID = ""; return c }, anthropic, "federation_rule_id is required"},
		{"anthropic without organization", func(c Config) Config { c.OrganizationID = " "; return c }, anthropic, "organization_id is required"},
		{"openai without identity provider", func(c Config) Config { c.IdentityProviderID = ""; return c }, openai, "identity_provider_id is required"},
		{"without service account", func(c Config) Config { c.ServiceAccountID = ""; return c }, openai, "service_account_id is required"},
		{"without endpoint", func(c Config) Config { c.TokenEndpoint = ""; return c }, openai, "token_endpoint is required"},
		{"unknown grant", func(c Config) Config { c.Grant = "password"; return c }, openai, "unsupported grant"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.mutate(tt.base).Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestDefaultTokenEndpoint(t *testing.T) {
	assert.Equal(t, OpenAITokenEndpoint, DefaultTokenEndpoint(TokenExchange, "https://proxy.internal/v1"))
	assert.Equal(t, "https://api.anthropic.com/v1/oauth/token", DefaultTokenEndpoint(JWTBearer, "https://api.anthropic.com/v1/"))
	assert.Empty(t, DefaultTokenEndpoint("other", "https://x"))
}

func TestSourceConcurrentCallersShareOneExchange(t *testing.T) {
	server := newTokenServer(t, func(int) (int, string) { return http.StatusOK, tokenJSON("access-1", 3600) })
	source := newSource(t, anthropicConfig(server.URL), newClock())

	type result struct {
		token string
		err   error
	}
	results := make(chan result, 20)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			token, err := source.Token(t.Context())
			results <- result{token, err}
		})
	}
	wg.Wait()
	close(results)
	for r := range results {
		require.NoError(t, r.err)
		assert.Equal(t, "access-1", r.token)
	}
	assert.Equal(t, 1, server.count())
}
