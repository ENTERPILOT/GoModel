package run

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/config"
)

func runHealthProbe(timeout time.Duration) error {
	result, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	endpoint := healthProbeURL(result.Config.Server)
	if timeout <= 0 {
		timeout = defaultHealthTimeout
	}

	client, err := probeClient(result.Config.Server, timeout)
	if err != nil {
		return err
	}
	return checkHealthEndpoint(context.Background(), client, endpoint)
}

func runReadyProbe(timeout time.Duration) error {
	result, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	endpoint := probeURL(result.Config.Server, "/health/ready")
	if timeout <= 0 {
		timeout = defaultReadyTimeout
	}

	client, err := probeClient(result.Config.Server, timeout)
	if err != nil {
		return err
	}
	return checkReadyEndpoint(context.Background(), client, endpoint)
}

// probeClient returns the client the probes reach the local listener with.
//
// Over TLS it does not verify the server certificate: the probe dials its own
// process on loopback, and a certificate issued for the service name rarely
// lists 127.0.0.1. When the listener requires client certificates, the probe
// presents the serving pair, which passes when the same CA issued it for
// client authentication too. The paths may be ${env:...} or ${file:...}
// references; other schemes are left to the running gateway.
func probeClient(server config.ServerConfig, timeout time.Duration) (*http.Client, error) {
	client := &http.Client{Timeout: timeout}
	if !server.TLSEnabled() {
		return client, nil
	}
	//nolint:gosec // Loopback probe of this process; see the comment above.
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}
	if strings.TrimSpace(server.TLSClientCAFile) != "" {
		builtins := config.NewSecrets()
		certFile, err := builtins.Resolve(context.Background(), server.TLSCertFile)
		if err != nil {
			return nil, fmt.Errorf("resolve server.tls_cert_file: %w", err)
		}
		keyFile, err := builtins.Resolve(context.Background(), server.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("resolve server.tls_key_file: %w", err)
		}
		cert, err := tls.LoadX509KeyPair(strings.TrimSpace(certFile), strings.TrimSpace(keyFile))
		if err != nil {
			return nil, fmt.Errorf("load the serving certificate to present as the probe's client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	client.Transport = transport
	return client, nil
}

func healthProbeURL(server config.ServerConfig) string {
	return probeURL(server, "/health")
}

func probeURL(server config.ServerConfig, path string) string {
	port := strings.TrimSpace(server.Port)
	if port == "" {
		port = "8080"
	}

	u := url.URL{
		Scheme: server.Scheme(),
		Host:   net.JoinHostPort("127.0.0.1", port),
		Path:   config.JoinBasePath(server.BasePath, path),
	}
	return u.String()
}

func checkHealthEndpoint(ctx context.Context, client *http.Client, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build health request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request health endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned HTTP %d", resp.StatusCode)
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&body); err != nil {
		return fmt.Errorf("decode health response: %w", err)
	}
	if body.Status != "ok" {
		return fmt.Errorf("health status is %q", body.Status)
	}
	return nil
}

// checkReadyEndpoint treats both "ready" and "degraded" as serviceable (exit 0)
// and only fails on "not_ready" (HTTP 503) — a degraded gateway still handles
// traffic, so it should remain in rotation.
func checkReadyEndpoint(ctx context.Context, client *http.Client, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build readiness request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request readiness endpoint: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&body); err != nil {
		return fmt.Errorf("decode readiness response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness endpoint returned HTTP %d (status %q)", resp.StatusCode, body.Status)
	}
	if body.Status != "ready" && body.Status != "degraded" {
		return fmt.Errorf("readiness status is %q", body.Status)
	}
	return nil
}
