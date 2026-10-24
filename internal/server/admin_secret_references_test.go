package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/admin"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/builtin"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/enterpilot/gomodel/pluginapi"
)

const (
	secretRefMasterKey = "master-key"
	secretRefDashboard = "sk_gom_dashboard"
)

// secretRefCarrier is one admin-saved entity whose secret field accepts a
// secret reference.
type secretRefCarrier struct {
	name string
	path string
	// body is the upsert payload with secret as the secret field's value.
	body func(secret string) string
	// mask is what the view shows for a literal secret; sending it back keeps
	// the stored value.
	mask string
	// stored reads the secret field as persisted.
	stored func(t *testing.T) string
	// edit is body with a field changed that does not decide where the
	// entity sends its secrets.
	edit func(secret string) string
	// repoints are body with one destination field changed, by field.
	repoints map[string]func(secret string) string
	// destination reads the destination fields as persisted.
	destination func(t *testing.T) string
}

// newSecretRefServer wires the admin API over real MCP, provider-credential,
// and guardrail services, with a managed key that has dashboard access.
func newSecretRefServer(t *testing.T, masterKey string) (*Server, []secretRefCarrier) {
	t.Helper()
	ctx := t.Context()
	db := sqlxtest.NewSQLite(t)

	mcpStore, err := mcpgateway.NewSQLStore(ctx, db)
	require.NoError(t, err)
	mcpService, err := mcpgateway.NewService(ctx, mcpgateway.Options{Store: mcpStore})
	require.NoError(t, err)
	t.Cleanup(mcpService.Close)

	factory := providers.NewProviderFactory()
	for _, providerType := range []string{"openai", "anthropic"} {
		factory.Add(providers.Registration{
			Type: providerType,
			New: func(providers.ProviderConfig, providers.ProviderOptions) core.Provider {
				return &mockProvider{modelsResponse: &core.ModelsResponse{Object: "list", Data: []core.Model{{ID: "gpt-test", Object: "model"}}}}
			},
		})
	}
	credentialStore, err := providers.NewSQLCredentialStore(ctx, db)
	require.NoError(t, err)
	credentials, err := providers.NewCredentialsService(ctx, factory, providers.NewModelRegistry(), credentialStore, nil, config.ResilienceConfig{}, nil)
	require.NoError(t, err)

	catalog := plugins.NewCatalog()
	for _, plugin := range builtin.All() {
		require.NoError(t, catalog.Register(plugin, plugins.SourceBuiltin))
	}
	guardrailStore, err := guardrails.NewSQLStore(ctx, db)
	require.NoError(t, err)
	guardrailService, err := guardrails.NewService(guardrailStore, catalog, plugins.HostDeps{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = guardrailService.Close(context.Background()) })

	srv := New(&mockProvider{}, &Config{
		MasterKey:             masterKey,
		AdminEndpointsEnabled: true,
		AdminHandler: admin.NewHandler(nil, nil,
			admin.WithMCPServers(mcpService),
			admin.WithProviderCredentials(credentials),
			admin.WithGuardrailService(guardrailService),
		),
		Authenticator: mockAuthenticator{
			enabled:        true,
			tokenToID:      map[string]string{secretRefDashboard: "key-dashboard"},
			tokenDashboard: map[string]bool{secretRefDashboard: true},
		},
	})

	carriers := []secretRefCarrier{
		{
			name: "mcp server header",
			path: "/admin/mcp-servers",
			body: func(secret string) string {
				return `{"name":"docs","url":"http://127.0.0.1:1/mcp","headers":{"Authorization":` + strconv.Quote(secret) + `}}`
			},
			mask: "***",
			stored: func(t *testing.T) string {
				row, err := mcpStore.Get(t.Context(), "docs")
				require.NoError(t, err)
				return row.Headers["Authorization"]
			},
			edit: func(secret string) string {
				return `{"name":"docs","url":"http://127.0.0.1:1/mcp","description":"edited","headers":{"Authorization":` + strconv.Quote(secret) + `}}`
			},
			repoints: map[string]func(string) string{
				"url": func(secret string) string {
					return `{"name":"docs","url":"http://localhost:2/other","headers":{"Authorization":` + strconv.Quote(secret) + `}}`
				},
			},
			destination: func(t *testing.T) string {
				row, err := mcpStore.Get(t.Context(), "docs")
				require.NoError(t, err)
				return row.URL
			},
		},
		{
			name: "provider credential api key",
			path: "/admin/provider-credentials",
			body: func(secret string) string {
				return `{"name":"acme","type":"openai","api_keys":[` + strconv.Quote(secret) + `]}`
			},
			mask: "***********",
			stored: func(t *testing.T) string {
				row, err := credentialStore.Get(t.Context(), "acme")
				require.NoError(t, err)
				require.Len(t, row.APIKeys, 1)
				return row.APIKeys[0]
			},
			edit: func(secret string) string {
				return `{"name":"acme","type":"openai","api_keys":[` + strconv.Quote(secret) + `],"models":["gpt-test"]}`
			},
			repoints: map[string]func(string) string{
				"type": func(secret string) string {
					return `{"name":"acme","type":"anthropic","api_keys":[` + strconv.Quote(secret) + `]}`
				},
				"base_url": func(secret string) string {
					return `{"name":"acme","type":"openai","base_url":"http://127.0.0.1:2/v1","api_keys":[` + strconv.Quote(secret) + `]}`
				},
				"backend": func(secret string) string {
					return `{"name":"acme","type":"openai","backend":"aistudio","api_keys":[` + strconv.Quote(secret) + `]}`
				},
				"proxy_url": func(secret string) string {
					return `{"name":"acme","type":"openai","proxy_url":"http://127.0.0.1:3","api_keys":[` + strconv.Quote(secret) + `]}`
				},
			},
			destination: func(t *testing.T) string {
				row, err := credentialStore.Get(t.Context(), "acme")
				require.NoError(t, err)
				return strings.Join([]string{row.Type, row.BaseURL, row.Backend, row.ProxyURL}, " ")
			},
		},
		{
			name: "guardrail secret",
			path: "/admin/guardrails",
			body: func(secret string) string {
				return `{"name":"pii","type":"presidio","config":{"analyzer_url":"http://127.0.0.1:1","api_key":` + strconv.Quote(secret) + `}}`
			},
			mask: plugins.SecretMask,
			stored: func(t *testing.T) string {
				row, err := guardrailStore.Get(t.Context(), "pii")
				require.NoError(t, err)
				return plugins.SecretValues(catalogSchema(t, catalog, "presidio"), row.Config)["api_key"]
			},
			edit: func(secret string) string {
				return `{"name":"pii","type":"presidio","config":{"analyzer_url":"http://127.0.0.1:1","score_threshold":0.5,"api_key":` + strconv.Quote(secret) + `}}`
			},
			repoints: map[string]func(string) string{
				"config.analyzer_url": func(secret string) string {
					return `{"name":"pii","type":"presidio","config":{"analyzer_url":"http://localhost:2","api_key":` + strconv.Quote(secret) + `}}`
				},
			},
			destination: func(t *testing.T) string {
				row, err := guardrailStore.Get(t.Context(), "pii")
				require.NoError(t, err)
				return row.Type + " " + string(row.Config)
			},
		},
	}
	return srv, carriers
}

func catalogSchema(t *testing.T, catalog *plugins.Catalog, pluginType string) []pluginapi.Field {
	t.Helper()
	entry, ok := catalog.Lookup(pluginType)
	require.True(t, ok)
	return entry.Manifest.ConfigSchema
}

func putAdmin(t *testing.T, srv *Server, path, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestAdminSecretReferences_DashboardKeyCannotAddOrChangeReferences(t *testing.T) {
	t.Setenv("SECRETREF_SEED", "seed-value")
	t.Setenv("SECRETREF_OTHER", "other-value")
	const seed = "${env:SECRETREF_SEED}"

	srv, carriers := newSecretRefServer(t, secretRefMasterKey)
	for _, carrier := range carriers {
		t.Run(carrier.name, func(t *testing.T) {
			rec := putAdmin(t, srv, carrier.path, secretRefMasterKey, carrier.body(seed))
			require.Equal(t, http.StatusOK, rec.Code, "the master key saves a reference: %s", rec.Body.String())
			require.Equal(t, seed, carrier.stored(t))

			for _, value := range []string{"${env:SECRETREF_OTHER}", "${file:/etc/hostname}", "pre-${env:SECRETREF_OTHER}"} {
				rec = putAdmin(t, srv, carrier.path, secretRefDashboard, carrier.body(value))
				assert.Equal(t, http.StatusForbidden, rec.Code, "dashboard key adds %s: %s", value, rec.Body.String())
				assert.Contains(t, rec.Body.String(), "secret_reference_requires_master_key")
				assert.NotContains(t, rec.Body.String(), "SECRETREF_OTHER", "the error never repeats the value")
				assert.NotContains(t, rec.Body.String(), "/etc/hostname", "the error never repeats the value")
				assert.Equal(t, seed, carrier.stored(t), "a refused save leaves the stored entity unchanged")
			}

			// Re-sending the stored reference, as the view shows it, is no change.
			rec = putAdmin(t, srv, carrier.path, secretRefDashboard, carrier.body(seed))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, seed, carrier.stored(t))

			// A literal secret, and an escaped $${ that stays literal, still save.
			for _, literal := range []string{"sk-literal", "$${env:SECRETREF_OTHER}"} {
				rec = putAdmin(t, srv, carrier.path, secretRefDashboard, carrier.body(literal))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, literal, carrier.stored(t))
			}

			// The mask keeps the stored literal.
			rec = putAdmin(t, srv, carrier.path, secretRefDashboard, carrier.body(carrier.mask))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "$${env:SECRETREF_OTHER}", carrier.stored(t))

			rec = putAdmin(t, srv, carrier.path, secretRefMasterKey, carrier.body("${env:SECRETREF_OTHER}"))
			require.Equal(t, http.StatusOK, rec.Code, "the master key changes a reference: %s", rec.Body.String())
			assert.Equal(t, "${env:SECRETREF_OTHER}", carrier.stored(t))
		})
	}
}

func TestAdminSecretReferences_DashboardKeyCannotRepointHeldReferences(t *testing.T) {
	t.Setenv("SECRETREF_SEED", "seed-value")
	const seed = "${env:SECRETREF_SEED}"

	srv, carriers := newSecretRefServer(t, secretRefMasterKey)
	for _, carrier := range carriers {
		for field, repoint := range carrier.repoints {
			t.Run(carrier.name+"/"+field, func(t *testing.T) {
				rec := putAdmin(t, srv, carrier.path, secretRefMasterKey, carrier.body(seed))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				destination := carrier.destination(t)

				// The reference is unchanged, but the entity would send what it
				// resolves to somewhere else.
				rec = putAdmin(t, srv, carrier.path, secretRefDashboard, repoint(seed))
				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
				assert.Contains(t, rec.Body.String(), "secret_reference_requires_master_key")
				assert.Contains(t, rec.Body.String(), field)
				assert.NotContains(t, rec.Body.String(), "SECRETREF_SEED", "the error never repeats the value")
				assert.Equal(t, destination, carrier.destination(t), "a refused save leaves the stored entity unchanged")
				assert.Equal(t, seed, carrier.stored(t))

				rec = putAdmin(t, srv, carrier.path, secretRefDashboard, carrier.edit(seed))
				require.Equal(t, http.StatusOK, rec.Code, "a dashboard key edits other fields: %s", rec.Body.String())
				assert.Equal(t, seed, carrier.stored(t))

				rec = putAdmin(t, srv, carrier.path, secretRefMasterKey, repoint(seed))
				require.Equal(t, http.StatusOK, rec.Code, "the master key repoints: %s", rec.Body.String())
				assert.NotEqual(t, destination, carrier.destination(t))

				// Back to the start, then without a reference a dashboard key
				// repoints as before, a literal secret included.
				rec = putAdmin(t, srv, carrier.path, secretRefMasterKey, carrier.body(seed))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				rec = putAdmin(t, srv, carrier.path, secretRefDashboard, carrier.body("sk-literal"))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				rec = putAdmin(t, srv, carrier.path, secretRefDashboard, repoint("sk-literal"))
				require.Equal(t, http.StatusOK, rec.Code, "a dashboard key repoints an entity without references: %s", rec.Body.String())
				assert.NotEqual(t, destination, carrier.destination(t))
			})
		}
	}
}

func TestAdminSecretReferences_DashboardKeyRepointsWithoutMasterKey(t *testing.T) {
	t.Setenv("SECRETREF_SEED", "seed-value")
	const seed = "${env:SECRETREF_SEED}"

	srv, carriers := newSecretRefServer(t, "")
	for _, carrier := range carriers {
		for field, repoint := range carrier.repoints {
			t.Run(carrier.name+"/"+field, func(t *testing.T) {
				rec := putAdmin(t, srv, carrier.path, secretRefDashboard, carrier.body(seed))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				destination := carrier.destination(t)

				rec = putAdmin(t, srv, carrier.path, secretRefDashboard, repoint(seed))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.NotEqual(t, destination, carrier.destination(t))
			})
		}
	}
}

func TestAdminSecretReferences_MaskKeepsStoredReference(t *testing.T) {
	t.Setenv("SECRETREF_SEED", "seed-value")
	// A value mixing a literal with a reference is masked in the view.
	const mixed = "sk-${env:SECRETREF_SEED}"

	srv, carriers := newSecretRefServer(t, secretRefMasterKey)
	for _, carrier := range carriers {
		t.Run(carrier.name, func(t *testing.T) {
			rec := putAdmin(t, srv, carrier.path, secretRefMasterKey, carrier.body(mixed))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			rec = putAdmin(t, srv, carrier.path, secretRefDashboard, carrier.body(carrier.mask))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, mixed, carrier.stored(t))
		})
	}
}

func TestAdminSecretReferences_DashboardKeyWithoutMasterKey(t *testing.T) {
	// Without a master key there is none to protect: a dashboard key is the
	// gateway's top credential and saves references as before.
	t.Setenv("SECRETREF_SEED", "seed-value")

	srv, carriers := newSecretRefServer(t, "")
	for _, carrier := range carriers {
		t.Run(carrier.name, func(t *testing.T) {
			rec := putAdmin(t, srv, carrier.path, secretRefDashboard, carrier.body("${env:SECRETREF_SEED}"))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "${env:SECRETREF_SEED}", carrier.stored(t))
		})
	}
}

func TestAuthMiddleware_RestrictsSecretReferencesBelowTheMasterKey(t *testing.T) {
	keys := mockAuthenticator{enabled: true, tokenToID: map[string]string{secretRefDashboard: "key-dashboard"}, tokenDashboard: map[string]bool{secretRefDashboard: true}}
	identity := &mockRequestAuthenticator{result: &ext.Authentication{PrincipalID: "principal-1", DashboardAccess: true, Method: "oidc"}}
	tests := []struct {
		name           string
		masterKey      string
		bearer         string
		wantRestricted bool
	}{
		{name: "master key", masterKey: secretRefMasterKey, bearer: secretRefMasterKey},
		{name: "managed key", masterKey: secretRefMasterKey, bearer: secretRefDashboard, wantRestricted: true},
		{name: "extension identity", masterKey: secretRefMasterKey, wantRestricted: true},
		{name: "managed key without a master key", bearer: secretRefDashboard},
		{name: "extension identity without a master key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var storeErr error
			next := func(c *echo.Context) error {
				_, storeErr = config.NewSecrets().StoreSecret(c.Request().Context(), config.SecretKey{Entity: "mcp_servers", ID: "docs", Field: "headers.X"}, "${env:SECRETREF_OTHER}", nil)
				return c.NoContent(http.StatusNoContent)
			}
			handler := AuthMiddlewareWithRequestAuthenticators(tt.masterKey, keys, []ext.RequestAuthenticator{identity}, nil)(next)
			var opts []echotest.Option
			if tt.bearer != "" {
				opts = append(opts, echotest.WithHeader("Authorization", "Bearer "+tt.bearer))
			}
			c, rec := echotest.Get(t, "/admin/mcp-servers", opts...)
			require.NoError(t, handler(c))
			require.Equal(t, http.StatusNoContent, rec.Code)
			if tt.wantRestricted {
				assert.ErrorIs(t, storeErr, config.ErrSecretReferenceRestricted)
			} else {
				assert.NoError(t, storeErr)
			}
		})
	}
}
