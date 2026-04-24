package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gomodel/internal/admin"
	"gomodel/internal/admin/dashboard"
	"gomodel/internal/core"

	_ "gomodel/cmd/gomodel/docs"

	"github.com/labstack/echo/v5"
)

func TestRequestIDMiddleware(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, nil)

	t.Run("generates request ID when missing", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()

		srv.ServeHTTP(rec, req)

		got := rec.Header().Get(core.RequestIDHeader)
		if got == "" {
			t.Fatal("expected X-Request-ID in response header, got empty")
		}
		// Validate UUID format (8-4-4-4-12 hex digits)
		if len(got) != 36 {
			t.Errorf("expected UUID (36 chars), got %q (%d chars)", got, len(got))
		}
	})

	t.Run("preserves existing request ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Header.Set(core.RequestIDHeader, "my-custom-id")
		rec := httptest.NewRecorder()

		srv.ServeHTTP(rec, req)

		// Request header must not be overwritten
		got := req.Header.Get(core.RequestIDHeader)
		if got != "my-custom-id" {
			t.Errorf("expected request header to be preserved as %q, got %q", "my-custom-id", got)
		}

		// Response header must echo the client-provided ID back
		respID := rec.Header().Get(core.RequestIDHeader)
		if respID != "my-custom-id" {
			t.Errorf("expected response header X-Request-ID to be %q, got %q", "my-custom-id", respID)
		}
	})
}

func TestServerUsesDirectIPExtractorByDefault(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, nil)
	srv.echo.GET("/debug/ip", func(c *echo.Context) error {
		return c.String(http.StatusOK, c.RealIP())
	})

	req := httptest.NewRequest(http.MethodGet, "/debug/ip", nil)
	req.RemoteAddr = "203.0.113.7:4321"
	req.Header.Set("X-Forwarded-For", "198.51.100.8")
	req.Header.Set("X-Real-IP", "198.51.100.9")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "203.0.113.7" {
		t.Fatalf("RealIP = %q, want remote address host", got)
	}
}

func TestServerAllowsTrustedProxyIPExtractorOverride(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{
		IPExtractor: echo.ExtractIPFromXFFHeader(),
	})
	srv.echo.GET("/debug/ip", func(c *echo.Context) error {
		return c.String(http.StatusOK, c.RealIP())
	})

	req := httptest.NewRequest(http.MethodGet, "/debug/ip", nil)
	req.RemoteAddr = "10.0.0.10:4321"
	req.Header.Set("X-Forwarded-For", "198.51.100.8")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "198.51.100.8" {
		t.Fatalf("RealIP = %q, want X-Forwarded-For client IP", got)
	}
}

func TestStartWithListener(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, nil)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- srv.StartWithListener(ctx, listener)
	}()

	client := &http.Client{Timeout: 200 * time.Millisecond}
	url := "http://" + listener.Addr().String() + "/health"
	var lastErr error
	for i := 0; i < 20; i++ {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				cancel()
				if err := <-done; err != nil {
					t.Fatalf("StartWithListener() error = %v", err)
				}
				return
			}
			lastErr = nil
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("StartWithListener() error after timeout = %v", err)
	}
	t.Fatalf("health check never succeeded, last error: %v", lastErr)
}

func TestMetricsEndpoint(t *testing.T) {
	tests := []struct {
		name           string
		config         *Config
		requestPath    string
		expectedStatus int
		expectBody     string // substring to check in response body
	}{
		{
			name: "metrics enabled - default endpoint accessible",
			config: &Config{
				MetricsEnabled:  true,
				MetricsEndpoint: "/metrics",
			},
			requestPath:    "/metrics",
			expectedStatus: http.StatusOK,
			expectBody:     "go_goroutines", // Standard Go runtime metric
		},
		{
			name: "metrics enabled - empty endpoint defaults to /metrics",
			config: &Config{
				MetricsEnabled:  true,
				MetricsEndpoint: "",
			},
			requestPath:    "/metrics",
			expectedStatus: http.StatusOK,
			expectBody:     "go_goroutines",
		},
		{
			name: "metrics disabled - endpoint returns 404",
			config: &Config{
				MetricsEnabled:  false,
				MetricsEndpoint: "/metrics",
			},
			requestPath:    "/metrics",
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "nil config - metrics disabled by default",
			config:         nil,
			requestPath:    "/metrics",
			expectedStatus: http.StatusNotFound,
		},
		{
			name: "custom metrics endpoint path",
			config: &Config{
				MetricsEnabled:  true,
				MetricsEndpoint: "/custom-metrics",
			},
			requestPath:    "/custom-metrics",
			expectedStatus: http.StatusOK,
			expectBody:     "go_goroutines",
		},
		{
			name: "custom endpoint - default path returns 404",
			config: &Config{
				MetricsEnabled:  true,
				MetricsEndpoint: "/custom-metrics",
			},
			requestPath:    "/metrics",
			expectedStatus: http.StatusNotFound,
		},
		{
			name: "metrics endpoint with nested path",
			config: &Config{
				MetricsEnabled:  true,
				MetricsEndpoint: "/api/v1/metrics",
			},
			requestPath:    "/api/v1/metrics",
			expectedStatus: http.StatusOK,
			expectBody:     "go_goroutines",
		},
		{
			name: "metrics endpoint conflicting with passthrough route falls back to default",
			config: &Config{
				MetricsEnabled:  true,
				MetricsEndpoint: "/p/internal-metrics",
			},
			requestPath:    "/metrics",
			expectedStatus: http.StatusOK,
			expectBody:     "go_goroutines",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockProvider{}
			srv := New(mock, tt.config)

			req := httptest.NewRequest(http.MethodGet, tt.requestPath, nil)
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			if tt.expectBody != "" && !strings.Contains(rec.Body.String(), tt.expectBody) {
				t.Errorf("expected body to contain %q, got: %s", tt.expectBody, rec.Body.String())
			}
		})
	}
}

func TestMetricsEndpointReturnsPrometheusFormat(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{
		MetricsEnabled:  true,
		MetricsEndpoint: "/metrics",
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Check for Prometheus text format indicators
	// Prometheus metrics should contain HELP and TYPE comments
	if !strings.Contains(body, "# HELP") {
		t.Error("response should contain Prometheus HELP comments")
	}
	if !strings.Contains(body, "# TYPE") {
		t.Error("response should contain Prometheus TYPE comments")
	}

	// Check for standard Go runtime metrics that are always present
	standardMetrics := []string{
		"go_goroutines",
		"go_gc_duration_seconds",
		"go_memstats_alloc_bytes",
		"process_cpu_seconds_total",
	}

	for _, metric := range standardMetrics {
		if !strings.Contains(body, metric) {
			t.Errorf("response should contain standard metric %q", metric)
		}
	}

	// Check Content-Type header
	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/plain") {
		t.Errorf("expected Content-Type to contain text/plain, got %s", contentType)
	}
}

func TestServerWithMasterKeyAndMetrics(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{
		MasterKey:       "test-secret-key",
		MetricsEnabled:  true,
		MetricsEndpoint: "/metrics",
	})

	t.Run("metrics endpoint is public even when master key is set", func(t *testing.T) {
		// Metrics endpoint should be accessible without auth for Prometheus scraping
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()

		srv.ServeHTTP(rec, req)

		// Should return 200 - metrics is public for load balancers and monitoring
		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200 for public metrics endpoint, got %d", rec.Code)
		}
	})

	t.Run("health endpoint is public even when master key is set", func(t *testing.T) {
		// Health endpoint should be accessible without auth for load balancer health checks
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()

		srv.ServeHTTP(rec, req)

		// Should return 200 - health is public for load balancers
		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200 for public health endpoint, got %d", rec.Code)
		}
	})

	t.Run("API endpoints require auth when master key is set", func(t *testing.T) {
		// API endpoints should require auth
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		rec := httptest.NewRecorder()

		srv.ServeHTTP(rec, req)

		// Should return 401 - API requires auth
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected status 401 for protected API endpoint, got %d", rec.Code)
		}
	})

	t.Run("API endpoints accessible with valid auth", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer test-secret-key")
		rec := httptest.NewRecorder()

		srv.ServeHTTP(rec, req)

		// Should return 200 with valid auth
		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200 with valid auth, got %d", rec.Code)
		}
	})
}

func TestServer_ManagedAuthKeyUserPathOverridesHeaderBeforeWorkflowResolution(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes:   map[string]string{"gpt-5-mini": "openai"},
		response: &core.ChatResponse{
			ID:       "chatcmpl-test",
			Object:   "chat.completion",
			Model:    "gpt-5-mini",
			Provider: "openai",
			Choices: []core.Choice{
				{
					Index:        0,
					FinishReason: "stop",
					Message: core.ResponseMessage{
						Role:    "assistant",
						Content: "ok",
					},
				},
			},
		},
	}

	var capturedSelector core.WorkflowSelector
	srv := New(mock, &Config{
		Authenticator: mockAuthenticator{
			enabled:   true,
			tokenToID: map[string]string{"managed-token": "key-123"},
			tokenPath: map[string]string{"managed-token": "/team/from-key"},
		},
		WorkflowPolicyResolver: requestWorkflowPolicyResolverFunc(func(selector core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
			capturedSelector = selector
			return nil, nil
		}),
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer managed-token")
	req.Header.Set(core.UserPathHeader, "/team/from-header")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if capturedSelector.UserPath != "/team/from-key" {
		t.Fatalf("selector.UserPath = %q, want /team/from-key", capturedSelector.UserPath)
	}
}

func newDashboardHandler(t *testing.T) *dashboard.Handler {
	t.Helper()
	h, err := dashboard.New()
	if err != nil {
		t.Fatalf("failed to create dashboard handler: %v", err)
	}
	return h
}

func TestAdminEndpoints_Enabled(t *testing.T) {
	mock := &mockProvider{}
	adminHandler := admin.NewHandler(nil, nil)
	srv := New(mock, &Config{
		AdminEndpointsEnabled: true,
		AdminHandler:          adminHandler,
	})

	for _, path := range []string{"/admin/api/v1/models", "/admin/api/v1/providers/status", "/admin/api/v1/audit/log", "/admin/api/v1/audit/conversation?log_id=abc"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 for %s, got %d", path, rec.Code)
		}
	}
}

func TestAdminWorkflowEndpoints_AreRegistered(t *testing.T) {
	mock := &mockProvider{}
	adminHandler := admin.NewHandler(nil, nil)
	srv := New(mock, &Config{
		AdminEndpointsEnabled: true,
		AdminHandler:          adminHandler,
	})

	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/admin/api/v1/dashboard/config"},
		{method: http.MethodGet, path: "/admin/api/v1/providers/status"},
		{method: http.MethodPost, path: "/admin/api/v1/runtime/refresh"},
		{method: http.MethodGet, path: "/admin/api/v1/auth-keys"},
		{method: http.MethodPost, path: "/admin/api/v1/auth-keys"},
		{method: http.MethodPost, path: "/admin/api/v1/auth-keys/test-key/deactivate"},
		{method: http.MethodGet, path: "/admin/api/v1/workflows"},
		{method: http.MethodGet, path: "/admin/api/v1/workflows/guardrails"},
		{method: http.MethodPost, path: "/admin/api/v1/workflows"},
		{method: http.MethodPost, path: "/admin/api/v1/workflows/test-workflow/deactivate"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Fatalf("%s %s returned %d, want registered route and method", tc.method, tc.path, rec.Code)
		}
	}
}

func TestAdminDashboardConfigEndpoint_ReturnsHandlerResponse(t *testing.T) {
	mock := &mockProvider{}
	adminHandler := admin.NewHandler(nil, nil, admin.WithDashboardRuntimeConfig(admin.DashboardConfigResponse{
		FeatureFallbackMode: "manual",
	}))
	srv := New(mock, &Config{
		AdminEndpointsEnabled: true,
		AdminHandler:          adminHandler,
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/api/v1/dashboard/config", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"FEATURE_FALLBACK_MODE":"manual"`) {
		t.Fatalf("response body = %s, want FEATURE_FALLBACK_MODE payload", rec.Body.String())
	}
}

func TestAdminEndpoints_Disabled(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{
		AdminEndpointsEnabled: false,
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/api/v1/models", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestAdminUI_Enabled(t *testing.T) {
	mock := &mockProvider{}
	dashHandler := newDashboardHandler(t)
	adminHandler := admin.NewHandler(nil, nil)
	srv := New(mock, &Config{
		AdminEndpointsEnabled: true,
		AdminUIEnabled:        true,
		AdminHandler:          adminHandler,
		DashboardHandler:      dashHandler,
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("expected text/html Content-Type, got %s", contentType)
	}
}

func TestAdminUI_Disabled(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{
		AdminEndpointsEnabled: true,
		AdminUIEnabled:        false,
		AdminHandler:          admin.NewHandler(nil, nil),
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestAdminDashboard_SkipsAuth(t *testing.T) {
	mock := &mockProvider{}
	dashHandler := newDashboardHandler(t)
	adminHandler := admin.NewHandler(nil, nil)
	srv := New(mock, &Config{
		MasterKey:             "test-secret-key",
		AdminEndpointsEnabled: true,
		AdminUIEnabled:        true,
		AdminHandler:          adminHandler,
		DashboardHandler:      dashHandler,
	})

	// Dashboard should be accessible without auth
	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (no auth), got %d", rec.Code)
	}
}

func TestAdminAPI_RequiresAuth(t *testing.T) {
	mock := &mockProvider{}
	adminHandler := admin.NewHandler(nil, nil)
	srv := New(mock, &Config{
		MasterKey:             "test-secret-key",
		AdminEndpointsEnabled: true,
		AdminHandler:          adminHandler,
	})

	// Admin API should require auth
	req := httptest.NewRequest(http.MethodGet, "/admin/api/v1/models", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAdminAPI_SkipsAuthWithoutMasterKey(t *testing.T) {
	mock := &mockProvider{}
	adminHandler := admin.NewHandler(nil, nil)
	srv := New(mock, &Config{
		Authenticator:         mockAuthenticator{enabled: true, tokenToID: map[string]string{"managed-token": "key-123"}},
		AdminEndpointsEnabled: true,
		AdminHandler:          adminHandler,
	})

	adminReq := httptest.NewRequest(http.MethodGet, "/admin/api/v1/models", nil)
	adminRec := httptest.NewRecorder()
	srv.ServeHTTP(adminRec, adminReq)

	if adminRec.Code != http.StatusOK {
		t.Fatalf("expected admin API 200 without auth when master key is unset, got %d body=%s", adminRec.Code, adminRec.Body.String())
	}

	modelReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelRec := httptest.NewRecorder()
	srv.ServeHTTP(modelRec, modelReq)

	if modelRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected model API 401 without auth when managed keys are enabled, got %d body=%s", modelRec.Code, modelRec.Body.String())
	}
}

func TestAdminStaticAssets_SkipAuth(t *testing.T) {
	mock := &mockProvider{}
	dashHandler := newDashboardHandler(t)
	adminHandler := admin.NewHandler(nil, nil)
	srv := New(mock, &Config{
		MasterKey:             "test-secret-key",
		AdminEndpointsEnabled: true,
		AdminUIEnabled:        true,
		AdminHandler:          adminHandler,
		DashboardHandler:      dashHandler,
	})

	// Static assets should be accessible without auth
	req := httptest.NewRequest(http.MethodGet, "/admin/static/css/dashboard.css", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for static asset without auth, got %d", rec.Code)
	}
}

func TestHealthEndpointAlwaysAvailable(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
	}{
		{
			name:   "nil config",
			config: nil,
		},
		{
			name: "metrics disabled",
			config: &Config{
				MetricsEnabled: false,
			},
		},
		{
			name: "metrics enabled",
			config: &Config{
				MetricsEnabled:  true,
				MetricsEndpoint: "/metrics",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockProvider{}
			srv := New(mock, tt.config)

			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("expected status 200, got %d", rec.Code)
			}
		})
	}
}

func TestSwaggerEndpoint_Enabled(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{SwaggerEnabled: true})

	req := httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("expected text/html Content-Type, got %s", contentType)
	}

	if !strings.Contains(rec.Body.String(), "swagger") {
		t.Errorf("expected body to contain swagger UI content, got: %s", rec.Body.String()[:min(200, len(rec.Body.String()))])
	}
}

func TestSwaggerEndpoint_Disabled(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{SwaggerEnabled: false})

	req := httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}
}

func TestSwaggerEndpoint_NilConfig(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, nil)

	req := httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}
}

func TestSwaggerDocJson_ReturnsExpectedContent(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{SwaggerEnabled: true})

	req := httptest.NewRequest(http.MethodGet, "/swagger/doc.json", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "GoModel") {
		t.Errorf("expected doc.json to contain GoModel API title, got: %s", body[:min(300, len(body))])
	}
	if !strings.Contains(body, "swagger") {
		t.Errorf("expected doc.json to contain swagger spec, got: %s", body[:min(300, len(body))])
	}
}

func TestPprofEndpoint_Enabled(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{PprofEnabled: true})

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Types of profiles available:") {
		t.Errorf("expected pprof index content, got: %s", body[:min(200, len(body))])
	}
	if !strings.Contains(body, "goroutine") {
		t.Errorf("expected pprof index to list goroutine profile, got: %s", body[:min(200, len(body))])
	}
}

func TestPprofEndpoint_Disabled(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{PprofEnabled: false})

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}
}

func TestPprofEndpoint_NilConfig(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, nil)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}
}

func TestServerWithMasterKeyAndPprof(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{
		MasterKey:    "test-secret-key",
		PprofEnabled: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 for public pprof endpoint, got %d", rec.Code)
	}
}

func TestProviderPassthroughRoute_EnabledByDefault(t *testing.T) {
	mock := &mockProvider{
		providerNames: map[string]string{
			"passthrough/openai": "openai",
		},
		providerTypes: map[string]string{
			"passthrough/openai": "openai",
		},
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers: map[string][]string{
				"Content-Type": {"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
		},
	}
	srv := New(mock, &Config{})

	req := httptest.NewRequest(http.MethodPost, "/p/openai/responses", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if got := mock.lastPassthroughProvider; got != "openai" {
		t.Fatalf("provider = %q, want openai", got)
	}

	mock.lastPassthroughProvider = ""
	mock.lastPassthroughReq = nil
	mock.passthroughResponse = &core.PassthroughResponse{
		StatusCode: http.StatusOK,
		Headers: map[string][]string{
			"Content-Type": {"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}

	reqV1 := httptest.NewRequest(http.MethodPost, "/p/openai/v1/responses", strings.NewReader(`{"model":"gpt-5-mini"}`))
	reqV1.Header.Set("Content-Type", "application/json")
	recV1 := httptest.NewRecorder()

	srv.ServeHTTP(recV1, reqV1)

	if recV1.Code != http.StatusOK {
		t.Fatalf("expected normalized v1 route status 200, got %d", recV1.Code)
	}
	if got := mock.lastPassthroughProvider; got != "openai" {
		t.Fatalf("normalized v1 provider = %q, want openai", got)
	}
}

func TestProviderPassthroughRoute_DisabledRequiresAuthBefore404(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{
		MasterKey:                "test-secret-key",
		DisablePassthroughRoutes: true,
	})

	req := httptest.NewRequest(http.MethodPost, "/p/openai/responses", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}

	authReq := httptest.NewRequest(http.MethodPost, "/p/openai/responses", strings.NewReader(`{"model":"gpt-5-mini"}`))
	authReq.Header.Set("Content-Type", "application/json")
	authReq.Header.Set("Authorization", "Bearer test-secret-key")
	authRec := httptest.NewRecorder()

	srv.ServeHTTP(authRec, authReq)

	if authRec.Code != http.StatusNotFound {
		t.Fatalf("expected authenticated status 404, got %d", authRec.Code)
	}
	if mock.lastPassthroughProvider != "" || mock.lastPassthroughReq != nil {
		t.Fatal("passthrough handler should not be invoked when provider passthrough is disabled")
	}
}
