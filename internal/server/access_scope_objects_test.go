package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	batchstore "github.com/enterpilot/gomodel/internal/batch"
	"github.com/enterpilot/gomodel/internal/conversationstore"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/filestore"
	"github.com/enterpilot/gomodel/internal/responsestore"
)

// Bearer tokens accepted by scopedObjectServer and the scope each resolves to.
const (
	scopeTokenMaster = "master-key"
	scopeTokenAlpha  = "sk_gom_alpha"  // /team/alpha
	scopeTokenBeta   = "sk_gom_beta"   // /team/beta
	scopeTokenGlobal = "sk_gom_global" // managed key without a user path
)

func scopedObjectServer(provider *mockProvider, cfg *Config) *Server {
	if cfg == nil {
		cfg = &Config{}
	}
	cfg.MasterKey = scopeTokenMaster
	cfg.Authenticator = mockAuthenticator{
		enabled: true,
		tokenToID: map[string]string{
			scopeTokenAlpha:  "key-alpha",
			scopeTokenBeta:   "key-beta",
			scopeTokenGlobal: "key-global",
		},
		tokenPath: map[string]string{
			scopeTokenAlpha: "/team/alpha",
			scopeTokenBeta:  "/team/beta",
		},
	}
	return New(provider, cfg)
}

func scopedRequest(t *testing.T, srv *Server, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestResponsesLifecycle_ScopedCredentialCannotAddressForeignResponse(t *testing.T) {
	newStore := func(t *testing.T) responsestore.Store {
		t.Helper()
		store := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
		for id, userPath := range map[string]string{
			"resp_alpha":  "/team/alpha",
			"resp_child":  "/team/alpha/service",
			"resp_beta":   "/team/beta",
			"resp_legacy": "",
		} {
			require.NoError(t, store.Create(context.Background(), &responsestore.StoredResponse{
				Response: &core.ResponsesResponse{ID: id, Object: "response", Provider: "mock", Status: "completed"},
				Provider: "mock",
				UserPath: userPath,
			}))
		}
		return store
	}

	tests := []struct {
		name  string
		token string
		id    string
		want  int
	}{
		{name: "own response", token: scopeTokenAlpha, id: "resp_alpha", want: http.StatusOK},
		{name: "descendant response", token: scopeTokenAlpha, id: "resp_child", want: http.StatusOK},
		{name: "sibling tenant hidden", token: scopeTokenAlpha, id: "resp_beta", want: http.StatusNotFound},
		{name: "legacy row hidden from scoped", token: scopeTokenAlpha, id: "resp_legacy", want: http.StatusNotFound},
		{name: "master key sees tenant row", token: scopeTokenMaster, id: "resp_beta", want: http.StatusOK},
		{name: "master key sees legacy row", token: scopeTokenMaster, id: "resp_legacy", want: http.StatusOK},
		{name: "unscoped managed key sees tenant row", token: scopeTokenGlobal, id: "resp_beta", want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore(t)
			provider := &mockProvider{providerTypes: map[string]string{"gpt-5-mini": "mock"}}
			srv := scopedObjectServer(provider, &Config{ResponseStore: store})

			for _, op := range []struct {
				method string
				path   string
			}{
				{http.MethodGet, "/v1/responses/" + tt.id},
				{http.MethodGet, "/v1/responses/" + tt.id + "/input_items"},
			} {
				rec := scopedRequest(t, srv, op.method, op.path, tt.token, "")
				assert.Equal(t, tt.want, rec.Code, "%s %s: %s", op.method, op.path, rec.Body.String())
			}
			// A hidden tracked response must never fall through to the provider,
			// which would hand the same object back by ID.
			assert.Empty(t, provider.responseGetCalls, "provider lookup must not run for a tracked response")
			assert.Empty(t, provider.responseInputItemsCalls)

			rec := scopedRequest(t, srv, http.MethodDelete, "/v1/responses/"+tt.id, tt.token, "")
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
			if tt.want == http.StatusNotFound {
				assert.Empty(t, provider.responseDeleteCalls)
				_, err := store.Get(context.Background(), tt.id)
				assert.NoError(t, err, "hidden response must not be deleted")
			}
		})
	}
}

func TestConversations_ScopedCredentialCannotAddressForeignConversation(t *testing.T) {
	newStore := func(t *testing.T) conversationstore.Store {
		t.Helper()
		store := conversationstore.NewMemoryStore()
		for id, userPath := range map[string]string{
			"conv_alpha":  "/team/alpha",
			"conv_child":  "/team/alpha/service",
			"conv_beta":   "/team/beta",
			"conv_legacy": "",
		} {
			require.NoError(t, store.Create(context.Background(), &conversationstore.StoredConversation{
				Conversation: &core.Conversation{ID: id, Object: core.ConversationObject, Metadata: map[string]string{}},
				Items:        []json.RawMessage{json.RawMessage(`{"id":"msg_1","type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`)},
				UserPath:     userPath,
			}))
		}
		return store
	}

	tests := []struct {
		name  string
		token string
		id    string
		want  int
	}{
		{name: "own conversation", token: scopeTokenAlpha, id: "conv_alpha", want: http.StatusOK},
		{name: "descendant conversation", token: scopeTokenAlpha, id: "conv_child", want: http.StatusOK},
		{name: "sibling tenant hidden", token: scopeTokenAlpha, id: "conv_beta", want: http.StatusNotFound},
		{name: "legacy row hidden from scoped", token: scopeTokenAlpha, id: "conv_legacy", want: http.StatusNotFound},
		{name: "master key sees tenant row", token: scopeTokenMaster, id: "conv_beta", want: http.StatusOK},
		{name: "master key sees legacy row", token: scopeTokenMaster, id: "conv_legacy", want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore(t)
			srv := scopedObjectServer(&mockProvider{}, &Config{ConversationStore: store})

			ops := []struct {
				method string
				path   string
				body   string
			}{
				{http.MethodGet, "/v1/conversations/" + tt.id, ""},
				{http.MethodPost, "/v1/conversations/" + tt.id, `{"metadata":{"k":"v"}}`},
				{http.MethodGet, "/v1/conversations/" + tt.id + "/items", ""},
				{http.MethodPost, "/v1/conversations/" + tt.id + "/items", `{"items":[{"type":"message","role":"user","content":"more"}]}`},
				{http.MethodGet, "/v1/conversations/" + tt.id + "/items/msg_1", ""},
				{http.MethodDelete, "/v1/conversations/" + tt.id + "/items/msg_1", ""},
				{http.MethodDelete, "/v1/conversations/" + tt.id, ""},
			}
			for _, op := range ops {
				rec := scopedRequest(t, srv, op.method, op.path, tt.token, op.body)
				assert.Equal(t, tt.want, rec.Code, "%s %s: %s", op.method, op.path, rec.Body.String())
			}
			if tt.want == http.StatusNotFound {
				stored, err := store.Get(context.Background(), tt.id)
				require.NoError(t, err, "hidden conversation must survive foreign mutations")
				assert.Len(t, stored.Items, 1, "hidden conversation items must be untouched")
				assert.Empty(t, stored.Conversation.Metadata, "hidden conversation metadata must be untouched")
			}
		})
	}
}

func TestResponses_ConversationReferenceOutsideScopeIsNotFound(t *testing.T) {
	store := conversationstore.NewMemoryStore()
	require.NoError(t, store.Create(context.Background(), &conversationstore.StoredConversation{
		Conversation: &core.Conversation{ID: "conv_beta", Object: core.ConversationObject},
		UserPath:     "/team/beta",
	}))
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes:   map[string]string{"gpt-5-mini": "mock"},
		responsesResponse: &core.ResponsesResponse{
			ID: "resp_1", Object: "response", Model: "gpt-5-mini", Status: "completed",
		},
	}
	srv := scopedObjectServer(provider, &Config{ConversationStore: store})

	body := `{"model":"gpt-5-mini","input":"hello","conversation":"conv_beta"}`
	rec := scopedRequest(t, srv, http.MethodPost, "/v1/responses", scopeTokenAlpha, body)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	rec = scopedRequest(t, srv, http.MethodPost, "/v1/responses", scopeTokenBeta, body)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func seedBatch(t *testing.T, store batchstore.Store, id, userPath string, createdAt int64) {
	t.Helper()
	require.NoError(t, store.Create(context.Background(), &batchstore.StoredBatch{
		Batch:    &core.BatchResponse{ID: id, Object: "batch", Status: "completed", CreatedAt: createdAt},
		UserPath: userPath,
	}))
}

func TestBatches_ScopedCredentialCannotAddressForeignBatch(t *testing.T) {
	store := batchstore.NewMemoryStore()
	seedBatch(t, store, "batch_alpha", "/team/alpha", 10)
	seedBatch(t, store, "batch_child", "/team/alpha/service", 11)
	seedBatch(t, store, "batch_beta", "/team/beta", 12)
	seedBatch(t, store, "batch_legacy", "", 13)
	srv := scopedObjectServer(&mockProvider{}, &Config{BatchStore: store})

	tests := []struct {
		name  string
		token string
		id    string
		want  int
	}{
		{name: "own batch", token: scopeTokenAlpha, id: "batch_alpha", want: http.StatusOK},
		{name: "descendant batch", token: scopeTokenAlpha, id: "batch_child", want: http.StatusOK},
		{name: "sibling tenant hidden", token: scopeTokenAlpha, id: "batch_beta", want: http.StatusNotFound},
		{name: "legacy row hidden from scoped", token: scopeTokenAlpha, id: "batch_legacy", want: http.StatusNotFound},
		{name: "master key sees tenant row", token: scopeTokenMaster, id: "batch_beta", want: http.StatusOK},
		{name: "master key sees legacy row", token: scopeTokenMaster, id: "batch_legacy", want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, path := range []string{"/v1/batches/" + tt.id, "/v1/batches/" + tt.id + "/results"} {
				rec := scopedRequest(t, srv, http.MethodGet, path, tt.token, "")
				assert.Equal(t, tt.want, rec.Code, "%s: %s", path, rec.Body.String())
			}
			if tt.want == http.StatusNotFound {
				rec := scopedRequest(t, srv, http.MethodPost, "/v1/batches/"+tt.id+"/cancel", tt.token, "")
				assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestBatches_ListPagesWithinScope(t *testing.T) {
	store := batchstore.NewMemoryStore()
	// Newest first in store order: five beta batches sit in front of the
	// three alpha batches, so a scoped page must read past them.
	for i := range 5 {
		seedBatch(t, store, "batch_beta_"+string(rune('a'+i)), "/team/beta", int64(100+i))
	}
	seedBatch(t, store, "batch_alpha_1", "/team/alpha", 3)
	seedBatch(t, store, "batch_alpha_2", "/team/alpha/service", 2)
	seedBatch(t, store, "batch_alpha_3", "/team/alpha", 1)
	seedBatch(t, store, "batch_legacy", "", 0)
	srv := scopedObjectServer(&mockProvider{}, &Config{BatchStore: store})

	listIDs := func(t *testing.T, token, query string) ([]string, bool, int) {
		t.Helper()
		rec := scopedRequest(t, srv, http.MethodGet, "/v1/batches"+query, token, "")
		if rec.Code != http.StatusOK {
			return nil, false, rec.Code
		}
		var resp core.BatchListResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		ids := make([]string, 0, len(resp.Data))
		for _, item := range resp.Data {
			ids = append(ids, item.ID)
		}
		return ids, resp.HasMore, rec.Code
	}

	ids, hasMore, _ := listIDs(t, scopeTokenAlpha, "?limit=2")
	assert.Equal(t, []string{"batch_alpha_1", "batch_alpha_2"}, ids)
	assert.True(t, hasMore)

	ids, hasMore, _ = listIDs(t, scopeTokenAlpha, "?limit=2&after=batch_alpha_2")
	assert.Equal(t, []string{"batch_alpha_3"}, ids)
	assert.False(t, hasMore)

	_, _, status := listIDs(t, scopeTokenAlpha, "?limit=2&after=batch_beta_a")
	assert.Equal(t, http.StatusNotFound, status, "a foreign cursor must look unknown")

	ids, hasMore, _ = listIDs(t, scopeTokenMaster, "?limit=100")
	assert.Len(t, ids, 9, "global scope lists every row including the legacy one")
	assert.False(t, hasMore)
}

func TestFiles_ScopedCredentialCannotAddressForeignFile(t *testing.T) {
	fileStore := filestore.NewMemoryStore()
	for id, userPath := range map[string]string{
		"file_alpha":  "/team/alpha",
		"file_child":  "/team/alpha/service",
		"file_beta":   "/team/beta",
		"file_legacy": "",
	} {
		require.NoError(t, fileStore.Upsert(context.Background(), &filestore.StoredFile{ID: id, ProviderType: "openai", UserPath: userPath}))
	}

	tests := []struct {
		name  string
		token string
		id    string
		want  int
	}{
		{name: "own file", token: scopeTokenAlpha, id: "file_alpha", want: http.StatusOK},
		{name: "descendant file", token: scopeTokenAlpha, id: "file_child", want: http.StatusOK},
		{name: "sibling tenant hidden", token: scopeTokenAlpha, id: "file_beta", want: http.StatusNotFound},
		{name: "legacy mapping hidden from scoped", token: scopeTokenAlpha, id: "file_legacy", want: http.StatusNotFound},
		{name: "untracked file keeps provider fallback", token: scopeTokenAlpha, id: "file_untracked", want: http.StatusOK},
		{name: "master key sees tenant file", token: scopeTokenMaster, id: "file_beta", want: http.StatusOK},
		{name: "master key sees legacy mapping", token: scopeTokenMaster, id: "file_legacy", want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &mockProvider{providerTypes: map[string]string{"gpt-4o-mini": "openai"}}
			srv := scopedObjectServer(provider, &Config{FileStore: fileStore})

			for _, path := range []string{
				"/v1/files/" + tt.id,
				"/v1/files/" + tt.id + "?provider=openai",
				"/v1/files/" + tt.id + "/content",
			} {
				rec := scopedRequest(t, srv, http.MethodGet, path, tt.token, "")
				assert.Equal(t, tt.want, rec.Code, "%s: %s", path, rec.Body.String())
			}

			rec := scopedRequest(t, srv, http.MethodDelete, "/v1/files/"+tt.id, tt.token, "")
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
			if tt.want == http.StatusNotFound {
				assert.Empty(t, provider.capturedFileDeleteIDs, "hidden file must not reach the provider")
				_, err := fileStore.Get(context.Background(), tt.id)
				assert.NoError(t, err, "hidden mapping must not be deleted")
			}
		})
	}
}

func fileObject(id string, createdAt int64) core.FileObject {
	return core.FileObject{ID: id, Object: "file", Bytes: 10, CreatedAt: createdAt, Filename: id + ".jsonl", Purpose: "batch", Provider: "openai"}
}

func TestFiles_ListFiltersProviderListingThroughOwnedMappings(t *testing.T) {
	fileStore := filestore.NewMemoryStore()
	for id, userPath := range map[string]string{
		"file_alpha_1": "/team/alpha",
		"file_alpha_2": "/team/alpha/service",
		"file_alpha_3": "/team/alpha",
		"file_beta":    "/team/beta",
	} {
		require.NoError(t, fileStore.Upsert(context.Background(), &filestore.StoredFile{ID: id, ProviderType: "openai", UserPath: userPath}))
	}
	// Two provider pages; the first holds only one alpha file, so a scoped
	// page of two must read on to the second page.
	pages := map[string]*core.FileListResponse{
		"": {Object: "list", HasMore: true, Data: []core.FileObject{
			fileObject("file_beta", 50), fileObject("file_untracked", 40), fileObject("file_alpha_1", 30),
		}},
		"file_alpha_1": {Object: "list", HasMore: false, Data: []core.FileObject{
			fileObject("file_alpha_2", 20), fileObject("file_alpha_3", 10),
		}},
	}
	provider := &mockProvider{
		providerTypes:           map[string]string{"gpt-4o-mini": "openai"},
		fileListPagesByProvider: map[string]map[string]*core.FileListResponse{"openai": pages},
	}
	srv := scopedObjectServer(provider, &Config{FileStore: fileStore})

	listIDs := func(t *testing.T, token, query string) ([]string, bool) {
		t.Helper()
		rec := scopedRequest(t, srv, http.MethodGet, "/v1/files"+query, token, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp core.FileListResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		ids := make([]string, 0, len(resp.Data))
		for _, item := range resp.Data {
			ids = append(ids, item.ID)
		}
		return ids, resp.HasMore
	}

	for _, query := range []string{"?limit=2&provider=openai", "?limit=2"} {
		t.Run("scoped "+query, func(t *testing.T) {
			ids, hasMore := listIDs(t, scopeTokenAlpha, query)
			assert.Equal(t, []string{"file_alpha_1", "file_alpha_2"}, ids)
			assert.True(t, hasMore)
		})
	}

	ids, hasMore := listIDs(t, scopeTokenAlpha, "?limit=5&provider=openai")
	assert.Equal(t, []string{"file_alpha_1", "file_alpha_2", "file_alpha_3"}, ids)
	assert.False(t, hasMore)

	ids, hasMore = listIDs(t, scopeTokenMaster, "?limit=5&provider=openai")
	assert.Equal(t, []string{"file_beta", "file_untracked", "file_alpha_1"}, ids, "global scope gets the raw provider page")
	assert.True(t, hasMore)
}

func TestFiles_ScopedListWithoutStoreIsEmpty(t *testing.T) {
	provider := &mockProvider{providerTypes: map[string]string{"gpt-4o-mini": "openai"}}
	srv := scopedObjectServer(provider, nil)

	rec := scopedRequest(t, srv, http.MethodGet, "/v1/files?provider=openai", scopeTokenAlpha, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp core.FileListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Empty(t, resp.Data)
	assert.False(t, resp.HasMore)
}

// outageFileStore reports a lookup outage so ownership cannot be established.
type outageFileStore struct {
	filestore.Store
}

func (outageFileStore) Get(context.Context, string) (*filestore.StoredFile, error) {
	return nil, errors.New("file store unavailable")
}

// TestFiles_LookupOutageFailsClosedForScopedCredential pins that a scoped
// caller never falls through to the tenancy-blind provider lookup when the
// ownership record cannot be read, while global callers keep the fallback.
func TestFiles_LookupOutageFailsClosedForScopedCredential(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  int
	}{
		{name: "scoped caller fails closed", token: scopeTokenAlpha, want: http.StatusInternalServerError},
		{name: "master key keeps provider fallback", token: scopeTokenMaster, want: http.StatusOK},
		{name: "unscoped key keeps provider fallback", token: scopeTokenGlobal, want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &mockProvider{providerTypes: map[string]string{"gpt-4o-mini": "openai"}}
			srv := scopedObjectServer(provider, &Config{FileStore: outageFileStore{Store: filestore.NewMemoryStore()}})
			rec := scopedRequest(t, srv, http.MethodGet, "/v1/files/file_any", tt.token, "")
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
		})
	}
}
