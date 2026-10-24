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
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/responsestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Empty(t, provider.capturedResponseUtilityReqs)
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

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	resp := echotest.Decode[core.ResponsesResponse](t, rec)
	require.Equal(t, "resp_gateway", resp.ID)
	require.Equal(t, "response", resp.Object)
	require.Equal(t, "mock", resp.Provider)
}

func TestCancelStoredResponseNormalizesPersistedResponse(t *testing.T) {
	store := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
	err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response:           &core.ResponsesResponse{ID: "resp_gateway", Object: "response", Provider: "mock"},
		Provider:           "mock",
		ProviderResponseID: "provider_resp",
	})
	require.NoError(t, err)

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

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	resp := echotest.Decode[core.ResponsesResponse](t, rec)
	require.Equal(t, "resp_gateway", resp.ID)
	require.Equal(t, "response", resp.Object)
	require.Equal(t, "mock", resp.Provider)

	stored, err := store.Get(context.Background(), "resp_gateway")
	require.NoError(t, err)
	require.Equal(t, "resp_gateway", stored.Response.ID)
	require.Equal(t, "response", stored.Response.Object)
	require.Equal(t, "mock", stored.Response.Provider)
}

func TestGetStoredResponseRefreshesNonTerminalSnapshot(t *testing.T) {
	store := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
	err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response:           &core.ResponsesResponse{ID: "resp_gateway", Object: "response", Provider: "mock", Status: "queued"},
		Provider:           "mock",
		ProviderResponseID: "provider_resp",
	})
	require.NoError(t, err)

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

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	resp := echotest.Decode[core.ResponsesResponse](t, rec)
	require.Equal(t, "completed", resp.Status)
	require.Equal(t, "resp_gateway", resp.ID)
	require.Equal(t, "response", resp.Object)
	require.Equal(t, "mock", resp.Provider)
	require.Len(t, provider.responseGetCalls, 1)
	require.Equal(t, "provider_resp", provider.responseGetCalls[0].id)

	stored, err := store.Get(context.Background(), "resp_gateway")
	require.NoError(t, err)
	require.Equal(t, "completed", stored.Response.Status)
	require.Equal(t, "resp_gateway", stored.Response.ID)
}

func TestGetStoredResponseKeepsTerminalSnapshot(t *testing.T) {
	store := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
	err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response:           &core.ResponsesResponse{ID: "resp_gateway", Object: "response", Provider: "mock", Status: "completed"},
		Provider:           "mock",
		ProviderResponseID: "provider_resp",
	})
	require.NoError(t, err)

	provider := &mockProvider{}
	srv := New(provider, &Config{ResponseStore: store})

	req := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_gateway", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, provider.responseGetCalls)
}

func TestGetStoredResponseKeepsConcurrentCancellation(t *testing.T) {
	store := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
	err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response:           &core.ResponsesResponse{ID: "resp_gateway", Object: "response", Provider: "mock", Status: "queued"},
		Provider:           "mock",
		ProviderResponseID: "provider_resp",
	})
	require.NoError(t, err)

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
		assert.NoError(t, updateErr)
	}
	srv := New(provider, &Config{ResponseStore: store})

	req := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_gateway", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	resp := echotest.Decode[core.ResponsesResponse](t, rec)
	require.Equal(t, "cancelled", resp.Status)

	stored, err := store.Get(context.Background(), "resp_gateway")
	require.NoError(t, err)
	require.Equal(t, "cancelled", stored.Response.Status)
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
			require.NoError(t, err)

			provider := &mockProvider{responseLifecycleErr: tt.lifecycleErr}
			srv := New(provider, &Config{ResponseStore: store})

			req := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_gateway", nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			resp := echotest.Decode[core.ResponsesResponse](t, rec)
			require.Equal(t, "in_progress", resp.Status)
			require.Equal(t, "resp_gateway", resp.ID)

			stored, err := store.Get(context.Background(), "resp_gateway")
			require.NoError(t, err)
			require.Equal(t, "in_progress", stored.Response.Status)
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

			require.Equal(t, tt.want, rec.Code, rec.Body.String())
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
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusRequestTimeout, gatewayErr.HTTPStatusCode())
}

func TestIsUnsupportedNativeResponseErrorUsesCode(t *testing.T) {
	require.True(t, isUnsupportedNativeResponseError(unsupportedResponseOperation("response compaction is not supported")))

	messageOnly := core.NewInvalidRequestErrorWithStatus(http.StatusNotImplemented, "response compaction is not supported", nil)
	require.False(t, isUnsupportedNativeResponseError(messageOnly))
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

	require.True(t, resp.HasMore)
	require.Equal(t, "item_3", resp.FirstID)
	require.Equal(t, "item_2", resp.LastID)
	require.Equal(t, "item_3", responseInputItemID(resp.Data[0]))
	require.Equal(t, "item_2", responseInputItemID(resp.Data[1]))

	items[2][len(`{"id":"item_`)] = 'x'
	require.Len(t, resp.Data, 2)
	require.Equal(t, "item_3", responseInputItemID(resp.Data[0]))
}
