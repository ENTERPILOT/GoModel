package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/responsecache"
)

// These run through the whole middleware chain, so they prove the partition
// reads the path authentication settled on rather than the raw header.
func TestResponseCachePartition_UsesPathBoundToTheKey(t *testing.T) {
	const (
		keyA       = "sk-team-a"
		keyB       = "sk-team-b"
		keyUnbound = "sk-unbound"
		masterKey  = "sk-master"
		pathA      = "/team/a"
		pathB      = "/team/b"
	)
	authenticator := mockAuthenticator{
		enabled:   true,
		tokenToID: map[string]string{keyA: "a", keyB: "b", keyUnbound: "u"},
		tokenPath: map[string]string{keyA: pathA, keyB: pathB},
	}
	tests := []struct {
		name    string
		token   string
		header  string
		wantHit bool
	}{
		{name: "bound key reuses its own entry", token: keyA, wantHit: true},
		{name: "bound key cannot leave its partition through the header", token: keyA, header: pathB, wantHit: true},
		{name: "another bound key misses", token: keyB},
		{name: "another bound key cannot reach it through the header", token: keyB, header: pathA},
		{name: "unbound key without a header misses", token: keyUnbound},
		{name: "master key without a header misses", token: masterKey},
		{name: "known limitation: unbound key chooses the path", token: keyUnbound, header: pathA, wantHit: true},
		{name: "known limitation: master key chooses the path", token: masterKey, header: pathA, wantHit: true},
	}
	bodies := []struct {
		name   string
		stream bool
		body   string
	}{
		{name: "non-streaming", body: `{"model":"gpt-4o","messages":[{"role":"user","content":"partition"}]}`},
		{name: "streaming", stream: true, body: `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"partition"}]}`},
	}
	for _, variant := range bodies {
		for _, tt := range tests {
			t.Run(variant.name+"/"+tt.name, func(t *testing.T) {
				store := cache.NewMapStore()
				t.Cleanup(func() { _ = store.Close() })
				mw := responsecache.NewResponseCacheMiddlewareWithStoreAndUserPathPartition(store, time.Hour)
				provider := &mockProvider{
					supportedModels: []string{"gpt-4o"},
					providerTypes:   map[string]string{"gpt-4o": "openai"},
					streamData:      "data: {\"id\":\"chunk\"}\n\ndata: [DONE]\n\n",
					response: &core.ChatResponse{
						ID: "chatcmpl-test", Object: "chat.completion", Model: "gpt-4o",
						Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "ok"}}},
					},
				}
				srv := New(provider, &Config{MasterKey: masterKey, Authenticator: authenticator, ResponseCacheMiddleware: mw})

				send := func(token, header string) *httptest.ResponseRecorder {
					req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(variant.body))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Authorization", "Bearer "+token)
					if header != "" {
						req.Header.Set(core.UserPathHeader, header)
					}
					rec := httptest.NewRecorder()
					srv.ServeHTTP(rec, req)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					return rec
				}

				primed := send(keyA, "")
				require.Empty(t, primed.Header().Get("X-Cache"))
				require.NoError(t, mw.Close()) // flushes the async cache write

				rec := send(tt.token, tt.header)
				if tt.wantHit {
					assert.Equal(t, responsecache.CacheHeaderExact, rec.Header().Get("X-Cache"))
					return
				}
				assert.Empty(t, rec.Header().Get("X-Cache"))
			})
		}
	}
}

// With the setting off the same requests share one entry, whatever the key.
func TestResponseCachePartition_OffSharesAcrossBoundKeys(t *testing.T) {
	store := cache.NewMapStore()
	t.Cleanup(func() { _ = store.Close() })
	mw := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	provider := &mockProvider{
		supportedModels: []string{"gpt-4o"},
		providerTypes:   map[string]string{"gpt-4o": "openai"},
		response: &core.ChatResponse{
			ID: "chatcmpl-test", Object: "chat.completion", Model: "gpt-4o",
			Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "ok"}}},
		},
	}
	srv := New(provider, &Config{
		Authenticator: mockAuthenticator{
			enabled:   true,
			tokenToID: map[string]string{"sk-a": "a", "sk-b": "b"},
			tokenPath: map[string]string{"sk-a": "/team/a", "sk-b": "/team/b"},
		},
		ResponseCacheMiddleware: mw,
	})
	send := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"partition"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec
	}

	send("sk-a")
	require.NoError(t, mw.Close()) // flushes the async cache write

	assert.Equal(t, responsecache.CacheHeaderExact, send("sk-b").Header().Get("X-Cache"))
}
