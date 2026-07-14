package run

import (
	"context"
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

	client := &http.Client{Timeout: timeout}
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

	client := &http.Client{Timeout: timeout}
	return checkReadyEndpoint(context.Background(), client, endpoint)
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
		Scheme: "http",
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
