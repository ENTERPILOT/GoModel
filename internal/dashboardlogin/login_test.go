package dashboardlogin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testUsername = "admin"
	testPassword = "correct horse battery staple"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	service, err := New(testUsername, testPassword)
	require.NoError(t, err)
	return service
}

func sessionRequest(method, token string) *http.Request {
	req := httptest.NewRequest(method, "/admin/models", nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	}
	return req
}

func TestNewRequiresUsernameAndPassword(t *testing.T) {
	tests := []struct {
		name     string
		username string
		password string
	}{
		{name: "empty password", username: testUsername},
		{name: "empty username", password: testPassword},
		{name: "blank username", username: "  ", password: testPassword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.username, tt.password)
			require.Error(t, err)
		})
	}
}

func TestCredentialsMatch(t *testing.T) {
	service := newTestService(t)
	tests := []struct {
		name     string
		username string
		password string
		want     bool
	}{
		{name: "configured credentials", username: testUsername, password: testPassword, want: true},
		{name: "username with surrounding space", username: " admin ", password: testPassword, want: true},
		{name: "wrong password", username: testUsername, password: "wrong"},
		{name: "wrong username", username: "root", password: testPassword},
		{name: "username differing in case", username: "Admin", password: testPassword},
		{name: "password with trailing space", username: testUsername, password: testPassword + " "},
		{name: "empty", username: "", password: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.credentialsMatch(tt.username, tt.password)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Password checks run one at a time; a sign-in that would only join a long
// line behind them is refused unchecked instead of held open.
func TestCredentialsMatchRefusesWhenTooManyAreWaiting(t *testing.T) {
	service := newTestService(t)
	for range maxPendingChecks {
		service.checks <- struct{}{}
	}

	_, err := service.credentialsMatch(testUsername, testPassword)
	require.ErrorIs(t, err, errBusy)

	<-service.checks
	got, err := service.credentialsMatch(testUsername, testPassword)
	require.NoError(t, err)
	assert.True(t, got)
}

func TestSessionTokenLifetime(t *testing.T) {
	service := newTestService(t)
	issuedAt := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return issuedAt }

	token, expires := service.issue()
	assert.Equal(t, issuedAt.Add(SessionTTL), expires)

	got, ok := service.verify(token)
	require.True(t, ok)
	assert.True(t, expires.Equal(got))

	service.now = func() time.Time { return expires.Add(-time.Second) }
	_, ok = service.verify(token)
	assert.True(t, ok, "a session is valid until it expires")

	service.now = func() time.Time { return expires }
	_, ok = service.verify(token)
	assert.False(t, ok, "a session ends at its expiry")
}

func TestSessionTokenRejectsForgery(t *testing.T) {
	service := newTestService(t)
	token, _ := service.issue()

	otherPassword, err := New(testUsername, "another password")
	require.NoError(t, err)
	otherUsername, err := New("operator", testPassword)
	require.NoError(t, err)
	foreign, _ := otherPassword.issue()
	renamed, _ := otherUsername.issue()

	expiry, mac, _ := strings.Cut(token, ".")

	tests := []struct {
		name  string
		token string
	}{
		{name: "empty", token: ""},
		{name: "no MAC", token: expiry},
		{name: "MAC that is not base64", token: expiry + ".not a MAC!"},
		{name: "truncated MAC", token: token[:len(token)-4]},
		{name: "later expiry", token: "9" + expiry + "." + mac},
		{name: "expiry that is not a number", token: "never." + mac},
		{name: "signed for another password", token: foreign},
		{name: "signed for another username", token: renamed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := service.verify(tt.token)
			assert.False(t, ok)
		})
	}
}

// Replicas configured alike accept each other's sessions without sharing state.
func TestSessionTokenAcceptedByAnotherInstance(t *testing.T) {
	token, _ := newTestService(t).issue()
	_, ok := newTestService(t).verify(token)
	assert.True(t, ok)
}

func TestAuthenticateRequest(t *testing.T) {
	service := newTestService(t)
	token, _ := service.issue()

	t.Run("valid session", func(t *testing.T) {
		got, err := service.AuthenticateRequest(context.Background(), sessionRequest(http.MethodGet, token))
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, testUsername, got.PrincipalID)
		assert.True(t, got.DashboardAccess)
		assert.Equal(t, AuthMethod, got.Method)
		assert.Empty(t, got.UserPath, "the dashboard login administers the whole gateway")
	})

	notApplicable := []struct {
		name string
		req  func() *http.Request
	}{
		{name: "no cookie", req: func() *http.Request { return sessionRequest(http.MethodGet, "") }},
		{name: "invalid cookie", req: func() *http.Request { return sessionRequest(http.MethodGet, "forged") }},
		{name: "cross-site request", req: func() *http.Request {
			req := sessionRequest(http.MethodPost, token)
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			return req
		}},
	}
	for _, tt := range notApplicable {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.AuthenticateRequest(context.Background(), tt.req())
			require.NoError(t, err)
			assert.Nil(t, got)
		})
	}
}

func TestSameOrigin(t *testing.T) {
	tests := []struct {
		name      string
		fetchSite string
		origin    string
		want      bool
	}{
		{name: "no browser headers", want: true},
		{name: "same-origin fetch", fetchSite: "same-origin", want: true},
		{name: "address bar navigation", fetchSite: "none", want: true},
		{name: "sibling subdomain", fetchSite: "same-site"},
		{name: "another site", fetchSite: "cross-site"},
		{name: "same-origin wins over a proxy-rewritten host", fetchSite: "same-origin", origin: "https://gateway.example.com", want: true},
		{name: "older browser, matching origin", origin: "http://example.com", want: true},
		{name: "older browser, matching origin in another case", origin: "http://EXAMPLE.com", want: true},
		{name: "older browser, foreign origin", origin: "https://evil.example"},
		{name: "older browser, opaque origin", origin: "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://example.com/admin/session", nil)
			if tt.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			assert.Equal(t, tt.want, sameOrigin(req))
		})
	}
}
