package admin

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
)

// mcpOAuthFake adds the OAuth sign-in surface to mcpAdminFake.
type mcpOAuthFake struct {
	*mcpAdminFake
	startReq    mcpgateway.OAuthStartRequest
	startErr    error
	callback    mcpgateway.OAuthCallback
	completeErr error
	signedOut   string
	signOutErr  error
}

func (f *mcpOAuthFake) StartOAuth(_ context.Context, name string, req mcpgateway.OAuthStartRequest) (mcpgateway.OAuthStart, error) {
	f.startReq = req
	if f.startErr != nil {
		return mcpgateway.OAuthStart{}, f.startErr
	}
	return mcpgateway.OAuthStart{AuthorizationURL: "https://auth.example.com/authorize?state=s&client_id=" + name, Binding: "binding-1"}, nil
}

func (f *mcpOAuthFake) CompleteOAuth(_ context.Context, callback mcpgateway.OAuthCallback) (string, error) {
	f.callback = callback
	if errors.Is(f.completeErr, mcpgateway.ErrOAuthFlowUnknown) {
		return "", f.completeErr
	}
	return "notion", f.completeErr
}

func (f *mcpOAuthFake) SignOutOAuth(_ context.Context, name string) error {
	f.signedOut = name
	return f.signOutErr
}

func newMCPOAuthFake() *mcpOAuthFake {
	return &mcpOAuthFake{mcpAdminFake: newMCPAdminFake()}
}

func TestStartMCPServerOAuth(t *testing.T) {
	t.Run("returns the authorization URL and sets the binding cookie", func(t *testing.T) {
		fake := newMCPOAuthFake()
		h := NewHandler(nil, nil, WithMCPServers(fake))
		body := map[string]string{"redirect_uri": "https://gw.example.com/base/admin/mcp-oauth/callback", "client_id": "pre"}
		c, rec := echotest.Request(t, http.MethodPost, "/admin/mcp-servers/notion/oauth/start", body, echotest.WithPathValue("name", "notion"))
		require.NoError(t, h.StartMCPServerOAuth(c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		resp := echotest.Decode[startMCPOAuthResponse](t, rec)
		assert.Equal(t, "https://auth.example.com/authorize?state=s&client_id=notion", resp.AuthorizationURL)
		assert.Equal(t, "pre", fake.startReq.ClientID)

		cookies := rec.Result().Cookies()
		require.Len(t, cookies, 1)
		assert.Equal(t, mcpOAuthBindingCookie, cookies[0].Name)
		assert.Equal(t, "binding-1", cookies[0].Value)
		assert.Equal(t, "/base/admin/mcp-oauth/callback", cookies[0].Path)
		assert.True(t, cookies[0].HttpOnly)
		assert.True(t, cookies[0].Secure)
		assert.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)
	})

	tests := []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{name: "unknown server", err: mcpgateway.ErrNotFound, wantCode: http.StatusNotFound},
		{name: "client required", err: mcpgateway.ErrOAuthClientRequired, wantCode: http.StatusBadRequest, wantBody: "oauth_client_required"},
		{name: "bad redirect", err: mcpgateway.ErrOAuthInvalidRequest, wantCode: http.StatusBadRequest},
		{name: "unavailable", err: mcpgateway.ErrOAuthUnavailable, wantCode: http.StatusServiceUnavailable},
		{name: "discovery failure", err: errors.New("the server publishes no OAuth protected resource metadata"), wantCode: http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newMCPOAuthFake()
			fake.startErr = tt.err
			h := NewHandler(nil, nil, WithMCPServers(fake))
			c, rec := echotest.Request(t, http.MethodPost, "/admin/mcp-servers/notion/oauth/start",
				map[string]string{"redirect_uri": "https://gw.example.com/admin/mcp-oauth/callback"}, echotest.WithPathValue("name", "notion"))
			require.NoError(t, h.StartMCPServerOAuth(c))
			assert.Equal(t, tt.wantCode, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tt.wantBody)
			assert.Empty(t, rec.Result().Cookies())
		})
	}

	t.Run("gateways without oauth report unavailable", func(t *testing.T) {
		h := newMCPHandler(newMCPAdminFake())
		c, rec := echotest.Request(t, http.MethodPost, "/admin/mcp-servers/notion/oauth/start", map[string]string{}, echotest.WithPathValue("name", "notion"))
		require.NoError(t, h.StartMCPServerOAuth(c))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
}

func TestSignOutMCPServerOAuth(t *testing.T) {
	fake := newMCPOAuthFake()
	h := NewHandler(nil, nil, WithMCPServers(fake))
	c, rec := echotest.Request(t, http.MethodDelete, "/admin/mcp-servers/notion/oauth", nil, echotest.WithPathValue("name", "notion"))
	require.NoError(t, h.SignOutMCPServerOAuth(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "notion", fake.signedOut)

	fake.signOutErr = mcpgateway.ErrNotFound
	c, rec = echotest.Request(t, http.MethodDelete, "/admin/mcp-servers/missing/oauth", nil, echotest.WithPathValue("name", "missing"))
	require.NoError(t, h.SignOutMCPServerOAuth(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMCPOAuthCallbackRedirectsToDashboard(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantLocation string
	}{
		{name: "signed in", wantLocation: "../dashboard/mcp-servers?mcp_oauth=signed_in&server=notion"},
		{name: "failed", err: errors.New("token endpoint returned invalid_grant"), wantLocation: "../dashboard/mcp-servers?mcp_oauth=error&server=notion"},
		{name: "unknown state", err: mcpgateway.ErrOAuthFlowUnknown, wantLocation: "../dashboard/mcp-servers?mcp_oauth=expired"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newMCPOAuthFake()
			fake.completeErr = tt.err
			h := NewHandler(nil, nil, WithMCPServers(fake))
			c, rec := echotest.Request(t, http.MethodGet, MCPOAuthCallbackPath+"?state=s1&code=c1&iss=https%3A%2F%2Fas.example",
				nil, echotest.WithHeader("Cookie", mcpOAuthBindingCookie+"=binding-1"))
			require.NoError(t, h.MCPOAuthCallback(c))
			assert.Equal(t, http.StatusSeeOther, rec.Code)
			assert.Equal(t, tt.wantLocation, rec.Header().Get("Location"))
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			assert.NotContains(t, rec.Header().Get("Location"), "invalid_grant", "failure details stay out of the URL")
			assert.Equal(t, mcpgateway.OAuthCallback{State: "s1", Code: "c1", Issuer: "https://as.example", Binding: "binding-1"}, fake.callback)
		})
	}
}

func TestMCPServerViewCarriesOAuthStateWithoutTokens(t *testing.T) {
	fake := newMCPAdminFake()
	expires := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fake.views["notion"] = mcpgateway.ServerView{
		Spec:         mcpgateway.ServerSpec{Name: "notion", URL: "https://mcp.notion.com/mcp", Transport: "http", Enabled: true},
		Status:       mcpgateway.StatusConnected,
		AuthRequired: true,
		OAuth: &mcpgateway.OAuthView{
			Status: mcpgateway.OAuthStatusSignedIn, Issuer: "https://auth.notion.com", Scopes: []string{"read"}, ExpiresAt: expires,
		},
	}
	h := newMCPHandler(fake)
	c, rec := echotest.Request(t, http.MethodGet, "/admin/mcp-servers", nil)
	require.NoError(t, h.ListMCPServers(c))
	require.Equal(t, http.StatusOK, rec.Code)

	views := echotest.Decode[[]mcpServerViewResponse](t, rec)
	require.Len(t, views, 1)
	assert.True(t, views[0].AuthRequired)
	require.NotNil(t, views[0].OAuth)
	assert.Equal(t, "signed_in", views[0].OAuth.Status)
	require.NotNil(t, views[0].OAuth.ExpiresAt)
	assert.True(t, expires.Equal(*views[0].OAuth.ExpiresAt))
	assert.NotContains(t, rec.Body.String(), "token")
}
