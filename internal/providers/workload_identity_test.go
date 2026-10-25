package providers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/workloadidentity"
)

type fakeTokenSource struct {
	token string
	err   error
}

func (f fakeTokenSource) Token(context.Context) (string, error) { return f.token, f.err }

func TestTokenSourceKeyring(t *testing.T) {
	k := NewTokenSourceKeyring(fakeTokenSource{token: "minted"})
	require.NotNil(t, k)
	assert.True(t, k.Federated())
	assert.Equal(t, "minted", k.NextForContext(t.Context()))
	assert.Equal(t, "minted", k.NextForContext(core.WithSessionID(t.Context(), "session-1")))
	assert.Equal(t, "minted", k.Next())
	assert.Equal(t, "minted", k.Primary())
	assert.Equal(t, 1, k.Len())
	assert.False(t, k.Rotates())
	_, stable := k.StableForContext(core.WithSessionID(t.Context(), "session-1"))
	assert.False(t, stable, "a refreshed token must not back persisted credential-scoped resources")
	assert.False(t, k.Replace("sk-static"), "static keys cannot replace a token source")

	failing := NewTokenSourceKeyring(fakeTokenSource{err: errors.New("exchange failed")})
	assert.Empty(t, failing.NextForContext(t.Context()))

	assert.Nil(t, NewTokenSourceKeyring(nil))
	assert.False(t, NewKeyring("sk-static").Federated())
	assert.False(t, (*Keyring)(nil).Federated())
}

// newWorkloadIdentityFactory registers a "wif" type speaking grant and
// records the keyring each build receives.
func newWorkloadIdentityFactory(grant workloadidentity.Grant) (*ProviderFactory, *atomic.Pointer[Keyring]) {
	var keys atomic.Pointer[Keyring]
	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "wif",
		New: func(_ ProviderConfig, opts ProviderOptions) core.Provider {
			keys.Store(opts.Keys)
			return &factoryMockProvider{}
		},
		Discovery: DiscoveryConfig{DefaultBaseURL: "https://api.example.com/v1", WorkloadIdentity: grant},
	})
	return factory, &keys
}

func TestFactoryCreateWithWorkloadIdentity(t *testing.T) {
	var exchanges atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		assert.Equal(t, "/v1/oauth/token", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"minted","expires_in":3600}`)
	}))
	t.Cleanup(server.Close)

	factory, keys := newWorkloadIdentityFactory(workloadidentity.JWTBearer)
	_, err := factory.Create(ProviderConfig{
		Name:    "claude",
		Type:    "wif",
		BaseURL: server.URL + "/v1",
		WorkloadIdentity: config.WorkloadIdentityConfig{
			IdentityToken:    "jwt",
			FederationRuleID: "fdrl_1",
			OrganizationID:   "org",
			ServiceAccountID: "svac_1",
		},
	})
	require.NoError(t, err)

	ring := keys.Load()
	require.True(t, ring.Federated())
	assert.Zero(t, exchanges.Load(), "no exchange before the first request")
	assert.Equal(t, "minted", ring.NextForContext(t.Context()))
	assert.Equal(t, "minted", ring.NextForContext(t.Context()))
	assert.Equal(t, int32(1), exchanges.Load(), "token is cached across requests")
}

func TestFactoryCreateWorkloadIdentityRules(t *testing.T) {
	complete := config.WorkloadIdentityConfig{
		IdentityToken:    "jwt",
		FederationRuleID: "fdrl_1",
		OrganizationID:   "org",
		ServiceAccountID: "svac_1",
	}
	tests := []struct {
		name          string
		grant         workloadidentity.Grant
		cfg           ProviderConfig
		wantErr       string
		wantFederated bool
		wantWarning   bool
	}{
		{
			name:        "api key wins over workload identity",
			grant:       workloadidentity.JWTBearer,
			cfg:         ProviderConfig{Type: "wif", APIKey: "sk-static", APIKeys: []string{"sk-static"}, WorkloadIdentity: complete},
			wantWarning: true,
		},
		{
			name:    "unsupported provider type",
			cfg:     ProviderConfig{Type: "wif", WorkloadIdentity: complete},
			wantErr: `workload_identity is not supported by provider type "wif"`,
		},
		{
			name:    "incomplete configuration",
			grant:   workloadidentity.JWTBearer,
			cfg:     ProviderConfig{Type: "wif", WorkloadIdentity: config.WorkloadIdentityConfig{IdentityToken: "jwt", ServiceAccountID: "svac_1"}},
			wantErr: "workload_identity: federation_rule_id is required",
		},
		{
			name:          "complete configuration",
			grant:         workloadidentity.JWTBearer,
			cfg:           ProviderConfig{Type: "wif", WorkloadIdentity: complete},
			wantFederated: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureSlog(t)
			factory, keys := newWorkloadIdentityFactory(tt.grant)

			_, err := factory.Create(tt.cfg)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantFederated, keys.Load().Federated())
			if tt.wantWarning {
				assert.Contains(t, logs.String(), "workload_identity ignored")
				assert.Equal(t, "sk-static", keys.Load().NextForContext(t.Context()))
			}
			assert.NotContains(t, logs.String(), "jwt\"", "identity token must never be logged")
		})
	}
}

func TestApplyProviderEnvVars_WorkloadIdentity(t *testing.T) {
	t.Setenv("ANTHROPIC_FEDERATION_RULE_ID", "fdrl_1")
	t.Setenv("ANTHROPIC_ORGANIZATION_ID", "org-uuid")
	t.Setenv("ANTHROPIC_SERVICE_ACCOUNT_ID", "svac_1")
	t.Setenv("ANTHROPIC_IDENTITY_TOKEN_FILE", "/var/run/secrets/anthropic.com/token")
	t.Setenv("ANTHROPIC_WORKSPACE_ID", "wrkspc_1")
	t.Setenv("OPENAI_IDENTITY_PROVIDER_ID", "idp_1")
	t.Setenv("OPENAI_SERVICE_ACCOUNT_ID", "sa_1")
	t.Setenv("OPENAI_IDENTITY_TOKEN", "jwt-literal")
	t.Setenv("OPENAI_EU_IDENTITY_TOKEN_FILE", "/var/run/secrets/openai/token")
	t.Setenv("OPENAI_EU_IDENTITY_PROVIDER_ID", "idp_eu")
	t.Setenv("OPENAI_EU_SERVICE_ACCOUNT_ID", "sa_eu")
	t.Setenv("OPENAI_EU_TOKEN_ENDPOINT", "https://auth.example.com/oauth/token")
	// Anthropic-only settings do not apply to OpenAI, so this creates nothing.
	t.Setenv("OPENAI_STAGING_ORGANIZATION_ID", "org-unrelated")

	got, _, err := resolveProviders(t.Context(), config.NewSecrets(), map[string]config.RawProviderConfig{}, config.ResilienceConfig{}, testDiscoveryConfigs)
	require.NoError(t, err)

	require.Contains(t, got, "anthropic")
	assert.Equal(t, "https://api.anthropic.com/v1", got["anthropic"].BaseURL)
	assert.Empty(t, got["anthropic"].APIKeys)
	assert.Equal(t, config.WorkloadIdentityConfig{
		IdentityTokenFile: "/var/run/secrets/anthropic.com/token",
		ServiceAccountID:  "svac_1",
		FederationRuleID:  "fdrl_1",
		OrganizationID:    "org-uuid",
		WorkspaceID:       "wrkspc_1",
	}, got["anthropic"].WorkloadIdentity)

	require.Contains(t, got, "openai")
	assert.Equal(t, "https://api.openai.com/v1", got["openai"].BaseURL)
	assert.Equal(t, config.WorkloadIdentityConfig{
		IdentityToken:      "jwt-literal",
		ServiceAccountID:   "sa_1",
		IdentityProviderID: "idp_1",
	}, got["openai"].WorkloadIdentity)

	require.Contains(t, got, "openai-eu")
	assert.Equal(t, "https://auth.example.com/oauth/token", got["openai-eu"].WorkloadIdentity.TokenEndpoint)
	assert.NotContains(t, got, "openai-staging")
}

func TestApplyProviderEnvVars_WorkloadIdentityNotShadowedByBareKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-stray")

	got, _ := applyEnv(t, map[string]config.RawProviderConfig{
		"claude": {Type: "anthropic", WorkloadIdentity: config.WorkloadIdentityConfig{
			IdentityTokenFile: "/var/run/token",
			FederationRuleID:  "fdrl_1",
			OrganizationID:    "org",
			ServiceAccountID:  "svac_1",
		}},
	})

	require.Contains(t, got, "claude")
	assert.Empty(t, got["claude"].APIKey, "a differently named provider with workload identity keeps it")
}

func TestFilterProviders_KeepsWorkloadIdentityWithoutAPIKey(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"anthropic": {Type: "anthropic", WorkloadIdentity: config.WorkloadIdentityConfig{FederationRuleID: "fdrl_1"}},
		"openai":    {Type: "openai", WorkloadIdentity: config.WorkloadIdentityConfig{IdentityToken: "${UNSET_TOKEN}"}},
	}
	got := filterEmptyProviders(raw, testDiscoveryConfigs)
	assert.Contains(t, got, "anthropic")
	assert.NotContains(t, got, "openai", "an unresolved placeholder is not a credential")
}
