package providers

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/httpclient"
)

// SetProxySelector installs the extension selector consulted for the outbound
// proxy of providers that set no `proxy_url` of their own. It affects
// providers created after the call, so app startup installs it before the
// first provider exists. A nil selector clears it.
func (f *ProviderFactory) SetProxySelector(selector ext.ProxySelector) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proxySelector = selector
}

// providerHTTPClient builds the transport a provider's clients share when a
// proxy applies to it: the provider's own proxy_url, or the extension
// selector consulted per request. It returns nil when neither applies, so
// providers keep constructing their own default client and behaviour is
// unchanged for deployments without proxies.
func providerHTTPClient(cfg ProviderConfig, selector ext.ProxySelector) (*http.Client, error) {
	clientCfg := httpclient.DefaultConfig()
	switch {
	case cfg.ProxyURL != "":
		proxy, err := httpclient.ParseProxyURL(cfg.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy_url: %w", err)
		}
		clientCfg.Proxy = http.ProxyURL(proxy)
	case selector != nil:
		clientCfg.Proxy = selectedProxy(selector, strings.TrimSpace(cfg.Name), cfg.Type, http.ProxyFromEnvironment)
	default:
		return nil, nil
	}
	return httpclient.NewHTTPClient(&clientCfg), nil
}

// selectedProxy adapts the extension selector to Transport.Proxy for one
// provider instance. "No opinion" (nil, nil) defers to fallback -- the
// process environment in production -- so a selector that only knows some
// providers leaves the rest untouched.
func selectedProxy(selector ext.ProxySelector, providerName, providerType string, fallback func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		proxy, err := selector.SelectProxy(ext.ProxyRequest{
			Provider:     providerName,
			ProviderType: providerType,
			URL:          req.URL,
		})
		if err != nil {
			return nil, fmt.Errorf("proxy selector %q: %w", selector.Name(), err)
		}
		if proxy != nil {
			return proxy, nil
		}
		return fallback(req)
	}
}
