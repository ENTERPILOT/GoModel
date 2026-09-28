package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// TestMasterKeyUserPathHeaderScopesRestrictedModelAccess pins the contract the
// dashboard Playground relies on: a master-key request may scope itself to a
// user path through the user-path header (X-GoModel-User-Path by default, or
// the configured USER_PATH_HEADER), and that path both lands in the request
// snapshot and satisfies user_path-restricted virtual-model policies. Without
// the header the same master-key request is denied.
func TestMasterKeyUserPathHeaderScopesRestrictedModelAccess(t *testing.T) {
	service, err := virtualmodels.NewService(newAliasesTestStore(virtualmodels.VirtualModel{
		Source:    "openai/gpt-4o",
		UserPaths: []string{"/team/x"},
		Enabled:   true,
	}), &aliasesTestCatalog{
		supported:     map[string]bool{"openai/gpt-4o": true},
		providerTypes: map[string]string{"openai/gpt-4o": "openai"},
		models:        map[string]core.Model{"openai/gpt-4o": {ID: "gpt-4o", Object: "model"}},
	}, true)
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))
	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}

	const customHeader = "X-Tenant-Path"

	tests := []struct {
		name string
		// configuredHeader is the server's USER_PATH_HEADER; empty keeps the default.
		configuredHeader string
		// sentHeader is the header name the request carries; empty means the default.
		sentHeader     string
		userPathHeader string
		wantSnapshot   string
		wantAllowed    bool
	}{
		{
			name:           "user path header passes restricted model",
			userPathHeader: "/team/x",
			wantSnapshot:   "/team/x",
			wantAllowed:    true,
		},
		{
			name:        "missing header denies restricted model",
			wantAllowed: false,
		},
		{
			name:           "unlisted user path header denies restricted model",
			userPathHeader: "/team/y",
			wantSnapshot:   "/team/y",
			wantAllowed:    false,
		},
		{
			name:             "configured header name passes restricted model",
			configuredHeader: customHeader,
			sentHeader:       customHeader,
			userPathHeader:   "/team/x",
			wantSnapshot:     "/team/x",
			wantAllowed:      true,
		},
		{
			name:             "default header is ignored when a custom name is configured",
			configuredHeader: customHeader,
			userPathHeader:   "/team/x",
			wantAllowed:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain := RequestSnapshotCapture(tt.configuredHeader)(AuthMiddleware("master-key", nil)(func(c *echo.Context) error {
				ctx := c.Request().Context()
				snapshot := core.GetRequestSnapshot(ctx)
				require.NotNil(t, snapshot)
				assert.Equal(t, tt.wantSnapshot, snapshot.UserPath)
				assert.Equal(t, tt.wantAllowed, service.AllowsModel(ctx, selector))
				return c.String(http.StatusOK, "ok")
			}))

			opts := []echotest.Option{echotest.WithHeader("Authorization", "Bearer master-key")}
			if tt.userPathHeader != "" {
				opts = append(opts, echotest.WithHeader(core.UserPathHeaderName(tt.sentHeader), tt.userPathHeader))
			}
			c, rec := echotest.Post(t, "/v1/chat/completions",
				`{"model":"openai/gpt-4o","messages":[{"role":"user","content":"hi"}]}`, opts...)

			require.NoError(t, chain(c))
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

// TestTransportOwnedEndpointsKeepHeaderUserPath covers model endpoints that
// own their transport (MCP, realtime, audio uploads). They take no request
// snapshot, so the header path lives only in the effective user path, and the
// explicit-credential reset must not erase it: a master-key or unbound-key
// caller keeps its header path, a bound key still wins, and identity from an
// outer extension session never survives an explicit credential.
func TestTransportOwnedEndpointsKeepHeaderUserPath(t *testing.T) {
	authenticator := mockAuthenticator{
		enabled:   true,
		tokenToID: map[string]string{"sk_bound": "key-bound", "sk_unbound": "key-unbound"},
		tokenPath: map[string]string{"sk_bound": "/team/bound"},
	}
	tests := []struct {
		name          string
		path          string
		token         string
		headerPath    string
		outerIdentity string
		// configuredHeader is the server's USER_PATH_HEADER; empty keeps the default.
		configuredHeader string
		want             string
	}{
		{name: "master key on /mcp keeps header path", path: "/mcp", token: "master-key", headerPath: "/eng/platform", want: "/eng/platform"},
		{name: "master key on pinned /mcp/{server}", path: "/mcp/github", token: "master-key", headerPath: "/eng", want: "/eng"},
		{name: "master key on audio transcription", path: "/v1/audio/transcriptions", token: "master-key", headerPath: "/team/x", want: "/team/x"},
		{name: "master key on realtime", path: "/v1/realtime", token: "master-key", headerPath: "/team/x", want: "/team/x"},
		{name: "master key without header stays global", path: "/mcp", token: "master-key", want: ""},
		{name: "unbound managed key keeps header path", path: "/mcp", token: "sk_unbound", headerPath: "/eng/platform", want: "/eng/platform"},
		{name: "bound managed key wins over header", path: "/mcp", token: "sk_bound", headerPath: "/eng/platform", want: "/team/bound"},
		{name: "outer extension identity is dropped", path: "/mcp", token: "master-key", outerIdentity: "/ext/session", want: ""},
		{name: "header replaces outer extension identity", path: "/mcp", token: "master-key", outerIdentity: "/ext/session", headerPath: "/eng", want: "/eng"},
		{name: "configured header name on /mcp", path: "/mcp", token: "master-key", configuredHeader: "X-Tenant-Path", headerPath: "/eng", want: "/eng"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			auth := AuthMiddlewareWithAuthenticator("master-key", authenticator, nil, tt.configuredHeader)(func(c *echo.Context) error {
				got = core.UserPathFromContext(c.Request().Context())
				return c.String(http.StatusOK, "ok")
			})
			// An outer extension session installs its identity before the
			// gateway's auth middleware runs.
			outer := func(c *echo.Context) error {
				if tt.outerIdentity != "" {
					req := c.Request()
					c.SetRequest(req.WithContext(core.WithEffectiveUserPath(req.Context(), tt.outerIdentity)))
				}
				return auth(c)
			}
			chain := RequestSnapshotCapture(tt.configuredHeader)(outer)

			opts := []echotest.Option{echotest.WithHeader("Authorization", "Bearer "+tt.token)}
			if tt.headerPath != "" {
				opts = append(opts, echotest.WithHeader(core.UserPathHeaderName(tt.configuredHeader), tt.headerPath))
			}
			c, rec := echotest.Post(t, tt.path, `{}`, opts...)

			require.NoError(t, chain(c))
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.want, got)
		})
	}
}
