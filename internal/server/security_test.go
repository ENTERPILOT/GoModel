package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMetricsEndpointCustomPaths verifies that custom metrics paths work correctly
func TestMetricsEndpointCustomPaths(t *testing.T) {
	mock := &mockProvider{}

	t.Run("custom metrics path is accessible without auth", func(t *testing.T) {
		srv := New(mock, &Config{
			MasterKey:       "secret-key",
			MetricsEnabled:  true,
			MetricsEndpoint: "/monitoring/metrics",
		})

		req := httptest.NewRequest(http.MethodGet, "/monitoring/metrics", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("nested metrics path works", func(t *testing.T) {
		srv := New(mock, &Config{
			MasterKey:       "secret-key",
			MetricsEnabled:  true,
			MetricsEndpoint: "/api/v2/metrics",
		})

		req := httptest.NewRequest(http.MethodGet, "/api/v2/metrics", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

// TestMetricsEndpointAPIRouteProtection verifies that metrics endpoint cannot shadow API routes
func TestMetricsEndpointAPIRouteProtection(t *testing.T) {
	mock := &mockProvider{}

	t.Run("metrics at /v1/metrics falls back to /metrics", func(t *testing.T) {
		srv := New(mock, &Config{
			MasterKey:       "secret-key",
			MetricsEnabled:  true,
			MetricsEndpoint: "/v1/metrics",
		})

		// /v1/metrics should require auth (not be metrics endpoint)
		req := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)

		// Metrics should be at /metrics instead
		req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec2 := httptest.NewRecorder()
		srv.ServeHTTP(rec2, req2)

		assert.Equal(t, http.StatusOK, rec2.Code)
	})

	t.Run("metrics at /v1/models falls back to /metrics", func(t *testing.T) {
		srv := New(mock, &Config{
			MasterKey:       "secret-key",
			MetricsEnabled:  true,
			MetricsEndpoint: "/v1/models",
		})

		// /v1/models should require auth
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("path traversal to /v1/ is blocked", func(t *testing.T) {
		srv := New(mock, &Config{
			MasterKey:       "secret-key",
			MetricsEnabled:  true,
			MetricsEndpoint: "/foo/../v1/admin",
		})

		// Metrics should fall back to /metrics
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

// TestBodyLimitHTTPMethodCoverage tests that body limits apply to all HTTP methods
func TestBodyLimitHTTPMethodCoverage(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{
		MasterKey:      "",
		MetricsEnabled: false,
	})

	// Create a body larger than 10MB
	largeBody := strings.Repeat("x", 11*1024*1024)

	// Test GET with large body (unusual but possible)
	t.Run("GET with large body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", strings.NewReader(largeBody))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code,
			"GET request with 11MB body should be rejected (body limit is 10MB); otherwise large GET bodies allow DoS")
	})

	// Test POST with large body
	t.Run("POST with large body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(largeBody))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	})
}

// TestConfigurableBodySizeLimit tests that body size limit can be configured
func TestConfigurableBodySizeLimit(t *testing.T) {
	mock := &mockProvider{}

	t.Run("default body size limit is 10M when not configured", func(t *testing.T) {
		srv := New(mock, &Config{})

		// 9MB should be accepted
		body9MB := strings.Repeat("x", 9*1024*1024)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body9MB))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.NotEqual(t, http.StatusRequestEntityTooLarge, rec.Code)

		// 11MB should be rejected
		body11MB := strings.Repeat("x", 11*1024*1024)
		req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body11MB))
		rec2 := httptest.NewRecorder()
		srv.ServeHTTP(rec2, req2)

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec2.Code)
	})

	t.Run("default body size limit is 10M when config is nil", func(t *testing.T) {
		srv := New(mock, nil)

		// 11MB should be rejected
		body11MB := strings.Repeat("x", 11*1024*1024)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body11MB))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	})

	t.Run("custom body size limit of 1M is respected", func(t *testing.T) {
		srv := New(mock, &Config{
			BodySizeLimit: "1M",
		})

		// 500KB should be accepted
		body500KB := strings.Repeat("x", 500*1024)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body500KB))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.NotEqual(t, http.StatusRequestEntityTooLarge, rec.Code)

		// 2MB should be rejected
		body2MB := strings.Repeat("x", 2*1024*1024)
		req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body2MB))
		rec2 := httptest.NewRecorder()
		srv.ServeHTTP(rec2, req2)

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec2.Code)
	})

	t.Run("custom body size limit of 20M allows larger requests", func(t *testing.T) {
		srv := New(mock, &Config{
			BodySizeLimit: "20M",
		})

		// 15MB should be accepted
		body15MB := strings.Repeat("x", 15*1024*1024)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body15MB))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.NotEqual(t, http.StatusRequestEntityTooLarge, rec.Code)

		// 25MB should be rejected
		body25MB := strings.Repeat("x", 25*1024*1024)
		req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body25MB))
		rec2 := httptest.NewRecorder()
		srv.ServeHTTP(rec2, req2)

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec2.Code)
	})

	t.Run("body size limit with kilobytes unit", func(t *testing.T) {
		srv := New(mock, &Config{
			BodySizeLimit: "500K",
		})

		// 400KB should be accepted
		body400KB := strings.Repeat("x", 400*1024)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body400KB))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.NotEqual(t, http.StatusRequestEntityTooLarge, rec.Code)

		// 600KB should be rejected
		body600KB := strings.Repeat("x", 600*1024)
		req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body600KB))
		rec2 := httptest.NewRecorder()
		srv.ServeHTTP(rec2, req2)

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec2.Code)
	})
}

// TestBodyLimitAppliesToAllRoutes tests that body limit is applied globally
func TestBodyLimitAppliesToAllRoutes(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, nil)

	largeBody := strings.Repeat("x", 11*1024*1024)

	// Body limit applies to all routes including health (DoS protection)
	req := httptest.NewRequest(http.MethodPost, "/health", strings.NewReader(largeBody))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

// TestMetricsEndpointPathTraversal tests that path traversal is normalized
func TestMetricsEndpointPathTraversal(t *testing.T) {
	mock := &mockProvider{}

	t.Run("path traversal is normalized", func(t *testing.T) {
		// /foo/../admin normalizes to /admin
		srv := New(mock, &Config{
			MasterKey:       "secret",
			MetricsEnabled:  true,
			MetricsEndpoint: "/foo/../admin",
		})

		// Normalized path /admin should serve metrics
		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("double dots are cleaned from path", func(t *testing.T) {
		// /a/b/../c normalizes to /a/c
		srv := New(mock, &Config{
			MasterKey:       "secret",
			MetricsEnabled:  true,
			MetricsEndpoint: "/a/b/../c",
		})

		req := httptest.NewRequest(http.MethodGet, "/a/c", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestPprofEndpoint_RequiresAuthWhenDisabled(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{
		MasterKey:    "secret-key",
		PprofEnabled: false,
	})

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
