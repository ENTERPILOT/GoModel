package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/streaming"
	"gomodel/internal/usage"
)

type ForwardProxyConfig struct {
	Enabled         bool
	MITMHosts       []string
	CACertFile      string
	CAKeyFile       string
	AuditLogger     auditlog.LoggerInterface
	UsageLogger     usage.LoggerInterface
	PricingResolver usage.PricingResolver
	Transport       *http.Transport
}

type forwardProxyHandler struct {
	api             http.Handler
	auditLogger     auditlog.LoggerInterface
	usageLogger     usage.LoggerInterface
	pricingResolver usage.PricingResolver
	transport       *http.Transport
	authority       *mitmCertificateAuthority
	mitmHosts       map[string]struct{}
}

type mitmCertificateAuthority struct {
	cert  *x509.Certificate
	key   crypto.Signer
	cache sync.Map
}

var errForwardProxyCloseConnection = errors.New("forward proxy close connection")

func NewForwardProxyHandler(api http.Handler, cfg *ForwardProxyConfig) (http.Handler, error) {
	if api == nil {
		return nil, fmt.Errorf("forward proxy requires an API handler")
	}
	if cfg == nil || !cfg.Enabled {
		return api, nil
	}

	handler := &forwardProxyHandler{
		api:             api,
		auditLogger:     cfg.AuditLogger,
		usageLogger:     cfg.UsageLogger,
		pricingResolver: cfg.PricingResolver,
		transport:       cloneProxyTransport(cfg.Transport),
		mitmHosts:       normalizeProxyHosts(cfg.MITMHosts),
	}

	if len(handler.mitmHosts) > 0 {
		authority, err := loadMITMCertificateAuthority(cfg.CACertFile, cfg.CAKeyFile)
		if err != nil {
			return nil, err
		}
		handler.authority = authority
	}

	return handler, nil
}

func cloneProxyTransport(transport *http.Transport) *http.Transport {
	if transport == nil {
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.Proxy = nil
		return base
	}
	cloned := transport.Clone()
	cloned.Proxy = nil
	return cloned
}

func normalizeProxyHosts(hosts []string) map[string]struct{} {
	if len(hosts) == 0 {
		return nil
	}
	result := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		canonical := canonicalProxyHost(host)
		if canonical == "" {
			continue
		}
		result[canonical] = struct{}{}
	}
	return result
}

func (h *forwardProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !isForwardProxyRequest(r) {
		h.api.ServeHTTP(w, r)
		return
	}

	switch r.Method {
	case http.MethodConnect:
		h.handleConnect(w, r)
	default:
		h.handleHTTPProxy(w, r)
	}
}

func isForwardProxyRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	return r.Method == http.MethodConnect || (r.URL != nil && r.URL.IsAbs())
}

func (h *forwardProxyHandler) handleConnect(w http.ResponseWriter, r *http.Request) {
	targetAddr := targetAddress(r.Host, "443")
	targetHost := canonicalProxyHost(targetAddr)
	if _, ok := h.mitmHosts[targetHost]; ok && h.authority != nil {
		slog.Info("forward proxy CONNECT", "target", targetAddr, "mode", "mitm")
		h.handleMITMConnect(w, r, targetAddr, targetHost)
		return
	}
	slog.Info("forward proxy CONNECT", "target", targetAddr, "mode", "tunnel")
	h.handleTunnelConnect(w, targetAddr)
}

func (h *forwardProxyHandler) handleTunnelConnect(w http.ResponseWriter, targetAddr string) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "proxy hijacking is not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, "failed to hijack proxy connection", http.StatusInternalServerError)
		return
	}

	upstreamConn, err := net.DialTimeout("tcp", targetAddr, 30*time.Second)
	if err != nil {
		_, _ = io.WriteString(clientConn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		_ = clientConn.Close()
		return
	}

	_, _ = io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n")

	go func() {
		_, _ = io.Copy(upstreamConn, clientConn)
		_ = upstreamConn.Close()
	}()
	go func() {
		_, _ = io.Copy(clientConn, upstreamConn)
		_ = clientConn.Close()
	}()
}

func (h *forwardProxyHandler) handleMITMConnect(w http.ResponseWriter, r *http.Request, targetAddr, targetHost string) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "proxy hijacking is not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, "failed to hijack proxy connection", http.StatusInternalServerError)
		return
	}
	defer func() { _ = clientConn.Close() }()

	_, _ = io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n")

	cert, err := h.authority.certificateForHost(targetHost)
	if err != nil {
		slog.Error("failed to mint MITM certificate", "host", targetHost, "error", err)
		return
	}

	tlsConn := tls.Server(clientConn, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		MinVersion:   tls.VersionTLS12,
	})
	defer func() { _ = tlsConn.Close() }()

	if err := tlsConn.Handshake(); err != nil {
		slog.Debug("forward proxy TLS handshake failed", "host", targetHost, "error", err)
		return
	}
	slog.Info(
		"forward proxy TLS handshake",
		"host", targetHost,
		"alpn", tlsConn.ConnectionState().NegotiatedProtocol,
		"version", tls.VersionName(tlsConn.ConnectionState().Version),
	)

	reader := bufio.NewReader(tlsConn)
	requestCount := 0
	for {
		req, err := http.ReadRequest(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				buffered := reader.Buffered()
				prefix := ""
				if buffered > 0 {
					peek, peekErr := reader.Peek(min(buffered, 32))
					if peekErr == nil {
						prefix = sanitizeProxyPreview(peek)
					}
				}
				slog.Info(
					"forward proxy request read failed",
					"host", targetHost,
					"error", err,
					"alpn", tlsConn.ConnectionState().NegotiatedProtocol,
					"requests_served", requestCount,
					"buffered", buffered,
					"prefix", prefix,
				)
			}
			return
		}
		requestCount++
		if err := h.serveMITMRequest(tlsConn, req, targetAddr); err != nil {
			if errors.Is(err, errForwardProxyCloseConnection) {
				return
			}
			slog.Debug("forward proxy MITM request failed", "host", targetHost, "error", err)
			return
		}
	}
}

func (h *forwardProxyHandler) serveMITMRequest(clientConn net.Conn, req *http.Request, targetAddr string) error {
	defer func() {
		if req.Body != nil {
			_ = req.Body.Close()
		}
	}()

	requestID := ensureProxyRequestID(req.Header)
	start := time.Now().UTC()
	slog.Info(
		"forward proxy request",
		"target", targetAddr,
		"method", req.Method,
		"path", req.URL.Path,
		"content_length", req.ContentLength,
		"expect", strings.TrimSpace(req.Header.Get("Expect")),
		"user_agent", req.UserAgent(),
		"request_id", requestID,
	)
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return err
	}
	model, streamRequested := extractProxyRequestInfo(bodyBytes)
	if req.URL.Path == "/api/event_logging/v2/batch" {
		return h.serveSyntheticEventLoggingSuccess(clientConn, req, start, requestID, model, bodyBytes)
	}

	upstreamReq := req.Clone(context.Background())
	upstreamReq.URL = &url.URL{
		Scheme:   "https",
		Host:     targetAddr,
		Path:     req.URL.Path,
		RawPath:  req.URL.RawPath,
		RawQuery: req.URL.RawQuery,
	}
	upstreamReq.RequestURI = ""
	upstreamReq.Host = targetAddr
	upstreamReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	upstreamReq.ContentLength = int64(len(bodyBytes))
	upstreamReq.Header = cloneHeader(req.Header)
	stripProxyHeaders(upstreamReq.Header)
	upstreamReq.Header.Del("Accept-Encoding")

	resp, err := h.transport.RoundTrip(upstreamReq)
	if err != nil {
		entry := h.newProxyAuditEntry(start, requestID, req, req.URL.Path, model)
		entry.StatusCode = http.StatusBadGateway
		entry.ErrorType = "proxy_error"
		entry.Data.ErrorMessage = err.Error()
		h.writeAuditEntry(entry)
		_, _ = io.WriteString(clientConn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	slog.Info(
		"forward proxy upstream response",
		"target", targetAddr,
		"method", req.Method,
		"path", req.URL.Path,
		"status", resp.StatusCode,
		"request_id", requestID,
	)

	entry := h.newProxyAuditEntry(start, requestID, req, req.URL.Path, model)
	entry.StatusCode = resp.StatusCode
	entry.Data.RequestBody, entry.Data.RequestBodyTooBigToHandle = proxyBodyForAudit(bodyBytes)
	entry.Data.ResponseHeaders = proxyHeaderMap(resp.Header)

	if isEventStreamHeader(resp.Header) || streamRequested {
		return h.serveMITMStreamResponse(clientConn, req, resp, entry)
	}
	return h.serveMITMBufferedResponse(clientConn, req, resp, entry, bodyBytes)
}

func (h *forwardProxyHandler) serveSyntheticEventLoggingSuccess(clientConn net.Conn, req *http.Request, start time.Time, requestID, model string, requestBody []byte) error {
	entry := h.newProxyAuditEntry(start, requestID, req, req.URL.Path, model)
	entry.StatusCode = http.StatusNoContent
	entry.DurationNs = time.Since(start).Nanoseconds()
	entry.Data.RequestBody, entry.Data.RequestBodyTooBigToHandle = proxyBodyForAudit(requestBody)
	entry.Data.ResponseHeaders = map[string]string{
		"Connection":     "close",
		"Content-Length": "0",
	}
	h.writeAuditEntry(entry)

	if _, err := io.WriteString(clientConn, "HTTP/1.1 204 No Content\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"); err != nil {
		return err
	}
	return errForwardProxyCloseConnection
}

func (h *forwardProxyHandler) serveMITMBufferedResponse(clientConn net.Conn, req *http.Request, resp *http.Response, entry *auditlog.LogEntry, requestBody []byte) error {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	entry.DurationNs = time.Since(entry.Timestamp).Nanoseconds()
	entry.Data.RequestBody, entry.Data.RequestBodyTooBigToHandle = proxyBodyForAudit(requestBody)
	entry.Data.ResponseBody, entry.Data.ResponseBodyTooBigToHandle = proxyBodyForAudit(respBody)

	if usageEntry := h.extractAnthropicUsageEntry(respBody, entry.RequestID, entry.Model, req.URL.Path); usageEntry != nil {
		h.writeUsageEntry(usageEntry)
		if entry.Model == "" && usageEntry.Model != "" {
			entry.Model = usageEntry.Model
		}
	}
	h.writeAuditEntry(entry)

	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	resp.Close = true
	resp.Header.Set("Connection", "close")
	normalizeResponseForProxyWrite(resp)
	if err := resp.Write(clientConn); err != nil {
		return err
	}
	return errForwardProxyCloseConnection
}

func (h *forwardProxyHandler) serveMITMStreamResponse(clientConn net.Conn, req *http.Request, resp *http.Response, entry *auditlog.LogEntry) error {
	observers := make([]streaming.Observer, 0, 2)
	if h.auditLogger != nil && h.auditLogger.Config().Enabled {
		if observer := auditlog.NewStreamLogObserver(h.auditLogger, entry, req.URL.Path); observer != nil {
			observers = append(observers, observer)
		}
	}
	if h.usageLogger != nil && h.usageLogger.Config().Enabled {
		if observer := usage.NewStreamUsageObserver(h.usageLogger, entry.Model, entry.Provider, entry.RequestID, req.URL.Path, h.pricingResolver); observer != nil {
			observers = append(observers, observer)
		}
	}

	wrappedStream := streaming.NewObservedSSEStream(resp.Body, observers...)
	defer func() { _ = wrappedStream.Close() }()

	resp.Body = wrappedStream
	resp.Close = true
	resp.Header.Set("Connection", "close")
	normalizeResponseForProxyWrite(resp)
	if err := resp.Write(clientConn); err != nil {
		return err
	}
	return errForwardProxyCloseConnection
}

func (h *forwardProxyHandler) handleHTTPProxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now().UTC()
	requestID := ensureProxyRequestID(r.Header)

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read proxy request body", http.StatusBadRequest)
		return
	}
	model, _ := extractProxyRequestInfo(bodyBytes)

	upstreamReq := r.Clone(context.Background())
	upstreamReq.RequestURI = ""
	upstreamReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	upstreamReq.ContentLength = int64(len(bodyBytes))
	upstreamReq.Header = cloneHeader(r.Header)
	stripProxyHeaders(upstreamReq.Header)
	upstreamReq.Header.Del("Accept-Encoding")

	resp, err := h.transport.RoundTrip(upstreamReq)
	if err != nil {
		entry := h.newProxyAuditEntry(start, requestID, r, r.URL.Path, model)
		entry.StatusCode = http.StatusBadGateway
		entry.ErrorType = "proxy_error"
		entry.Data.ErrorMessage = err.Error()
		h.writeAuditEntry(entry)
		http.Error(w, "proxy upstream request failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "failed to read proxy upstream response", http.StatusBadGateway)
		return
	}

	entry := h.newProxyAuditEntry(start, requestID, r, r.URL.Path, model)
	entry.StatusCode = resp.StatusCode
	entry.DurationNs = time.Since(start).Nanoseconds()
	entry.Data.RequestBody, entry.Data.RequestBodyTooBigToHandle = proxyBodyForAudit(bodyBytes)
	entry.Data.ResponseBody, entry.Data.ResponseBodyTooBigToHandle = proxyBodyForAudit(respBody)
	entry.Data.ResponseHeaders = proxyHeaderMap(resp.Header)
	h.writeAuditEntry(entry)

	if usageEntry := h.extractAnthropicUsageEntry(respBody, requestID, model, r.URL.Path); usageEntry != nil {
		h.writeUsageEntry(usageEntry)
	}

	copyProxyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

func (h *forwardProxyHandler) newProxyAuditEntry(start time.Time, requestID string, req *http.Request, path, model string) *auditlog.LogEntry {
	entry := &auditlog.LogEntry{
		ID:         uuid.NewString(),
		Timestamp:  start,
		RequestID:  requestID,
		ClientIP:   proxyClientIP(req.RemoteAddr),
		Method:     req.Method,
		Path:       path,
		Model:      model,
		Provider:   "anthropic",
		StatusCode: http.StatusOK,
		Data: &auditlog.LogData{
			UserAgent:      req.UserAgent(),
			APIKeyHash:     proxyCredentialHash(req.Header),
			RequestHeaders: proxyHeaderMap(req.Header),
		},
	}
	return entry
}

func (h *forwardProxyHandler) writeAuditEntry(entry *auditlog.LogEntry) {
	if h.auditLogger == nil || !h.auditLogger.Config().Enabled || entry == nil {
		return
	}
	h.auditLogger.Write(entry)
}

func (h *forwardProxyHandler) writeUsageEntry(entry *usage.UsageEntry) {
	if h.usageLogger == nil || !h.usageLogger.Config().Enabled || entry == nil {
		return
	}
	h.usageLogger.Write(entry)
}

func (h *forwardProxyHandler) extractAnthropicUsageEntry(body []byte, requestID, model, endpoint string) *usage.UsageEntry {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}

	usageRaw, ok := payload["usage"].(map[string]any)
	if !ok {
		return nil
	}

	providerID, _ := payload["id"].(string)
	if responseModel, _ := payload["model"].(string); responseModel != "" {
		model = responseModel
	}

	inputTokens := int(floatFromMap(usageRaw, "input_tokens"))
	outputTokens := int(floatFromMap(usageRaw, "output_tokens"))
	totalTokens := int(floatFromMap(usageRaw, "total_tokens"))
	if totalTokens == 0 && (inputTokens > 0 || outputTokens > 0) {
		totalTokens = inputTokens + outputTokens
	}
	if inputTokens == 0 && outputTokens == 0 && totalTokens == 0 {
		return nil
	}

	rawData := make(map[string]any)
	for _, key := range []string{"cache_creation_input_tokens", "cache_read_input_tokens"} {
		if value := int(floatFromMap(usageRaw, key)); value > 0 {
			rawData[key] = value
		}
	}
	if len(rawData) == 0 {
		rawData = nil
	}

	var pricingArgs []*core.ModelPricing
	if h.pricingResolver != nil {
		if pricing := h.pricingResolver.ResolvePricing(model, "anthropic"); pricing != nil {
			pricingArgs = append(pricingArgs, pricing)
		}
	}

	return usage.ExtractFromSSEUsage(
		providerID,
		inputTokens, outputTokens, totalTokens,
		rawData,
		requestID, model, "anthropic", endpoint,
		pricingArgs...,
	)
}

func extractProxyRequestInfo(body []byte) (model string, stream bool) {
	if len(body) == 0 {
		return "", false
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false
	}
	if m, ok := payload["model"].(string); ok {
		model = strings.TrimSpace(m)
	}
	if s, ok := payload["stream"].(bool); ok {
		stream = s
	}
	return model, stream
}

func ensureProxyRequestID(headers http.Header) string {
	if headers == nil {
		return uuid.NewString()
	}
	if requestID := strings.TrimSpace(headers.Get("X-Request-ID")); requestID != "" {
		return requestID
	}
	requestID := uuid.NewString()
	headers.Set("X-Request-ID", requestID)
	return requestID
}

func proxyBodyForAudit(body []byte) (any, bool) {
	if len(body) == 0 {
		return nil, false
	}
	if len(body) > auditlog.MaxBodyCapture {
		return nil, true
	}

	var parsed any
	if err := json.Unmarshal(body, &parsed); err == nil {
		return parsed, false
	}
	return string(body), false
}

func proxyHeaderMap(headers http.Header) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) == 0 {
			continue
		}
		result[key] = values[0]
	}
	return auditlog.RedactHeaders(result)
}

func proxyCredentialHash(headers http.Header) string {
	if headers == nil {
		return ""
	}
	token := strings.TrimSpace(headers.Get("Authorization"))
	if token == "" {
		token = strings.TrimSpace(headers.Get("Cookie"))
	}
	if token == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])[:auditlog.APIKeyHashPrefixLength]
}

func proxyClientIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return strings.TrimSpace(remoteAddr)
	}
	return host
}

func targetAddress(hostPort, defaultPort string) string {
	hostPort = strings.TrimSpace(hostPort)
	if hostPort == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(hostPort); err == nil {
		return hostPort
	}
	return net.JoinHostPort(hostPort, defaultPort)
}

func normalizeResponseForProxyWrite(resp *http.Response) {
	if resp == nil {
		return
	}
	resp.Proto = "HTTP/1.1"
	resp.ProtoMajor = 1
	resp.ProtoMinor = 1
}

func canonicalProxyHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	return strings.ToLower(strings.TrimSpace(host))
}

func cloneHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	cloned := make(http.Header, len(headers))
	for key, values := range headers {
		dst := make([]string, len(values))
		copy(dst, values)
		cloned[key] = dst
	}
	return cloned
}

func stripProxyHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	for _, key := range []string{
		"Proxy-Connection",
		"Proxy-Authorization",
		"Proxy-Authenticate",
		"Connection",
	} {
		headers.Del(key)
	}
}

func copyProxyResponseHeaders(dst, src http.Header) {
	if dst == nil || src == nil {
		return
	}
	connectionHeaders := passthroughConnectionHeaders(src)
	for key, values := range src {
		canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if len(values) == 0 {
			continue
		}
		if _, hopByHop := connectionHeaders[canonicalKey]; hopByHop {
			continue
		}
		switch canonicalKey {
		case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
			continue
		}
		dst.Del(canonicalKey)
		for _, value := range values {
			dst.Add(canonicalKey, value)
		}
	}
}

func sanitizeProxyPreview(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	const maxLen = 32
	if len(data) > maxLen {
		data = data[:maxLen]
	}
	out := make([]byte, 0, len(data))
	for _, b := range data {
		if b >= 32 && b <= 126 {
			out = append(out, b)
			continue
		}
		switch b {
		case '\r':
			out = append(out, '\\', 'r')
		case '\n':
			out = append(out, '\\', 'n')
		case '\t':
			out = append(out, '\\', 't')
		default:
			out = append(out, '.')
		}
	}
	return string(out)
}

func isEventStreamHeader(headers http.Header) bool {
	for key, values := range headers {
		if !strings.EqualFold(key, "Content-Type") {
			continue
		}
		for _, value := range values {
			if strings.Contains(strings.ToLower(value), "text/event-stream") {
				return true
			}
		}
	}
	return false
}

func floatFromMap(values map[string]any, key string) float64 {
	value, ok := values[key]
	if !ok {
		return 0
	}
	if number, ok := value.(float64); ok {
		return number
	}
	return 0
}

func loadMITMCertificateAuthority(certPath, keyPath string) (*mitmCertificateAuthority, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read forward proxy CA certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read forward proxy CA key: %w", err)
	}

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to load forward proxy CA keypair: %w", err)
	}

	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("failed to parse forward proxy CA certificate: %w", err)
	}

	signer, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("forward proxy CA key does not implement crypto.Signer")
	}

	return &mitmCertificateAuthority{
		cert: leaf,
		key:  signer,
	}, nil
}

func (a *mitmCertificateAuthority) certificateForHost(host string) (*tls.Certificate, error) {
	if cached, ok := a.cache.Load(host); ok {
		if cert, ok := cached.(*tls.Certificate); ok {
			return cert, nil
		}
	}

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: host,
		},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              minTime(a.cert.NotAfter, time.Now().Add(24*time.Hour)),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}

	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, privateKey.Public(), a.key)
	if err != nil {
		return nil, err
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}

	cert := &tls.Certificate{
		Certificate: [][]byte{der, a.cert.Raw},
		PrivateKey:  privateKey,
		Leaf:        leaf,
	}
	a.cache.Store(host, cert)
	return cert, nil
}

func minTime(first, second time.Time) time.Time {
	if first.Before(second) {
		return first
	}
	return second
}

func encodePEMBlock(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}
