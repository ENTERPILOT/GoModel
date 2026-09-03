package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
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
			e := echo.New()
			chain := RequestSnapshotCapture(tt.configuredHeader)(AuthMiddleware("master-key", nil)(func(c *echo.Context) error {
				ctx := c.Request().Context()
				snapshot := core.GetRequestSnapshot(ctx)
				require.NotNil(t, snapshot)
				assert.Equal(t, tt.wantSnapshot, snapshot.UserPath)
				assert.Equal(t, tt.wantAllowed, service.AllowsModel(ctx, selector))
				return c.String(http.StatusOK, "ok")
			}))

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"openai/gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer master-key")
			if tt.userPathHeader != "" {
				req.Header.Set(core.UserPathHeaderName(tt.sentHeader), tt.userPathHeader)
			}
			rec := httptest.NewRecorder()

			require.NoError(t, chain(e.NewContext(req, rec)))
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}
