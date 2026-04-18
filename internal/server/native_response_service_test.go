package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/responsestore"
)

func TestResponsesUtilityRoutesRejectNullBody(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes: map[string]string{
			"gpt-5-mini": "mock",
		},
	}
	srv := New(provider, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader("null"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if len(provider.capturedResponseUtilityReqs) != 0 {
		t.Fatalf("utility calls = %d, want 0", len(provider.capturedResponseUtilityReqs))
	}
}

func TestCancelResponseNormalizesNativeResponse(t *testing.T) {
	provider := &mockProvider{
		providerTypes: map[string]string{
			"gpt-5-mini": "mock",
		},
		responseCancelResponse: &core.ResponsesResponse{
			ID:       "provider_resp",
			Provider: "upstream",
			Status:   "cancelled",
		},
	}
	srv := New(provider, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/resp_gateway/cancel?provider=mock", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var resp core.ResponsesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID != "resp_gateway" || resp.Object != "response" || resp.Provider != "mock" {
		t.Fatalf("response = %+v, want gateway id/object/provider", resp)
	}
}

func TestCancelStoredResponseNormalizesPersistedResponse(t *testing.T) {
	store := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
	err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response:           &core.ResponsesResponse{ID: "resp_gateway", Object: "response", Provider: "mock"},
		Provider:           "mock",
		ProviderResponseID: "provider_resp",
	})
	if err != nil {
		t.Fatalf("store.Create() error = %v", err)
	}
	provider := &mockProvider{
		responseCancelResponse: &core.ResponsesResponse{
			ID:       "provider_resp",
			Provider: "upstream",
			Status:   "cancelled",
		},
	}
	srv := New(provider, &Config{ResponseStore: store})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/resp_gateway/cancel", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var resp core.ResponsesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID != "resp_gateway" || resp.Object != "response" || resp.Provider != "mock" {
		t.Fatalf("response = %+v, want gateway id/object/provider", resp)
	}

	stored, err := store.Get(context.Background(), "resp_gateway")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if stored.Response.ID != "resp_gateway" || stored.Response.Object != "response" || stored.Response.Provider != "mock" {
		t.Fatalf("stored response = %+v, want normalized gateway id/object/provider", stored.Response)
	}
}

func TestNativeResponseByProviderWrapsContextCancellation(t *testing.T) {
	provider := &mockProvider{
		providerTypes: map[string]string{
			"gpt-5-mini": "mock",
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := nativeResponseByProvider[*core.ResponsesResponse](ctx, provider, "", func(core.NativeResponseLifecycleRoutableProvider, string) (*core.ResponsesResponse, error) {
		t.Fatal("provider call should not run after context cancellation")
		return nil, nil
	})

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T %[1]v, want *core.GatewayError", err)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want 408", gatewayErr.HTTPStatusCode())
	}
}
