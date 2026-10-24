package providers

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
)

// captureOptionsFactory registers a provider type whose constructor records
// the ProviderOptions it received, so a test can inspect the transport the
// factory handed down.
func captureOptionsFactory(t *testing.T) (*ProviderFactory, *ProviderOptions) {
	t.Helper()
	captured := &ProviderOptions{}
	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "capture",
		New: func(_ ProviderConfig, opts ProviderOptions) core.Provider {
			*captured = opts
			return &factoryMockProvider{}
		},
	})
	return factory, captured
}

func transportProxy(t *testing.T, client *http.Client, target string) *url.URL {
	t.Helper()
	require.NotNil(t, client)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	req, err := http.NewRequest(http.MethodGet, target, nil)
	require.NoError(t, err)
	proxy, err := transport.Proxy(req)
	require.NoError(t, err)
	return proxy
}

type fakeProxySelector struct {
	proxy *url.URL
	err   error
	calls atomic.Int32
	last  atomic.Pointer[ext.ProxyRequest]
}

func (s *fakeProxySelector) Name() string { return "fake" }

func (s *fakeProxySelector) SelectProxy(req ext.ProxyRequest) (*url.URL, error) {
	s.calls.Add(1)
	s.last.Store(&req)
	return s.proxy, s.err
}

func TestProviderFactory_NoProxyLeavesTransportToTheProvider(t *testing.T) {
	factory, captured := captureOptionsFactory(t)
	_, err := factory.Create(ProviderConfig{Name: "plain", Type: "capture", APIKey: "k"})
	require.NoError(t, err)
	assert.Nil(t, captured.HTTPClient)
}

func TestProviderFactory_ProxyURLBuildsAProxiedTransport(t *testing.T) {
	factory, captured := captureOptionsFactory(t)
	_, err := factory.Create(ProviderConfig{
		Name:     "openai-eu",
		Type:     "capture",
		APIKey:   "k",
		ProxyURL: "socks5://user:pass@10.0.0.1:1080",
	})
	require.NoError(t, err)

	proxy := transportProxy(t, captured.HTTPClient, "https://api.openai.com/v1/chat/completions")
	require.NotNil(t, proxy)
	assert.Equal(t, "socks5://user:pass@10.0.0.1:1080", proxy.String())
}

func TestProviderFactory_ProxyURLWinsOverTheSelector(t *testing.T) {
	factory, captured := captureOptionsFactory(t)
	selector := &fakeProxySelector{proxy: &url.URL{Scheme: "http", Host: "selected:3128"}}
	factory.SetProxySelector(selector)

	_, err := factory.Create(ProviderConfig{Name: "p", Type: "capture", APIKey: "k", ProxyURL: "http://static:3128"})
	require.NoError(t, err)

	proxy := transportProxy(t, captured.HTTPClient, "https://api.example.com/v1/models")
	require.NotNil(t, proxy)
	assert.Equal(t, "http://static:3128", proxy.String())
	assert.Zero(t, selector.calls.Load())
}

func TestProviderFactory_InvalidProxyURLIsRejected(t *testing.T) {
	factory, _ := captureOptionsFactory(t)
	_, err := factory.Create(ProviderConfig{Name: "p", Type: "capture", APIKey: "k", ProxyURL: "ftp://user:pass@proxy:21"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid proxy_url")
	assert.NotContains(t, err.Error(), "pass")
}

func TestProviderFactory_SelectorIsConsultedPerRequest(t *testing.T) {
	factory, captured := captureOptionsFactory(t)
	selector := &fakeProxySelector{proxy: &url.URL{Scheme: "http", Host: "selected:3128"}}
	factory.SetProxySelector(selector)

	_, err := factory.Create(ProviderConfig{Name: " openai-eu ", Type: "capture", APIKey: "k"})
	require.NoError(t, err)

	proxy := transportProxy(t, captured.HTTPClient, "https://api.openai.com/v1/models")
	require.NotNil(t, proxy)
	assert.Equal(t, "http://selected:3128", proxy.String())

	last := selector.last.Load()
	require.NotNil(t, last)
	assert.Equal(t, "openai-eu", last.Provider)
	assert.Equal(t, "capture", last.ProviderType)
	assert.Equal(t, "api.openai.com", last.URL.Host)
}

// http.ProxyFromEnvironment reads the environment once per process, so the
// fallback is exercised through the seam rather than by setting HTTPS_PROXY.
func TestSelectedProxy_WithoutOpinionUsesTheFallback(t *testing.T) {
	selector := &fakeProxySelector{}
	fallback := func(*http.Request) (*url.URL, error) { return &url.URL{Scheme: "http", Host: "env-proxy:3128"}, nil }
	proxyFunc := selectedProxy(selector, "openai-eu", "openai", fallback)

	req, err := http.NewRequest(http.MethodGet, "https://api.example.com/v1/models", nil)
	require.NoError(t, err)
	proxy, err := proxyFunc(req)
	require.NoError(t, err)
	require.NotNil(t, proxy)
	assert.Equal(t, "http://env-proxy:3128", proxy.String())
	assert.Equal(t, int32(1), selector.calls.Load())
}

func TestProviderFactory_SelectorInstalledBuildsAProxiedTransport(t *testing.T) {
	factory, captured := captureOptionsFactory(t)
	factory.SetProxySelector(&fakeProxySelector{})

	_, err := factory.Create(ProviderConfig{Name: "p", Type: "capture", APIKey: "k"})
	require.NoError(t, err)
	require.NotNil(t, captured.HTTPClient)

	factory.SetProxySelector(nil)
	_, err = factory.Create(ProviderConfig{Name: "p", Type: "capture", APIKey: "k"})
	require.NoError(t, err)
	assert.Nil(t, captured.HTTPClient)
}

func TestProviderFactory_SelectorErrorFailsTheRequest(t *testing.T) {
	factory, captured := captureOptionsFactory(t)
	factory.SetProxySelector(&fakeProxySelector{err: errors.New("no healthy egress")})

	_, err := factory.Create(ProviderConfig{Name: "p", Type: "capture", APIKey: "k"})
	require.NoError(t, err)

	transport, ok := captured.HTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	req, err := http.NewRequest(http.MethodGet, "https://api.example.com/v1/models", nil)
	require.NoError(t, err)
	_, err = transport.Proxy(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `proxy selector "fake": no healthy egress`)
}

// A real forward proxy: the provider's request must arrive at the proxy with
// the upstream's absolute URL and never reach the upstream directly.
func TestProviderFactory_RequestsTravelThroughTheProxy(t *testing.T) {
	var upstreamHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	var seen atomic.Pointer[string]
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.String()
		seen.Store(&target)
		_, _ = io.WriteString(w, "via-proxy")
	}))
	defer proxy.Close()

	factory, captured := captureOptionsFactory(t)
	_, err := factory.Create(ProviderConfig{Name: "p", Type: "capture", APIKey: "k", ProxyURL: proxy.URL})
	require.NoError(t, err)

	resp, err := captured.HTTPClient.Get(upstream.URL + "/v1/models")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, "via-proxy", string(body))
	assert.Zero(t, upstreamHits.Load())
	require.NotNil(t, seen.Load())
	assert.Equal(t, upstream.URL+"/v1/models", *seen.Load())
}

func TestCredentialSchema_EveryTypeAcceptsAProxyURL(t *testing.T) {
	factory := NewProviderFactory()
	factory.Add(Registration{Type: "keyed", New: func(ProviderConfig, ProviderOptions) core.Provider { return &factoryMockProvider{} }})
	factory.Add(Registration{
		Type:      "vertexish",
		New:       func(ProviderConfig, ProviderOptions) core.Provider { return &factoryMockProvider{} },
		Discovery: DiscoveryConfig{CredentialFields: []CredentialField{{Name: CredentialFieldVertexProject, Required: true}}},
	})
	for _, schema := range factory.CredentialSchemas() {
		field, ok := schema.Field(CredentialFieldProxyURL)
		require.True(t, ok, "type %s should accept proxy_url", schema.Type)
		assert.True(t, field.Advanced)
		assert.False(t, field.Required)
	}
}

func TestValidateCredential_RejectsAMalformedProxyURLByField(t *testing.T) {
	schema := CredentialSchema{Type: "openai", Fields: []CredentialField{{Name: CredentialFieldAPIKeys, Required: true}, {Name: CredentialFieldProxyURL}}}
	err := validateCredential(ManagedProviderCredential{Type: "openai", APIKeys: []string{"sk"}, ProxyURL: "proxy:3128"}, schema)
	var fieldErr *CredentialFieldError
	require.ErrorAs(t, err, &fieldErr)
	assert.Equal(t, CredentialFieldProxyURL, fieldErr.Field)

	require.NoError(t, validateCredential(ManagedProviderCredential{Type: "openai", APIKeys: []string{"sk"}, ProxyURL: "http://proxy:3128"}, schema))
}

func TestSanitizeProviderConfigs_MasksProxyPassword(t *testing.T) {
	got := SanitizeProviderConfigs(map[string]ProviderConfig{
		"p": {Type: "openai", ProxyURL: "socks5://user:s3cret@proxy:1080"},
	})
	require.Len(t, got, 1)
	assert.Equal(t, "socks5://user:xxxxx@proxy:1080", got[0].ProxyURL)
}
