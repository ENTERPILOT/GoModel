package ext

import (
	"crypto/tls"
	"net/http"
	"time"

	"github.com/enterpilot/gomodel/internal/httpclient"
)

// OutboundTLSConfig returns a copy of the client TLS configuration core uses
// for outbound HTTPS (http.tls: private CA bundle, client certificate), or nil
// when the gateway trusts the operating system store only. Extensions that
// build their own transport, such as one dialing through a specific proxy,
// set it as the transport's TLSClientConfig.
//
// Core installs it before SetupConfig and ReloadConfig run. The copy is a
// snapshot: a later reload that changes http.tls does not reach it, while a
// client from NewHTTPClient follows the change.
func OutboundTLSConfig() *tls.Config {
	return httpclient.ConfiguredTLS()
}

// NewHTTPClient returns a client on core's outbound transport: the process
// HTTP_PROXY/HTTPS_PROXY/NO_PROXY environment, the http.tls trust (following
// reloads), and core's dial and keep-alive tuning. timeout bounds each
// request; zero or less keeps core's default HTTP timeout. Extensions use it
// for their own upstream calls, such as a secret store, so they trust the
// same private CAs as the gateway.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return httpclient.NewClientWithTimeout(timeout)
}
