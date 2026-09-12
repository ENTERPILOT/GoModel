package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

func retrieveModelCatalog() *core.ModelsResponse {
	return &core.ModelsResponse{
		Object: "list",
		Data: []core.Model{
			{
				ID:       "openai/gpt-4.1-mini",
				Object:   "model",
				Created:  1721172741,
				OwnedBy:  "openai",
				Metadata: &core.ModelMetadata{DisplayName: "GPT-4.1 mini"},
			},
			{ID: "groq/openai/gpt-oss-20b", Object: "model", Created: 1712361441, OwnedBy: "groq"},
			{
				ID:      "fireworks/accounts/fireworks/models/glm-5p3-flash",
				Object:  "model",
				Created: 1712361441,
				OwnedBy: "fireworks",
			},
		},
	}
}

func TestRetrieveModel(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantID     string
		wantBody   []string
	}{
		{
			name:       "plain provider-qualified id",
			path:       "/v1/models/openai/gpt-4.1-mini",
			wantStatus: http.StatusOK,
			wantID:     "openai/gpt-4.1-mini",
			wantBody:   []string{`"object":"model"`, `"owned_by":"openai"`, `"display_name":"GPT-4.1 mini"`},
		},
		{
			name:       "id with two slashes",
			path:       "/v1/models/groq/openai/gpt-oss-20b",
			wantStatus: http.StatusOK,
			wantID:     "groq/openai/gpt-oss-20b",
		},
		{
			name:       "long provider id",
			path:       "/v1/models/fireworks/accounts/fireworks/models/glm-5p3-flash",
			wantStatus: http.StatusOK,
			wantID:     "fireworks/accounts/fireworks/models/glm-5p3-flash",
		},
		{
			name:       "percent-encoded slashes",
			path:       "/v1/models/openai%2Fgpt-4.1-mini",
			wantStatus: http.StatusOK,
			wantID:     "openai/gpt-4.1-mini",
		},
		{
			name:       "unknown id",
			path:       "/v1/models/openai/does-not-exist",
			wantStatus: http.StatusNotFound,
			wantBody:   []string{`"code":"model_not_found"`, "openai/does-not-exist"},
		},
		{
			name:       "empty id falls back to the list route",
			path:       "/v1/models/",
			wantStatus: http.StatusOK,
			wantBody:   []string{`"object":"list"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(&mockProvider{modelsResponse: retrieveModelCatalog()}, &Config{})

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantID != "" {
				var model core.Model
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &model))
				require.Equal(t, tt.wantID, model.ID)
				require.Equal(t, "model", model.Object)
			}
			for _, want := range tt.wantBody {
				require.Contains(t, rec.Body.String(), want)
			}
		})
	}
}

func TestRetrieveModel_MatchesListEntry(t *testing.T) {
	srv := New(&mockProvider{modelsResponse: retrieveModelCatalog()}, &Config{})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	var list core.ModelsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.NotEmpty(t, list.Data)

	for _, listed := range list.Data {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/"+listed.ID, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var retrieved core.Model
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &retrieved))
		require.Equal(t, listed, retrieved)
	}
}

func TestRetrieveModel_HiddenModelsReturn404(t *testing.T) {
	mock := &mockProvider{
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "openai/gpt-4.1-mini", Object: "model", OwnedBy: "openai"}},
		},
	}
	authorizer := &userPathModelAuthorizer{allowedUnder: map[string]string{"/acme/eng": "anthropic"}}
	srv := New(mock, &Config{
		ModelAuthorizer: authorizer,
		ExposedModelLister: staticExposedModelLister{
			models: []core.Model{{ID: "anthropic/claude-haiku-4-5", Object: "model", OwnedBy: "anthropic"}},
		},
	})

	get := func(id, userPath string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/models/"+id, nil)
		if userPath != "" {
			req.Header.Set(core.UserPathHeader, userPath)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	require.Equal(t, http.StatusOK, get("openai/gpt-4.1-mini", ""))
	// Under /acme/eng only anthropic models are visible, so the OpenAI model
	// must 404 rather than leak its metadata.
	require.Equal(t, http.StatusNotFound, get("openai/gpt-4.1-mini", "/acme/eng/alice"))
	require.Equal(t, http.StatusOK, get("anthropic/claude-haiku-4-5", "/acme/eng/alice"))
}

func TestRetrieveModel_VirtualModels(t *testing.T) {
	catalog := &aliasesTestCatalog{
		supported:     map[string]bool{"openai/gpt-4o": true},
		providerTypes: map[string]string{"openai/gpt-4o": "openai"},
		models:        map[string]core.Model{"openai/gpt-4o": {ID: "gpt-4o", Object: "model", OwnedBy: "openai"}},
	}
	service, err := virtualmodels.NewService(newAliasesTestStore(
		redirectVM("smart", "gpt-4o", "openai", true),
	), catalog, true)
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))

	mock := &mockProvider{
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "gpt-4o", Object: "model", OwnedBy: "openai"}},
		},
	}
	srv := New(mock, &Config{
		ExposedModelLister:              service,
		KeepOnlyAliasesAtModelsEndpoint: true,
	})

	get := func(id string) int {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/"+id, nil))
		return rec.Code
	}

	require.Equal(t, http.StatusOK, get("smart"))
	// keep_only_aliases_at_models_endpoint hides concrete provider models from
	// the list, so retrieve must hide them too.
	require.Equal(t, http.StatusNotFound, get("gpt-4o"))
}

func TestRetrieveModel_AnthropicDialect(t *testing.T) {
	srv := New(&mockProvider{modelsResponse: retrieveModelCatalog()}, &Config{})

	req := httptest.NewRequest(http.MethodGet, "/v1/models/openai/gpt-4.1-mini", nil)
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	require.Contains(t, body, `"type":"model"`)
	require.Contains(t, body, `"id":"openai/gpt-4.1-mini"`)
	require.Contains(t, body, `"display_name":"GPT-4.1 mini"`)

	req = httptest.NewRequest(http.MethodGet, "/v1/models/openai/nope", nil)
	req.Header.Set("anthropic-version", "2023-06-01")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), `"type":"error"`)
}

func TestRetrieveModel_ProviderErrorIsReported(t *testing.T) {
	srv := New(&mockProvider{err: context.DeadlineExceeded}, &Config{})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/openai/gpt-4.1-mini", nil))

	require.NotEqual(t, http.StatusNotFound, rec.Code)
	require.GreaterOrEqual(t, rec.Code, http.StatusInternalServerError)
}
