package httpclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestParseProxyURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "empty means no proxy", raw: "   ", want: ""},
		{name: "http proxy", raw: "http://proxy.internal:3128", want: "http://proxy.internal:3128"},
		{name: "https proxy with credentials", raw: "https://user:pass@proxy.internal:443", want: "https://user:pass@proxy.internal:443"},
		{name: "socks5 proxy", raw: "socks5://127.0.0.1:1080", want: "socks5://127.0.0.1:1080"},
		{name: "socks5h proxy, scheme normalized", raw: "SOCKS5H://proxy:1080", want: "socks5h://proxy:1080"},
		{name: "unsupported scheme", raw: "ftp://proxy:21", wantErr: true},
		{name: "missing scheme", raw: "proxy.internal:3128", wantErr: true},
		{name: "missing host", raw: "http://", wantErr: true},
		{name: "unparseable", raw: "http://[::1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseProxyURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseProxyURL(%q) = %v, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseProxyURL(%q) error = %v", tt.raw, err)
			}
			if tt.want == "" {
				if got != nil {
					t.Fatalf("ParseProxyURL(%q) = %v, want nil", tt.raw, got)
				}
				return
			}
			if got.String() != tt.want {
				t.Fatalf("ParseProxyURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestRedactProxyURL(t *testing.T) {
	tests := map[string]string{
		"":                                   "",
		"http://proxy:3128":                  "http://proxy:3128",
		"socks5://user:secret@proxy:1080":    "socks5://user:xxxxx@proxy:1080",
		"http://user@proxy:3128":             "http://user@proxy:3128",
		"not a url at all but keeps secrets": "not a url at all but keeps secrets",
	}
	for raw, want := range tests {
		if got := RedactProxyURL(raw); got != want {
			t.Errorf("RedactProxyURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestProxyFromContext(t *testing.T) {
	if got := ProxyFromContext(context.Background()); got != nil {
		t.Fatalf("ProxyFromContext(background) = %v, want nil", got)
	}
	ctx := ContextWithProxy(context.Background(), nil)
	if got := ProxyFromContext(ctx); got != nil {
		t.Fatalf("ContextWithProxy(nil) attached %v, want nothing", got)
	}
	proxy := &url.URL{Scheme: "http", Host: "proxy:3128"}
	ctx = ContextWithProxy(context.Background(), proxy)
	if got := ProxyFromContext(ctx); got != proxy {
		t.Fatalf("ProxyFromContext() = %v, want %v", got, proxy)
	}
}

func TestProxyForRequest_ContextWinsOverEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://env-proxy:3128")
	t.Setenv("HTTPS_PROXY", "http://env-proxy:3128")
	t.Setenv("NO_PROXY", "")

	req := httptest.NewRequest(http.MethodGet, "http://api.example.com/v1/models", nil)
	got, err := proxyForRequest(req)
	if err != nil {
		t.Fatalf("proxyForRequest() error = %v", err)
	}
	if got == nil || got.Host != "env-proxy:3128" {
		t.Fatalf("proxyForRequest() without context proxy = %v, want the environment proxy", got)
	}

	ctxProxy := &url.URL{Scheme: "socks5", Host: "ctx-proxy:1080"}
	req = req.WithContext(ContextWithProxy(req.Context(), ctxProxy))
	got, err = proxyForRequest(req)
	if err != nil {
		t.Fatalf("proxyForRequest() error = %v", err)
	}
	if got != ctxProxy {
		t.Fatalf("proxyForRequest() with context proxy = %v, want %v", got, ctxProxy)
	}
}

// A request whose context carries a proxy must actually be sent to that
// proxy: the fake proxy below sees the absolute upstream URL an HTTP forward
// proxy receives, while the upstream itself is never contacted.
func TestNewHTTPClient_RoutesContextProxyRequests(t *testing.T) {
	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	var proxied []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = append(proxied, r.RequestURI)
		if got := r.Header.Get("Proxy-Authorization"); got == "" {
			t.Errorf("proxy received no Proxy-Authorization header")
		}
		_, _ = io.WriteString(w, "via proxy")
	}))
	defer proxy.Close()

	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	proxyURL.User = url.UserPassword("user", "secret")

	client := NewDefaultHTTPClient()
	target := upstream.URL + "/v1/models"

	ctx := ContextWithProxy(context.Background(), proxyURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do() via proxy error = %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "via proxy" {
		t.Fatalf("body = %q, want the proxy's response", body)
	}
	if len(proxied) != 1 || proxied[0] != target {
		t.Fatalf("proxy saw %v, want [%s]", proxied, target)
	}
	if upstreamHits != 0 {
		t.Fatalf("upstream hit %d times, want 0 (request should go to the proxy)", upstreamHits)
	}

	// The same client, without a proxy in the context, goes direct.
	req, err = http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("Do() direct error = %v", err)
	}
	_ = resp.Body.Close()
	if upstreamHits != 1 {
		t.Fatalf("upstream hit %d times, want 1 for the direct request", upstreamHits)
	}
	if len(proxied) != 1 {
		t.Fatalf("proxy saw %d requests, want the direct request to bypass it", len(proxied))
	}
}
