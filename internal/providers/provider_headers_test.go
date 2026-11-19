package providers

import (
	"context"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

func TestProviderFactoryValidateHeaders_UsesTheRulesOfTheType(t *testing.T) {
	factory := NewProviderFactory()
	factory.Add(Registration{
		Type:      "signed",
		New:       func(ProviderConfig, ProviderOptions) core.Provider { return &initTestProvider{} },
		Discovery: DiscoveryConfig{HeadersUnsupported: true},
	})
	factory.Add(Registration{
		Type: "plain",
		New:  func(ProviderConfig, ProviderOptions) core.Provider { return &initTestProvider{} },
	})

	headers := map[string]string{"X-Edge-Auth": "tok"}
	require.NoError(t, factory.ValidateHeaders("plain", headers))
	require.EqualError(t, factory.ValidateHeaders("signed", headers), `headers: provider type "signed" does not support headers`)
}

func TestValidateHeaders(t *testing.T) {
	const secret = "tok-do-not-leak"
	many := map[string]string{}
	for i := range maxProviderHeaders + 1 {
		many["X-H-"+string(rune('a'+i%26))+strings.Repeat("x", i/26)] = "v"
	}
	typed := DiscoveryConfig{ReservedHeaders: []string{"anthropic-version", "Anthropic-Beta"}}

	tests := []struct {
		name    string
		spec    DiscoveryConfig
		headers map[string]string
		wantErr string
	}{
		{name: "none"},
		{name: "proxy token and attribution", headers: map[string]string{"X-Edge-Auth": secret, "OpenAI-Organization": "org-1", "x_tenant": "acme"}},
		{name: "edge gateway credentials are what the setting is for", headers: map[string]string{"cf-aig-authorization": "Bearer " + secret, "Ocp-Apim-Subscription-Key": secret}},
		{name: "a name another type reserves", headers: map[string]string{"anthropic-beta": "x"}},
		{
			name:    "authorization",
			headers: map[string]string{"Authorization": "Bearer " + secret},
			wantErr: "headers.Authorization: header carries the provider credential; set api_key or api_keys instead",
		},
		{
			name:    "credential header spelled with underscores",
			headers: map[string]string{"X_Api_Key": secret},
			wantErr: "headers.X_Api_Key: header carries the provider credential; set api_key or api_keys instead",
		},
		{
			name:    "elevenlabs credential",
			headers: map[string]string{"xi-api-key": secret},
			wantErr: "headers.xi-api-key: header carries the provider credential; set api_key or api_keys instead",
		},
		{
			name:    "transport",
			headers: map[string]string{"host": "evil.example.com"},
			wantErr: "headers.host: header is set by GoModel and cannot be configured",
		},
		{
			name:    "body",
			headers: map[string]string{"Content-Type": "text/plain"},
			wantErr: "headers.Content-Type: header is set by GoModel and cannot be configured",
		},
		{
			name:    "response encoding",
			headers: map[string]string{"Accept-Encoding": "gzip"},
			wantErr: "headers.Accept-Encoding: header is set by GoModel and cannot be configured",
		},
		{
			name:    "idempotency key",
			headers: map[string]string{"Idempotency-Key": "k"},
			wantErr: "headers.Idempotency-Key: header is set by GoModel and cannot be configured",
		},
		{
			name:    "proxy hop",
			headers: map[string]string{"Proxy-Authorization": "Basic " + secret},
			wantErr: "headers.Proxy-Authorization: header carries the provider credential; set api_key or api_keys instead",
		},
		{
			name:    "reserved for the type",
			spec:    typed,
			headers: map[string]string{"Anthropic-Version": "2020-01-01"},
			wantErr: "headers.Anthropic-Version: header is set by GoModel for anthropic providers and cannot be configured",
		},
		{
			name:    "reserved for the type, underscores",
			spec:    typed,
			headers: map[string]string{"anthropic_beta": "x"},
			wantErr: "headers.anthropic_beta: header is set by GoModel for anthropic providers and cannot be configured",
		},
		{
			name:    "type without header support",
			spec:    DiscoveryConfig{HeadersUnsupported: true},
			headers: map[string]string{"X-Edge-Auth": secret},
			wantErr: `headers: provider type "anthropic" does not support headers`,
		},
		{
			name:    "invalid name",
			headers: map[string]string{"X Edge": secret},
			wantErr: `headers: "X Edge" is not a valid header name`,
		},
		{
			name:    "empty name",
			headers: map[string]string{"": secret},
			wantErr: `headers: "" is not a valid header name`,
		},
		{
			name:    "duplicate ignoring case",
			headers: map[string]string{"X-Tenant": "a", "x-tenant": "b"},
			wantErr: "headers.x-tenant: header is set twice, also as X-Tenant",
		},
		{
			name:    "duplicate spelled with an underscore",
			headers: map[string]string{"X-Tenant": "a", "X_Tenant": "b"},
			wantErr: "headers.X_Tenant: header is set twice, also as X-Tenant",
		},
		{
			name:    "empty value",
			headers: map[string]string{"X-Tenant": " "},
			wantErr: "headers.X-Tenant: value is empty",
		},
		{
			name:    "line break",
			headers: map[string]string{"X-Edge-Auth": secret + "\r\nX-Injected: 1"},
			wantErr: "headers.X-Edge-Auth: value holds a character HTTP does not allow in a header, such as a line break",
		},
		{
			name:    "NUL",
			headers: map[string]string{"X-Edge-Auth": secret + "\x00"},
			wantErr: "headers.X-Edge-Auth: value holds a character HTTP does not allow in a header, such as a line break",
		},
		{
			name:    "too many",
			headers: many,
			wantErr: "headers: 33 headers set, at most 32 are allowed",
		},
		{
			name:    "too large",
			headers: map[string]string{"X-Big": strings.Repeat("a", maxProviderHeaderBytes)},
			wantErr: "headers: names and values take 8197 bytes, at most 8192 are allowed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateHeaders("anthropic", tt.spec, tt.headers)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.wantErr)
			assert.NotContains(t, err.Error(), secret, "an error names the header, never its value")
		})
	}
}

func TestParseHeadersEnv(t *testing.T) {
	got, err := parseHeadersEnv(`{"X-Edge-Auth":"${file:/run/secrets/edge}","X-Tenant":"acme"}`)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"X-Edge-Auth": "${file:/run/secrets/edge}", "X-Tenant": "acme"}, got)

	empty, err := parseHeadersEnv(`{}`)
	require.NoError(t, err)
	assert.NotNil(t, empty, "an empty object still replaces the YAML headers")
	assert.Empty(t, empty)

	for _, value := range []string{`X-Edge-Auth: tok-do-not-leak`, `{"X-Edge-Auth":"tok-do-not-leak"`, `{"X-Retries":3}`, `["tok-do-not-leak"]`, `null`} {
		_, err := parseHeadersEnv(value)
		require.EqualError(t, err, "value is not a JSON object of header names to string values", value)
	}
}

func TestResolveProviders_Headers(t *testing.T) {
	global := config.ResilienceConfig{Retry: config.DefaultRetryConfig(), CircuitBreaker: config.DefaultCircuitBreakerConfig()}
	yamlHeaders := map[string]string{"X-Edge-Auth": "${vault:edge}", "X-Tenant": "acme"}
	tests := []struct {
		name        string
		env         map[string]string
		raw         map[string]config.RawProviderConfig
		want        map[string]map[string]string
		wantLookups []string
		wantErr     string
	}{
		{
			name:        "yaml, with a reference resolved in the value",
			raw:         map[string]config.RawProviderConfig{"gemini": {Type: "gemini", APIKey: "k", Headers: yamlHeaders}},
			want:        map[string]map[string]string{"gemini": {"X-Edge-Auth": "edge-token", "X-Tenant": "acme"}},
			wantLookups: []string{"edge"},
		},
		{
			name:        "env only, references resolved inside the JSON values",
			env:         map[string]string{"GEMINI_API_KEY": "k", "GEMINI_HEADERS": `{"X-Edge-Auth":"Bearer ${vault:edge}"}`},
			want:        map[string]map[string]string{"gemini": {"X-Edge-Auth": "Bearer edge-token"}},
			wantLookups: []string{"edge"},
		},
		{
			name:        "env replaces the yaml map instead of merging, and the replaced reference is never looked up",
			env:         map[string]string{"GEMINI_HEADERS": `{"X-Region":"eu"}`},
			raw:         map[string]config.RawProviderConfig{"gemini": {Type: "gemini", APIKey: "k", Headers: yamlHeaders}},
			want:        map[string]map[string]string{"gemini": {"X-Region": "eu"}},
			wantLookups: []string{},
		},
		{
			name: "an empty env object clears the yaml headers",
			env:  map[string]string{"GEMINI_HEADERS": `{}`},
			raw:  map[string]config.RawProviderConfig{"gemini": {Type: "gemini", APIKey: "k", Headers: yamlHeaders}},
			want: map[string]map[string]string{"gemini": {}},
		},
		{
			name: "a suffixed variable sets the headers of that instance only",
			env:  map[string]string{"OPENAI_API_KEY": "k", "OPENAI_EU_API_KEY": "k-eu", "OPENAI_EU_HEADERS": `{"X-Region":"eu"}`},
			want: map[string]map[string]string{"openai": nil, "openai-eu": {"X-Region": "eu"}},
		},
		{
			name: "bare variables do not replace the headers of a differently named provider",
			env:  map[string]string{"OPENAI_HEADERS": `{"X-Region":"eu"}`},
			raw:  map[string]config.RawProviderConfig{"alpha": {Type: "openai", APIKey: "k", BaseURL: "https://alpha.example.com", Headers: map[string]string{"X-Tenant": "acme"}}},
			want: map[string]map[string]string{"alpha": {"X-Tenant": "acme"}},
		},
		{
			name:        "headers of a provider without credentials are never looked up",
			env:         map[string]string{"ANTHROPIC_API_KEY": "k"},
			raw:         map[string]config.RawProviderConfig{"gemini": {Type: "gemini", Headers: map[string]string{"X-Edge-Auth": "${vault:missing}"}}},
			want:        map[string]map[string]string{"anthropic": nil},
			wantLookups: []string{},
		},
		{
			name:    "an unresolvable reference names the field, not the value",
			raw:     map[string]config.RawProviderConfig{"gemini": {Type: "gemini", APIKey: "k", Headers: map[string]string{"X-Edge-Auth": "Bearer ${vault:missing}"}}},
			wantErr: "providers.gemini.headers.X-Edge-Auth: secret reference ${vault:...}: not found",
		},
		{
			name:    "an unresolvable reference in an env value names the field",
			env:     map[string]string{"GEMINI_API_KEY": "k", "GEMINI_HEADERS": `{"X-Edge-Auth":"${vault:missing}"}`},
			wantErr: "providers.gemini.headers.X-Edge-Auth: secret reference ${vault:...}: not found",
		},
		{
			name:    "an unset legacy placeholder is not sent as the value",
			raw:     map[string]config.RawProviderConfig{"gemini": {Type: "gemini", APIKey: "k", Headers: map[string]string{"X-Edge-Auth": "${GOMODEL_TEST_UNSET}"}}},
			wantErr: "providers.gemini.headers.X-Edge-Auth: value holds a ${...} placeholder that was not expanded; set the variable it names, or write ${env:NAME}",
		},
		{
			name:    "a legacy placeholder in an env value is never expanded, even when the variable is set",
			env:     map[string]string{"GEMINI_API_KEY": "k", "GOMODEL_TEST_EDGE": "edge-token", "GEMINI_HEADERS": `{"X-Edge-Auth":"${GOMODEL_TEST_EDGE}"}`},
			wantErr: "providers.gemini.headers.X-Edge-Auth: value holds a ${...} placeholder that was not expanded; set the variable it names, or write ${env:NAME}",
		},
		{
			name:    "a malformed env value fails without repeating it",
			env:     map[string]string{"GEMINI_API_KEY": "k", "GEMINI_HEADERS": `X-Edge-Auth: tok-do-not-leak`},
			wantErr: "GEMINI_HEADERS: value is not a JSON object of header names to string values",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			secrets, lookups := countingVault(t, map[string]string{"edge": "edge-token"})
			raw := tt.raw
			if raw == nil {
				raw = map[string]config.RawProviderConfig{}
			}
			loaded := make(map[string]map[string]string, len(raw))
			for name, p := range raw {
				loaded[name] = maps.Clone(p.Headers)
			}

			got, filtered, err := resolveProviders(t.Context(), secrets, raw, global, testDiscoveryConfigs)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			gotHeaders := make(map[string]map[string]string, len(got))
			for name, cfg := range got {
				gotHeaders[name] = cfg.Headers
				assert.Equal(t, cfg.Headers, filtered[name].Headers, "auxiliary clients read the same resolved headers")
			}
			assert.Equal(t, tt.want, gotHeaders)
			if tt.wantLookups != nil {
				assert.ElementsMatch(t, tt.wantLookups, *lookups)
			}
			for name, before := range loaded {
				assert.Equal(t, before, raw[name].Headers, "the loaded configuration keeps its references")
			}
		})
	}
}

func TestInit_StopsOnInvalidProviderHeaders(t *testing.T) {
	tests := []struct {
		name    string
		spec    DiscoveryConfig
		headers map[string]string
		wantErr string
	}{
		{
			name:    "credential header",
			headers: map[string]string{"Authorization": "Bearer tok-do-not-leak"},
			wantErr: "providers.edge.headers.Authorization: header carries the provider credential; set api_key or api_keys instead",
		},
		{
			name:    "reserved for the type",
			spec:    DiscoveryConfig{ReservedHeaders: []string{"X-Test-Version"}},
			headers: map[string]string{"x-test-version": "1"},
			wantErr: "providers.edge.headers.x-test-version: header is set by GoModel for test providers and cannot be configured",
		},
		{
			name:    "type without header support",
			spec:    DiscoveryConfig{HeadersUnsupported: true},
			headers: map[string]string{"X-Edge-Auth": "tok-do-not-leak"},
			wantErr: `providers.edge.headers: provider type "test" does not support headers`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			built := false
			factory := NewProviderFactory()
			factory.Add(Registration{
				Type: "test",
				New: func(ProviderConfig, ProviderOptions) core.Provider {
					built = true
					return &initTestProvider{}
				},
				Discovery: tt.spec,
			})

			_, err := Init(t.Context(), &config.LoadResult{
				Config: &config.Config{Cache: config.CacheConfig{Model: config.ModelCacheConfig{
					RefreshInterval: 1,
					Local:           &config.LocalCacheConfig{CacheDir: t.TempDir()},
				}}},
				RawProviders: map[string]config.RawProviderConfig{"edge": {Type: "test", APIKey: "k", Headers: tt.headers}},
				Secrets:      config.NewSecrets(),
			}, factory)
			require.EqualError(t, err, tt.wantErr)
			assert.NotContains(t, err.Error(), "tok-do-not-leak")
			assert.False(t, built, "a provider must not be built with headers that were refused")
		})
	}
}

func TestFactoryCreate_PassesHeadersToTheProvider(t *testing.T) {
	var got ProviderOptions
	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "test",
		New: func(_ ProviderConfig, opts ProviderOptions) core.Provider {
			got = opts
			return &initTestProvider{}
		},
	})

	headers := map[string]string{"X-Edge-Auth": "tok"}
	_, err := factory.Create(ProviderConfig{Name: "edge", Type: "test", Headers: headers, Resilience: config.ResilienceConfig{Retry: config.DefaultRetryConfig(), CircuitBreaker: config.DefaultCircuitBreakerConfig()}})
	require.NoError(t, err)
	assert.Equal(t, headers, got.Headers)
}

func TestRegistryProviderHeaders_BelongToTheInstance(t *testing.T) {
	registry := NewModelRegistry()
	first, second := &initTestProvider{}, &initTestProvider{}
	headers := map[string]string{"X-Edge-Auth": "tok"}

	registry.RegisterProviderWithNameAndType(first, "edge", "test")
	registry.SetProviderHeaders(first, headers)
	assert.Equal(t, headers, registry.ProviderHeaders(first))
	assert.Nil(t, registry.ProviderHeaders(second))

	t.Run("a replacement under the same name does not inherit them", func(t *testing.T) {
		registry.replaceProvider(second, "edge", "test", providerSettings{})
		assert.Nil(t, registry.ProviderHeaders(second))
		assert.Nil(t, registry.ProviderHeaders(first), "the replaced instance is forgotten")
	})

	t.Run("unregistering drops them", func(t *testing.T) {
		registry.SetProviderHeaders(second, headers)
		registry.UnregisterProvider("edge")
		assert.Nil(t, registry.ProviderHeaders(second))
	})

	t.Run("an empty set clears them", func(t *testing.T) {
		registry.RegisterProviderWithNameAndType(first, "edge", "test")
		registry.SetProviderHeaders(first, headers)
		registry.SetProviderHeaders(first, nil)
		assert.Nil(t, registry.ProviderHeaders(first))
	})
}

func TestRouterRealtimeTargetsCarryProviderHeaders(t *testing.T) {
	edge, plain := &realtimeMockProvider{}, &realtimeMockProvider{}
	registry := newTestRegistryWithModels(
		registryModelEntry{provider: edge, providerName: "edge", providerType: "openai", modelID: "gpt-realtime"},
		registryModelEntry{provider: plain, providerName: "plain", providerType: "xai", modelID: "grok-realtime"},
	)
	registry.SetProviderHeaders(edge, map[string]string{"X-Edge-Auth": "tok", "authorization": "replaced"})
	router, err := NewRouter(registry)
	require.NoError(t, err)
	ctx := context.Background()

	t.Run("websocket dial", func(t *testing.T) {
		target, err := router.RealtimeTarget(ctx, &core.RealtimeRequest{Model: "gpt-realtime", Provider: "edge"})
		require.NoError(t, err)
		assert.Equal(t, "tok", target.Headers.Get("X-Edge-Auth"))
		assert.Equal(t, []string{"replaced"}, target.Headers.Values("Authorization"), "configured headers go last and replace a value of the same name")
	})

	for name, resolve := range map[string]func(context.Context, *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error){
		"call signaling": router.RealtimeCallTarget,
		"client secret":  router.RealtimeClientSecretTarget,
	} {
		t.Run(name, func(t *testing.T) {
			target, err := resolve(ctx, &core.RealtimeRequest{Model: "gpt-realtime", Provider: "edge"})
			require.NoError(t, err)
			assert.Equal(t, "tok", target.Headers.Get("X-Edge-Auth"))

			other, err := resolve(ctx, &core.RealtimeRequest{Model: "grok-realtime", Provider: "plain"})
			require.NoError(t, err)
			assert.Empty(t, other.Headers.Get("X-Edge-Auth"), "another provider never carries them")
		})
	}

	t.Run("another provider's dial carries none", func(t *testing.T) {
		target, err := router.RealtimeTarget(ctx, &core.RealtimeRequest{Model: "grok-realtime", Provider: "plain"})
		require.NoError(t, err)
		assert.Equal(t, http.Header{"Authorization": {"Bearer test"}}, target.Headers)
	})
}
