package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/aliases"
	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

type explodingValidationReadCloser struct{}

var benchmarkSelectorValidationBody = []byte(`{
	"provider":"openai",
	"model":"gpt-4o-mini",
	"messages":[{"role":"user","content":"hi"}],
	"response_format":{"type":"json_schema"}
}`)

type modelCountingValidationProvider struct {
	*mockProvider
	modelCount int
}

type staticExecutionPolicyResolver struct {
	match func(core.ExecutionPlanSelector) (*core.ResolvedExecutionPolicy, error)
}

func (r *staticExecutionPolicyResolver) Match(selector core.ExecutionPlanSelector) (*core.ResolvedExecutionPolicy, error) {
	if r == nil || r.match == nil {
		return nil, nil
	}
	return r.match(selector)
}

func (explodingValidationReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("live request body should not be read")
}

func (explodingValidationReadCloser) Close() error {
	return nil
}

func (p *modelCountingValidationProvider) ModelCount() int {
	return p.modelCount
}

func TestModelValidation(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"gpt-4o-mini", "text-embedding-3-small", "openai/gpt-oss-120b"},
		providerTypes:   map[string]string{"groq/openai/gpt-oss-120b": "groq"},
	}

	tests := []struct {
		name           string
		method         string
		path           string
		body           string
		expectedStatus int
		expectedBody   string
		handlerCalled  bool
	}{
		{
			name:           "valid model on chat completions",
			method:         http.MethodPost,
			path:           "/v1/chat/completions",
			body:           `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "valid provider/model selector",
			method:         http.MethodPost,
			path:           "/v1/chat/completions",
			body:           `{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "valid model with provider field",
			method:         http.MethodPost,
			path:           "/v1/chat/completions",
			body:           `{"provider":"openai","model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "valid model on embeddings",
			method:         http.MethodPost,
			path:           "/v1/embeddings",
			body:           `{"model":"text-embedding-3-small","input":"hello"}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "valid model on responses",
			method:         http.MethodPost,
			path:           "/v1/responses",
			body:           `{"model":"gpt-4o-mini","input":"hello"}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "batch path skips root model validation",
			method:         http.MethodPost,
			path:           "/v1/batches",
			body:           `{"requests":[{"url":"/v1/chat/completions","body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}}]}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "files path skips root model validation",
			method:         http.MethodPost,
			path:           "/v1/files",
			body:           "",
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "missing model returns 400",
			method:         http.MethodPost,
			path:           "/v1/chat/completions",
			body:           `{"messages":[{"role":"user","content":"hi"}]}`,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "model is required",
			handlerCalled:  false,
		},
		{
			name:           "empty model returns 400",
			method:         http.MethodPost,
			path:           "/v1/embeddings",
			body:           `{"model":"","input":"hello"}`,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "model is required",
			handlerCalled:  false,
		},
		{
			name:           "unsupported model returns 400",
			method:         http.MethodPost,
			path:           "/v1/chat/completions",
			body:           `{"model":"unsupported-model","messages":[{"role":"user","content":"hi"}]}`,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "unsupported model",
			handlerCalled:  false,
		},
		{
			name:           "provider field keeps slash model raw",
			method:         http.MethodPost,
			path:           "/v1/chat/completions",
			body:           `{"provider":"groq","model":"openai/gpt-oss-120b","messages":[{"role":"user","content":"hi"}]}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "non-model path skips validation",
			method:         http.MethodGet,
			path:           "/v1/models",
			body:           "",
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "health path skips validation",
			method:         http.MethodGet,
			path:           "/health",
			body:           "",
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "invalid JSON passes through to handler",
			method:         http.MethodPost,
			path:           "/v1/chat/completions",
			body:           `{invalid}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			handlerCalled := false

			middleware := ExecutionPlanning(provider)
			handler := middleware(func(c *echo.Context) error {
				handlerCalled = true
				return c.String(http.StatusOK, "ok")
			})

			var body *strings.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			} else {
				body = strings.NewReader("")
			}

			req := httptest.NewRequest(tt.method, tt.path, body)
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := handler(c)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedStatus, rec.Code)
			assert.Equal(t, tt.handlerCalled, handlerCalled)

			if tt.expectedBody != "" {
				assert.Contains(t, rec.Body.String(), tt.expectedBody)
			}
		})
	}
}

func TestModelValidation_SetsProviderType(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var capturedProviderType string

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedProviderType = GetProviderType(c)
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, "mock", capturedProviderType)
}

func TestModelValidation_StoresExecutionPlan(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var capturedPlan *core.ExecutionPlan

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedPlan = core.GetExecutionPlan(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "plan-req-123")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	if assert.NotNil(t, capturedPlan) {
		assert.Equal(t, "plan-req-123", capturedPlan.RequestID)
		assert.Equal(t, core.ExecutionModeTranslated, capturedPlan.Mode)
		assert.Equal(t, "mock", capturedPlan.ProviderType)
		assert.True(t, capturedPlan.Capabilities.SemanticExtraction)
		assert.True(t, capturedPlan.Capabilities.AliasResolution)
		assert.True(t, capturedPlan.Capabilities.ResponseCaching)
		if assert.NotNil(t, capturedPlan.Resolution) {
			assert.Equal(t, "gpt-4o-mini", capturedPlan.Resolution.Requested.Model)
			assert.Equal(t, "gpt-4o-mini", capturedPlan.Resolution.ResolvedSelector.Model)
		}
	}
}

func TestModelValidation_StoresMatchedExecutionPolicy(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var capturedPlan *core.ExecutionPlan

	policyResolver := &staticExecutionPolicyResolver{
		match: func(selector core.ExecutionPlanSelector) (*core.ResolvedExecutionPolicy, error) {
			if selector.Provider == "mock" && selector.Model == "gpt-4o-mini" {
				return &core.ResolvedExecutionPolicy{
					VersionID:      "plan-openai-gpt-4o-mini-v3",
					Version:        3,
					ScopeProvider:  "mock",
					ScopeModel:     "gpt-4o-mini",
					Name:           "provider-model",
					PlanHash:       "hash-123",
					Features:       core.DefaultExecutionFeatures(),
					GuardrailsHash: "guardrails-123",
				}, nil
			}
			return &core.ResolvedExecutionPolicy{
				VersionID: "plan-global-v1",
				Version:   1,
				Name:      "global",
				Features:  core.DefaultExecutionFeatures(),
			}, nil
		},
	}

	middleware := ExecutionPlanningWithResolverAndPolicy(provider, nil, policyResolver)
	handler := middleware(func(c *echo.Context) error {
		capturedPlan = core.GetExecutionPlan(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	if assert.NotNil(t, capturedPlan) && assert.NotNil(t, capturedPlan.Policy) {
		assert.Equal(t, "plan-openai-gpt-4o-mini-v3", capturedPlan.Policy.VersionID)
		assert.Equal(t, "guardrails-123", capturedPlan.Policy.GuardrailsHash)
	}
}

func TestModelValidation_PassesUserPathToExecutionPolicyResolver(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var capturedSelector core.ExecutionPlanSelector

	policyResolver := &staticExecutionPolicyResolver{
		match: func(selector core.ExecutionPlanSelector) (*core.ResolvedExecutionPolicy, error) {
			capturedSelector = selector
			return &core.ResolvedExecutionPolicy{
				VersionID: "plan-global-v1",
				Version:   1,
				Name:      "global",
				Features:  core.DefaultExecutionFeatures(),
			}, nil
		},
	}

	middleware := RequestSnapshotCapture()
	handler := middleware(ExecutionPlanningWithResolverAndPolicy(provider, nil, policyResolver)(func(c *echo.Context) error {
		return c.String(http.StatusOK, "ok")
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(core.UserPathHeader, "/team/a/user")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, "mock", capturedSelector.Provider)
	assert.Equal(t, "gpt-4o-mini", capturedSelector.Model)
	assert.Equal(t, "/team/a/user", capturedSelector.UserPath)
}

func TestExecutionPlanning_StoresPassthroughRouteInfo(t *testing.T) {
	provider := &mockProvider{}

	e := echo.New()
	var capturedPlan *core.ExecutionPlan

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedPlan = core.GetExecutionPlan(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/p/openai/responses", nil)
	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/p/openai/responses",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{"model":"gpt-5-mini"}`),
		false,
		"pass-req-123",
		nil,
	)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(frame))
	req = req.WithContext(ctx)
	req.Header.Set("X-Request-ID", "pass-req-123")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	if assert.NotNil(t, capturedPlan) {
		assert.Equal(t, "pass-req-123", capturedPlan.RequestID)
		assert.Equal(t, core.ExecutionModePassthrough, capturedPlan.Mode)
		assert.Equal(t, "openai", capturedPlan.ProviderType)
		if assert.NotNil(t, capturedPlan.Passthrough) {
			assert.Equal(t, "openai", capturedPlan.Passthrough.Provider)
			assert.Equal(t, "responses", capturedPlan.Passthrough.RawEndpoint)
			assert.Equal(t, "gpt-5-mini", capturedPlan.Passthrough.Model)
			assert.Equal(t, "/p/openai/responses", capturedPlan.Passthrough.AuditPath)
		}
	}
}

func TestExecutionPlanning_PopulatesPassthroughProviderFromPathFallback(t *testing.T) {
	provider := &mockProvider{}

	e := echo.New()
	var capturedPlan *core.ExecutionPlan

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedPlan = core.GetExecutionPlan(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/p/openai/responses", nil)
	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/p/openai/responses",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{"model":"gpt-5-mini"}`),
		false,
		"pass-req-fallback-123",
		nil,
	)
	env := &core.WhiteBoxPrompt{
		RouteType:     "provider_passthrough",
		OperationType: string(core.OperationProviderPassthrough),
	}
	core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{
		RawEndpoint: "responses",
		Model:       "gpt-5-mini",
		AuditPath:   "/p/openai/responses",
	})
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, env)
	req = req.WithContext(ctx)
	req.Header.Set("X-Request-ID", "pass-req-fallback-123")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	if assert.NotNil(t, capturedPlan) {
		assert.Equal(t, core.ExecutionModePassthrough, capturedPlan.Mode)
		assert.Equal(t, "openai", capturedPlan.ProviderType)
		if assert.NotNil(t, capturedPlan.Passthrough) {
			assert.Equal(t, "openai", capturedPlan.Passthrough.Provider)
			assert.Equal(t, "responses", capturedPlan.Passthrough.RawEndpoint)
			assert.Equal(t, "gpt-5-mini", capturedPlan.Passthrough.Model)
		}
	}
}

func TestModelValidation_SetsRequestIDInContext(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var capturedRequestID string

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedRequestID = core.GetRequestID(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "test-req-123")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, "test-req-123", capturedRequestID)
}

func TestModelValidation_DoesNotTreatPrefixOvermatchAsBatchPath(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var capturedRequestID string

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedRequestID = core.GetRequestID(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/batchesXYZ", strings.NewReader(`{"foo":"bar"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "test-req-123")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "", capturedRequestID)
}

func TestModelValidation_BodyRewound(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var boundReq core.ChatRequest

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		if err := c.Bind(&boundReq); err != nil {
			return err
		}
		return c.String(http.StatusOK, "ok")
	})

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, "gpt-4o-mini", boundReq.Model)
	assert.Len(t, boundReq.Messages, 1)
}

func TestModelValidation_DoesNotReadLiveBodyWhenSelectorHintsAlreadyExist(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	handlerCalled := false

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = explodingValidationReadCloser{}

	frame := core.NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil, nil, "application/json", nil, false, "", nil)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, &core.WhiteBoxPrompt{
		RouteType:      "openai_compat",
		OperationType:  "chat_completions",
		JSONBodyParsed: true,
		RouteHints: core.RouteHints{
			Model: "gpt-4o-mini",
		},
	})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)
	assert.True(t, handlerCalled)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestModelValidation_UsesIngressBodyForMissingSelectorHints(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	handlerCalled := false

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = explodingValidationReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`),
		false,
		"",
		nil,
	)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(frame))
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)
	assert.True(t, handlerCalled)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestModelValidation_RegistryNotInitializedReturnsGatewayError(t *testing.T) {
	provider := &modelCountingValidationProvider{
		mockProvider: &mockProvider{},
		modelCount:   0,
	}

	e := echo.New()
	handlerCalled := false

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	assert.False(t, handlerCalled)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "model registry not initialized")
}

func TestModelValidation_EnrichesAuditEntryWithRequestedModelOnResolutionError(t *testing.T) {
	store := newAliasesTestStore(aliases.Alias{Name: "smart", TargetModel: "gpt-4o", TargetProvider: "openai", Enabled: false})
	catalog := &aliasesTestCatalog{
		supported: map[string]bool{
			"openai/gpt-4o": true,
		},
		providerTypes: map[string]string{
			"openai/gpt-4o": "openai",
		},
		models: map[string]core.Model{
			"openai/gpt-4o": {ID: "gpt-4o", Object: "model"},
		},
	}
	service, err := aliases.NewService(store, catalog)
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))

	inner := &mockProvider{
		supportedModels: []string{"gpt-4o"},
		providerTypes: map[string]string{
			"openai/gpt-4o": "openai",
		},
	}
	provider := aliases.NewProvider(inner, service)

	e := echo.New()
	handlerCalled := false

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"smart","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)

	err = handler(c)
	require.NoError(t, err)

	assert.False(t, handlerCalled)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "unsupported model: smart")
	assert.Equal(t, "smart", entry.Model)
	assert.Equal(t, "", entry.ResolvedModel)
	assert.Equal(t, "", entry.Provider)
	assert.Equal(t, "invalid_request_error", entry.ErrorType)
}

func TestModelValidation_ResolvesProviderTypeFromOversizedLiveBody(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes: map[string]string{
			"openai/gpt-4o-mini": "openai",
		},
	}

	e := echo.New()
	var capturedEnv *core.WhiteBoxPrompt
	var capturedProviderType string

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedEnv = core.GetWhiteBoxPrompt(c.Request().Context())
		capturedProviderType = GetProviderType(c)
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"provider":"openai",
		"model":"gpt-4o-mini",
		"messages":[{"role":"user","content":"hi"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "oversized-live-body")

	frame := core.NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil, nil, "application/json", nil, true, "", nil)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(frame))
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)
	require.NotNil(t, capturedEnv)
	assert.Equal(t, "openai", capturedProviderType)
	canonicalReq := capturedEnv.CachedChatRequest()
	require.NotNil(t, canonicalReq)
	assert.Equal(t, "gpt-4o-mini", canonicalReq.Model)
	assert.Equal(t, "openai", canonicalReq.Provider)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestModelValidation_DoesNotCacheCanonicalChatRequestWhenRouteHintsAlreadyExist(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var capturedEnv *core.WhiteBoxPrompt

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedEnv = core.GetWhiteBoxPrompt(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = explodingValidationReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"model":"gpt-4o-mini",
			"provider":"openai",
			"messages":[{"role":"user","content":"hi"}],
			"response_format":{"type":"json_schema"}
		}`),
		false,
		"",
		nil,
	)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(frame))
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)
	require.NotNil(t, capturedEnv)
	assert.Nil(t, capturedEnv.CachedChatRequest())
	assert.Equal(t, "gpt-4o-mini", capturedEnv.RouteHints.Model)
	assert.Equal(t, "openai", capturedEnv.RouteHints.Provider)
}

func TestModelValidation_DoesNotCacheCanonicalResponsesRequestWhenRouteHintsAlreadyExist(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var capturedEnv *core.WhiteBoxPrompt

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedEnv = core.GetWhiteBoxPrompt(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = explodingValidationReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/responses",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"model":"gpt-4o-mini",
			"input":[{"type":"message","role":"user","content":"hi","x_trace":{"id":"trace-1"}}]
		}`),
		false,
		"",
		nil,
	)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(frame))
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)
	require.NotNil(t, capturedEnv)
	assert.Nil(t, capturedEnv.CachedResponsesRequest())
	assert.Equal(t, "gpt-4o-mini", capturedEnv.RouteHints.Model)
}

func TestGetProviderType_EmptyWhenNotSet(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	assert.Equal(t, "", GetProviderType(c))
}

func TestGetProviderType_UsesExecutionPlan(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(core.WithExecutionPlan(req.Context(), &core.ExecutionPlan{
		ProviderType: "openai",
	}))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	assert.Equal(t, "openai", GetProviderType(c))
}

func TestSelectorHintsFromJSONGJSON_MatchesStdlibSemantics(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantModel    string
		wantProvider string
		wantParsed   bool
	}{
		{name: "model and provider strings", body: `{"provider":"openai","model":"gpt-4o-mini"}`, wantModel: "gpt-4o-mini", wantProvider: "openai", wantParsed: true},
		{name: "duplicate selector fields use first occurrence", body: `{"provider":"openai","provider":"anthropic","model":"blocked","model":"gpt-4o-mini"}`, wantModel: "blocked", wantProvider: "openai", wantParsed: true},
		{name: "duplicate null selector keeps first string value", body: `{"provider":"openai","provider":null,"model":"gpt-4o-mini","model":null}`, wantModel: "gpt-4o-mini", wantProvider: "openai", wantParsed: true},
		{name: "duplicate invalid selector field keeps first value", body: `{"provider":"openai","provider":123}`, wantProvider: "openai", wantParsed: true},
		{name: "model only", body: `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`, wantModel: "gpt-4o-mini", wantParsed: true},
		{name: "null selector fields", body: `{"provider":null,"model":null}`, wantParsed: true},
		{name: "missing selector fields", body: `{"messages":[{"role":"user","content":"hi"}]}`, wantParsed: true},
		{name: "invalid json", body: `not json`, wantParsed: false},
		{name: "array root", body: `[]`, wantParsed: false},
		{name: "numeric model", body: `{"model":123}`, wantParsed: false},
		{name: "numeric provider", body: `{"provider":123}`, wantParsed: false},
		{name: "mixed valid and invalid selector", body: `{"model":"gpt-4o-mini","provider":123}`, wantParsed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModel, gotProvider, gotParsed := selectorHintsFromJSONGJSON([]byte(tt.body))
			assert.Equal(t, tt.wantModel, gotModel)
			assert.Equal(t, tt.wantProvider, gotProvider)
			assert.Equal(t, tt.wantParsed, gotParsed)
		})
	}
}

func BenchmarkSelectorHintsFromJSONGJSON(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		model, provider, parsed := selectorHintsFromJSONGJSON(benchmarkSelectorValidationBody)
		if !parsed || model != "gpt-4o-mini" || provider != "openai" {
			b.Fatalf("unexpected selector hints: parsed=%v model=%q provider=%q", parsed, model, provider)
		}
	}
}

func TestModelValidation_ResolvesQualifiedMaskingAliasBeforeProviderParsing(t *testing.T) {
	catalog := aliasesTestCatalog{
		supported: map[string]bool{
			"anthropic/claude-opus-4-6": true,
			"openai/gpt-5-nano":         true,
		},
		providerTypes: map[string]string{
			"anthropic/claude-opus-4-6": "anthropic",
			"openai/gpt-5-nano":         "openai",
		},
		models: map[string]core.Model{
			"anthropic/claude-opus-4-6": {ID: "claude-opus-4-6", Object: "model"},
			"openai/gpt-5-nano":         {ID: "gpt-5-nano", Object: "model"},
		},
	}

	service, err := aliases.NewService(newAliasesTestStore(
		aliases.Alias{Name: "anthropic/claude-opus-4-6", TargetModel: "gpt-5-nano", TargetProvider: "openai", Enabled: true},
	), &catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := &mockProvider{
		supportedModels: []string{"claude-opus-4-6", "gpt-5-nano"},
		providerTypes: map[string]string{
			"anthropic/claude-opus-4-6": "anthropic",
			"openai/gpt-5-nano":         "openai",
		},
	}
	provider := aliases.NewProvider(inner, service)

	e := echo.New()
	var (
		capturedProviderType string
		capturedPlan         *core.ExecutionPlan
	)

	middleware := ExecutionPlanning(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedProviderType = GetProviderType(c)
		capturedPlan = core.GetExecutionPlan(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"anthropic/claude-opus-4-6","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler(c)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "openai", capturedProviderType)
	if assert.NotNil(t, capturedPlan) && assert.NotNil(t, capturedPlan.Resolution) {
		assert.True(t, capturedPlan.Resolution.AliasApplied)
		assert.Equal(t, "anthropic/claude-opus-4-6", capturedPlan.RequestedQualifiedModel())
		assert.Equal(t, "openai/gpt-5-nano", capturedPlan.ResolvedQualifiedModel())
	}
}

func TestExecutionPlanningWithResolver_UsesExplicitAliasResolverWithoutProviderDecorator(t *testing.T) {
	catalog := aliasesTestCatalog{
		supported: map[string]bool{
			"anthropic/claude-opus-4-6": true,
			"openai/gpt-5-nano":         true,
		},
		providerTypes: map[string]string{
			"anthropic/claude-opus-4-6": "anthropic",
			"openai/gpt-5-nano":         "openai",
		},
		models: map[string]core.Model{
			"anthropic/claude-opus-4-6": {ID: "claude-opus-4-6", Object: "model"},
			"openai/gpt-5-nano":         {ID: "gpt-5-nano", Object: "model"},
		},
	}

	service, err := aliases.NewService(newAliasesTestStore(
		aliases.Alias{Name: "anthropic/claude-opus-4-6", TargetModel: "gpt-5-nano", TargetProvider: "openai", Enabled: true},
	), &catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	provider := &mockProvider{
		supportedModels: []string{"gpt-5-nano"},
		providerTypes: map[string]string{
			"openai/gpt-5-nano": "openai",
		},
	}

	e := echo.New()
	var capturedPlan *core.ExecutionPlan

	middleware := ExecutionPlanningWithResolver(provider, service)
	handler := middleware(func(c *echo.Context) error {
		capturedPlan = core.GetExecutionPlan(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"anthropic/claude-opus-4-6","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler(c)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	if assert.NotNil(t, capturedPlan) && assert.NotNil(t, capturedPlan.Resolution) {
		assert.Equal(t, "openai", capturedPlan.ProviderType)
		assert.True(t, capturedPlan.Resolution.AliasApplied)
		assert.Equal(t, "anthropic/claude-opus-4-6", capturedPlan.RequestedQualifiedModel())
		assert.Equal(t, "openai/gpt-5-nano", capturedPlan.ResolvedQualifiedModel())
	}
}
