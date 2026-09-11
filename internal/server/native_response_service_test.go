package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/responsestore"
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

func TestGetStoredResponseRefreshesNonTerminalSnapshot(t *testing.T) {
	store := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
	err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response:           &core.ResponsesResponse{ID: "resp_gateway", Object: "response", Provider: "mock", Status: "queued"},
		Provider:           "mock",
		ProviderResponseID: "provider_resp",
	})
	if err != nil {
		t.Fatalf("store.Create() error = %v", err)
	}
	provider := &mockProvider{
		responseGetResponse: &core.ResponsesResponse{
			ID:       "provider_resp",
			Object:   "response",
			Provider: "upstream",
			Status:   "completed",
			Output: []core.ResponsesOutputItem{
				{ID: "msg_1", Type: "message", Role: "assistant"},
			},
		},
	}
	srv := New(provider, &Config{ResponseStore: store})

	req := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_gateway", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var resp core.ResponsesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "completed" {
		t.Fatalf("status = %q, want completed", resp.Status)
	}
	if resp.ID != "resp_gateway" || resp.Object != "response" || resp.Provider != "mock" {
		t.Fatalf("response = %+v, want gateway id/object/provider", resp)
	}
	if len(provider.responseGetCalls) != 1 || provider.responseGetCalls[0].id != "provider_resp" {
		t.Fatalf("provider get calls = %+v, want one lookup of provider_resp", provider.responseGetCalls)
	}

	stored, err := store.Get(context.Background(), "resp_gateway")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if stored.Response.Status != "completed" || stored.Response.ID != "resp_gateway" {
		t.Fatalf("stored response = %+v, want persisted terminal snapshot", stored.Response)
	}
}

func TestGetStoredResponseKeepsTerminalSnapshot(t *testing.T) {
	store := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
	err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response:           &core.ResponsesResponse{ID: "resp_gateway", Object: "response", Provider: "mock", Status: "completed"},
		Provider:           "mock",
		ProviderResponseID: "provider_resp",
	})
	if err != nil {
		t.Fatalf("store.Create() error = %v", err)
	}
	provider := &mockProvider{}
	srv := New(provider, &Config{ResponseStore: store})

	req := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_gateway", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(provider.responseGetCalls) != 0 {
		t.Fatalf("provider get calls = %+v, want none for a terminal snapshot", provider.responseGetCalls)
	}
}

func TestGetStoredResponseKeepsConcurrentCancellation(t *testing.T) {
	store := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
	err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response:           &core.ResponsesResponse{ID: "resp_gateway", Object: "response", Provider: "mock", Status: "queued"},
		Provider:           "mock",
		ProviderResponseID: "provider_resp",
	})
	if err != nil {
		t.Fatalf("store.Create() error = %v", err)
	}
	provider := &mockProvider{
		responseGetResponse: &core.ResponsesResponse{ID: "provider_resp", Object: "response", Status: "completed"},
	}
	// A cancel lands while the provider lookup is in flight.
	provider.responseGetHook = func() {
		updateErr := store.Update(context.Background(), &responsestore.StoredResponse{
			Response:           &core.ResponsesResponse{ID: "resp_gateway", Object: "response", Provider: "mock", Status: "cancelled"},
			Provider:           "mock",
			ProviderResponseID: "provider_resp",
		})
		if updateErr != nil {
			t.Errorf("store.Update() error = %v", updateErr)
		}
	}
	srv := New(provider, &Config{ResponseStore: store})

	req := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_gateway", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var resp core.ResponsesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "cancelled" {
		t.Fatalf("status = %q, want the cancellation to win", resp.Status)
	}

	stored, err := store.Get(context.Background(), "resp_gateway")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if stored.Response.Status != "cancelled" {
		t.Fatalf("stored status = %q, want cancelled", stored.Response.Status)
	}
}

func TestGetStoredResponseServesSnapshotWhenRefreshFails(t *testing.T) {
	tests := []struct {
		name         string
		lifecycleErr error
	}{
		{name: "unsupported operation", lifecycleErr: unsupportedResponseOperation("native response retrieval is not available for this provider")},
		{name: "not found upstream", lifecycleErr: core.NewNotFoundError("response not found")},
		{name: "unexpected provider error", lifecycleErr: errors.New("refresh failed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
			err := store.Create(context.Background(), &responsestore.StoredResponse{
				Response:           &core.ResponsesResponse{ID: "resp_gateway", Object: "response", Provider: "mock", Status: "in_progress"},
				Provider:           "mock",
				ProviderResponseID: "provider_resp",
			})
			if err != nil {
				t.Fatalf("store.Create() error = %v", err)
			}
			provider := &mockProvider{responseLifecycleErr: tt.lifecycleErr}
			srv := New(provider, &Config{ResponseStore: store})

			req := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_gateway", nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
			}
			var resp core.ResponsesResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Status != "in_progress" || resp.ID != "resp_gateway" {
				t.Fatalf("response = %+v, want the stored snapshot", resp)
			}
			stored, err := store.Get(context.Background(), "resp_gateway")
			if err != nil {
				t.Fatalf("store.Get() error = %v", err)
			}
			if stored.Response.Status != "in_progress" {
				t.Fatalf("stored status = %q, want the snapshot left unchanged", stored.Response.Status)
			}
		})
	}
}

func TestResponseLifecycleRoutesIgnoreJSONBody(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{name: "cancel empty object", method: http.MethodPost, path: "/v1/responses/resp_gateway/cancel?provider=mock", body: `{}`, want: http.StatusOK},
		{name: "cancel unrelated field", method: http.MethodPost, path: "/v1/responses/resp_gateway/cancel?provider=mock", body: `{"foo":1}`, want: http.StatusOK},
		{name: "retrieve with body", method: http.MethodGet, path: "/v1/responses/resp_gateway?provider=mock", body: `{}`, want: http.StatusOK},
		{name: "input items with body", method: http.MethodGet, path: "/v1/responses/resp_gateway/input_items?provider=mock", body: `{}`, want: http.StatusOK},
		{name: "delete with body", method: http.MethodDelete, path: "/v1/responses/resp_gateway?provider=mock", body: `{}`, want: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &mockProvider{}
			srv := New(provider, nil)

			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.want, rec.Body.String())
			}
		})
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

func TestIsUnsupportedNativeResponseErrorUsesCode(t *testing.T) {
	if !isUnsupportedNativeResponseError(unsupportedResponseOperation("response compaction is not supported")) {
		t.Fatal("unsupportedResponseOperation should be recognized")
	}
	messageOnly := core.NewInvalidRequestErrorWithStatus(http.StatusNotImplemented, "response compaction is not supported", nil)
	if isUnsupportedNativeResponseError(messageOnly) {
		t.Fatal("message-only unsupported error should not be recognized")
	}
}

func TestPaginateStoredResponseInputItemsSelectsOrderedWindow(t *testing.T) {
	items := []json.RawMessage{
		json.RawMessage(`{"id":"item_1"}`),
		json.RawMessage(`{"id":"item_2"}`),
		json.RawMessage(`{"id":"item_3"}`),
		json.RawMessage(`{"id":"item_4"}`),
		json.RawMessage(`{"id":"item_5"}`),
	}

	resp := paginateStoredResponseInputItems(items, core.ResponseInputItemsParams{
		Order: "desc",
		After: "item_4",
		Limit: 2,
	})

	if !resp.HasMore {
		t.Fatal("HasMore = false, want true")
	}
	if resp.FirstID != "item_3" || resp.LastID != "item_2" {
		t.Fatalf("first/last = %q/%q, want item_3/item_2", resp.FirstID, resp.LastID)
	}
	if got := responseInputItemID(resp.Data[0]); got != "item_3" {
		t.Fatalf("data[0] id = %q, want item_3", got)
	}
	if got := responseInputItemID(resp.Data[1]); got != "item_2" {
		t.Fatalf("data[1] id = %q, want item_2", got)
	}

	items[2][len(`{"id":"item_`)] = 'x'
	if len(resp.Data) != 2 || responseInputItemID(resp.Data[0]) != "item_3" {
		t.Fatal("paginated data should be cloned from the source items")
	}
}
