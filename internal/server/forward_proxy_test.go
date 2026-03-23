package server

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gomodel/internal/auditlog"
	"gomodel/internal/usage"
)

func TestForwardProxyMITMAnthropicJSONUsageAndAudit(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q, want /v1/messages", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read upstream request body: %v", err)
		}
		if !strings.Contains(string(body), `"model":"claude-sonnet-4-5"`) {
			t.Fatalf("unexpected request body: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":6}}`))
	}))
	defer upstream.Close()

	caCertPath, caKeyPath, caCert := writeTestCAFiles(t)
	proxyURL, proxyAudit, proxyUsage := startTestForwardProxy(t, upstream, caCertPath, caKeyPath)

	client := newMITMHTTPClient(t, proxyURL, caCert)
	req, err := http.NewRequest(http.MethodPost, upstream.URL+"/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"Reply with ok"}]}`))
	if err != nil {
		t.Fatalf("NewRequest error: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-code-test")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do error: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if !strings.Contains(string(body), `"id":"msg_123"`) {
		t.Fatalf("unexpected response body: %s", body)
	}

	if len(proxyAudit.entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(proxyAudit.entries))
	}
	auditEntry := proxyAudit.entries[0]
	if auditEntry.Provider != "anthropic" {
		t.Fatalf("Provider = %q, want anthropic", auditEntry.Provider)
	}
	if auditEntry.Path != "/v1/messages" {
		t.Fatalf("Path = %q, want /v1/messages", auditEntry.Path)
	}
	if auditEntry.Model != "claude-sonnet-4-5" {
		t.Fatalf("Model = %q, want claude-sonnet-4-5", auditEntry.Model)
	}
	if auditEntry.Data == nil || auditEntry.Data.RequestBody == nil || auditEntry.Data.ResponseBody == nil {
		t.Fatal("expected request and response bodies to be captured")
	}

	if len(proxyUsage.entries) != 1 {
		t.Fatalf("usage entries = %d, want 1", len(proxyUsage.entries))
	}
	usageEntry := proxyUsage.entries[0]
	if usageEntry.Provider != "anthropic" {
		t.Fatalf("Provider = %q, want anthropic", usageEntry.Provider)
	}
	if usageEntry.Endpoint != "/v1/messages" {
		t.Fatalf("Endpoint = %q, want /v1/messages", usageEntry.Endpoint)
	}
	if usageEntry.InputTokens != 10 {
		t.Fatalf("InputTokens = %d, want 10", usageEntry.InputTokens)
	}
	if usageEntry.OutputTokens != 2 {
		t.Fatalf("OutputTokens = %d, want 2", usageEntry.OutputTokens)
	}
	if usageEntry.TotalTokens != 12 {
		t.Fatalf("TotalTokens = %d, want 12", usageEntry.TotalTokens)
	}
	if usageEntry.RawData["cache_read_input_tokens"] != 6 {
		t.Fatalf("RawData[cache_read_input_tokens] = %v, want 6", usageEntry.RawData["cache_read_input_tokens"])
	}
}

func TestForwardProxyMITMAnthropicStreamingUsageAndAudit(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_stream_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

data: [DONE]

`))
	}))
	defer upstream.Close()

	caCertPath, caKeyPath, caCert := writeTestCAFiles(t)
	proxyURL, proxyAudit, proxyUsage := startTestForwardProxy(t, upstream, caCertPath, caKeyPath)

	client := newMITMHTTPClient(t, proxyURL, caCert)
	req, err := http.NewRequest(http.MethodPost, upstream.URL+"/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("NewRequest error: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do error: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if !strings.Contains(string(body), "message_start") {
		t.Fatalf("unexpected streamed body: %s", body)
	}

	if len(proxyAudit.entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(proxyAudit.entries))
	}
	auditEntry := proxyAudit.entries[0]
	if auditEntry.Data == nil {
		t.Fatal("Data = nil")
	}
	responseBody, ok := auditEntry.Data.ResponseBody.(map[string]any)
	if !ok {
		t.Fatalf("ResponseBody = %#v, want map", auditEntry.Data.ResponseBody)
	}
	if responseBody["id"] != "msg_stream_123" {
		t.Fatalf("id = %#v, want msg_stream_123", responseBody["id"])
	}
	if content, ok := responseBody["content"].([]map[string]any); ok {
		if len(content) != 1 || content[0]["text"] != "Hello world" {
			t.Fatalf("content = %#v, want Hello world", content)
		}
	} else {
		contentAny, ok := responseBody["content"].([]any)
		if !ok || len(contentAny) != 1 {
			t.Fatalf("content = %#v, want one text block", responseBody["content"])
		}
		content, ok := contentAny[0].(map[string]any)
		if !ok || content["text"] != "Hello world" {
			t.Fatalf("content[0] = %#v, want Hello world", contentAny[0])
		}
	}

	if len(proxyUsage.entries) != 1 {
		t.Fatalf("usage entries = %d, want 1", len(proxyUsage.entries))
	}
	usageEntry := proxyUsage.entries[0]
	if usageEntry.InputTokens != 10 || usageEntry.OutputTokens != 2 || usageEntry.TotalTokens != 12 {
		t.Fatalf("unexpected usage entry: %+v", usageEntry)
	}
}

func startTestForwardProxy(t *testing.T, upstream *httptest.Server, caCertPath, caKeyPath string) (string, *capturingAuditLogger, *collectingUsageLogger) {
	t.Helper()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("Parse upstream URL error: %v", err)
	}
	mitmHost := canonicalProxyHost(upstreamURL.Host)

	auditLogger := &capturingAuditLogger{
		config: auditlog.Config{
			Enabled:    true,
			LogBodies:  true,
			LogHeaders: true,
		},
	}
	usageLogger := &collectingUsageLogger{
		config: usage.Config{Enabled: true},
	}
	handler, err := NewForwardProxyHandler(http.NotFoundHandler(), &ForwardProxyConfig{
		Enabled:     true,
		MITMHosts:   []string{mitmHost},
		CACertFile:  caCertPath,
		CAKeyFile:   caKeyPath,
		AuditLogger: auditLogger,
		UsageLogger: usageLogger,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	})
	if err != nil {
		t.Fatalf("NewForwardProxyHandler error: %v", err)
	}

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL, auditLogger, usageLogger
}

func newMITMHTTPClient(t *testing.T, proxyAddr string, caCert *x509.Certificate) *http.Client {
	t.Helper()

	proxyURL, err := url.Parse(proxyAddr)
	if err != nil {
		t.Fatalf("Parse proxy URL error: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: pool,
			},
		},
	}
}

func writeTestCAFiles(t *testing.T) (string, string, *x509.Certificate) {
	t.Helper()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey error: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "gomodel-test-ca",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, privateKey.Public(), privateKey)
	if err != nil {
		t.Fatalf("CreateCertificate error: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate error: %v", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey error: %v", err)
	}

	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca-cert.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")
	if err := os.WriteFile(certPath, encodePEMBlock("CERTIFICATE", der), 0o600); err != nil {
		t.Fatalf("WriteFile cert error: %v", err)
	}
	if err := os.WriteFile(keyPath, encodePEMBlock("PRIVATE KEY", keyDER), 0o600); err != nil {
		t.Fatalf("WriteFile key error: %v", err)
	}

	return certPath, keyPath, cert
}

func TestIsForwardProxyRequest(t *testing.T) {
	absoluteURL, _ := url.Parse("https://api.anthropic.com/v1/messages")
	tests := []struct {
		name string
		req  *http.Request
		want bool
	}{
		{
			name: "connect",
			req:  &http.Request{Method: http.MethodConnect, URL: &url.URL{}},
			want: true,
		},
		{
			name: "absolute URL",
			req:  &http.Request{Method: http.MethodPost, URL: absoluteURL},
			want: true,
		},
		{
			name: "normal API route",
			req:  &http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/v1/chat/completions"}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isForwardProxyRequest(tt.req); got != tt.want {
				t.Fatalf("isForwardProxyRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProxyBodyForAuditParsesJSON(t *testing.T) {
	value, tooBig := proxyBodyForAudit([]byte(`{"ok":true}`))
	if tooBig {
		t.Fatal("tooBig = true, want false")
	}
	parsed, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %#v, want map", value)
	}
	if parsed["ok"] != true {
		t.Fatalf("ok = %#v, want true", parsed["ok"])
	}
}

func TestProxyBodyForAuditRejectsLargePayload(t *testing.T) {
	value, tooBig := proxyBodyForAudit([]byte(strings.Repeat("x", auditlog.MaxBodyCapture+1)))
	if value != nil {
		t.Fatalf("value = %#v, want nil", value)
	}
	if !tooBig {
		t.Fatal("tooBig = false, want true")
	}
}

func TestExtractAnthropicUsageEntryComputesTotal(t *testing.T) {
	handler := &forwardProxyHandler{}
	entry := handler.extractAnthropicUsageEntry(
		[]byte(`{"id":"msg_123","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":6}}`),
		"req-123",
		"claude-sonnet-4-5",
		"/v1/messages",
	)
	if entry == nil {
		t.Fatal("entry = nil")
	}
	if entry.TotalTokens != 12 {
		t.Fatalf("TotalTokens = %d, want 12", entry.TotalTokens)
	}
	if entry.RawData["cache_read_input_tokens"] != 6 {
		t.Fatalf("RawData[cache_read_input_tokens] = %v, want 6", entry.RawData["cache_read_input_tokens"])
	}
}

func TestNormalizeResponseForProxyWriteForcesHTTP11(t *testing.T) {
	resp := &http.Response{
		StatusCode:    http.StatusUnauthorized,
		Status:        "401 Unauthorized",
		Proto:         "HTTP/2.0",
		ProtoMajor:    2,
		ProtoMinor:    0,
		Body:          io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)),
		ContentLength: int64(len(`{"error":"unauthorized"}`)),
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}

	normalizeResponseForProxyWrite(resp)

	var buf bytes.Buffer
	if err := resp.Write(&buf); err != nil {
		t.Fatalf("resp.Write error: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "HTTP/1.1 401 Unauthorized\r\n") {
		t.Fatalf("unexpected wire response prefix: %q", buf.String())
	}
}

func TestWriteTestCAFilesProducesParseablePEM(t *testing.T) {
	certPath, keyPath, cert := writeTestCAFiles(t)
	if cert == nil {
		t.Fatal("cert = nil")
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("ReadFile cert error: %v", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("ReadFile key error: %v", err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair error: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate error: %v", err)
	}
	if leaf.Subject.CommonName != "gomodel-test-ca" {
		t.Fatalf("CommonName = %q, want gomodel-test-ca", leaf.Subject.CommonName)
	}
}

func TestProxyHeaderMapRedactsSensitiveHeaders(t *testing.T) {
	headers := proxyHeaderMap(http.Header{
		"Authorization": {"Bearer secret"},
		"Cookie":        {"session=secret"},
		"X-Test":        {"ok"},
	})
	data, err := json.Marshal(headers)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	if strings.Contains(string(data), "secret") {
		t.Fatalf("expected redaction, got %s", data)
	}
	if headers["X-Test"] != "ok" {
		t.Fatalf("X-Test = %q, want ok", headers["X-Test"])
	}
}
