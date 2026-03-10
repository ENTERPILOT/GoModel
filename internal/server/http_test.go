package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gomodel/internal/admin"
	"gomodel/internal/admin/dashboard"
	"gomodel/internal/core"

	_ "gomodel/cmd/gomodel/docs"
)

func TestRequestIDMiddleware(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, nil)

	t.Run("generates request ID when missing", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()

		srv.ServeHTTP(rec, req)

		got := rec.Header().Get("X-Request-ID")
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
		req.Header.Set("X-Request-ID", "my-custom-id")
		rec := httptest.NewRecorder()

		srv.ServeHTTP(rec, req)

		// Request header must not be overwritten
		got := req.Header.Get("X-Request-ID")
		if got != "my-custom-id" {
			t.Errorf("expected request header to be preserved as %q, got %q", "my-custom-id", got)
		}

		// Response header must echo the client-provided ID back
		respID := rec.Header().Get("X-Request-ID")
		if respID != "my-custom-id" {
			t.Errorf("expected response header X-Request-ID to be %q, got %q", "my-custom-id", respID)
		}
	})
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

	for _, path := range []string{"/admin/api/v1/models", "/admin/api/v1/audit/log", "/admin/api/v1/audit/conversation?log_id=abc"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 for %s, got %d", path, rec.Code)
		}
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
	if !strings.Contains(body, "GOModel") {
		t.Errorf("expected doc.json to contain GOModel API title, got: %s", body[:min(300, len(body))])
	}
	if !strings.Contains(body, "swagger") {
		t.Errorf("expected doc.json to contain swagger spec, got: %s", body[:min(300, len(body))])
	}
}

func TestProviderPassthroughRoute_EnabledByDefault(t *testing.T) {
	mock := &mockProvider{
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
}

func TestProviderPassthroughRoute_DisabledReturns404(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{
		MasterKey:                  "test-secret-key",
		DisableProviderPassthrough: true,
	})

	req := httptest.NewRequest(http.MethodPost, "/p/openai/responses", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rec.Code)
	}
}
