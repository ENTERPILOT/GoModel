package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/requestlog"
	"github.com/enterpilot/gomodel/internal/usage"
)

// moderationMockProvider extends mockProvider (a RoutableProvider) with
// moderation support so the service layer can be exercised without a router.
type moderationMockProvider struct {
	*mockProvider
	resp     *core.ModerationResponse
	err      error
	resolved *core.ModelSelector
	captured *core.ModerationRequest
}

func (m *moderationMockProvider) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if m.resolved != nil {
		return *m.resolved, true, nil
	}
	selector, err := core.ParseModelSelector(requested.Model, requested.ProviderHint)
	return selector, false, err
}

func (m *moderationMockProvider) CreateModeration(_ context.Context, req *core.ModerationRequest) (*core.ModerationResponse, error) {
	m.captured = req
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

func newModerationMock() *moderationMockProvider {
	return &moderationMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{core.DefaultModerationModel, "mistral-moderation-latest"}},
		resp: &core.ModerationResponse{
			ID:    "modr-1",
			Model: "omni-moderation-2024-09-26",
			Results: []core.ModerationResult{{
				Flagged:                   true,
				Categories:                map[string]bool{"violence": true},
				CategoryScores:            map[string]float64{"violence": 0.9},
				CategoryAppliedInputTypes: map[string][]string{"violence": {"text"}},
			}},
			Provider: "openai",
		},
	}
}

func TestModerations_ReturnsOpenAIShape(t *testing.T) {
	mock := newModerationMock()
	svc := &moderationService{provider: mock}
	c, rec := echotest.Post(t, "/v1/moderations", `{"input":["I will hurt you"]}`)
	err := svc.CreateModeration(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.JSONEq(t, `{
		"id": "modr-1",
		"model": "omni-moderation-2024-09-26",
		"provider": "openai",
		"results": [{
			"flagged": true,
			"categories": {"violence": true},
			"category_scores": {"violence": 0.9},
			"category_applied_input_types": {"violence": ["text"]}
		}]
	}`, rec.Body.String())
	require.NotNil(t, mock.captured)
	assert.Equal(t, core.DefaultModerationModel, mock.captured.Model, "omitted model falls back to the default")
	assert.Empty(t, mock.captured.Provider)
}

func TestModerations_RejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"malformed json", `{"input":`, "invalid request body"},
		{"missing input", `{"model":"omni-moderation-latest"}`, "input is required"},
		{"wrong input type", `{"input":42}`, "input must be a string or an array"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newModerationMock()
			svc := &moderationService{provider: mock}
			c, rec := echotest.Post(t, "/v1/moderations", tt.body)
			err := svc.CreateModeration(c)
			require.NoError(t, err)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tt.wantMsg)
			assert.Nil(t, mock.captured)
		})
	}
}

func TestModerations_RouterWithoutModerationSupport(t *testing.T) {
	svc := &moderationService{provider: &mockProvider{supportedModels: []string{core.DefaultModerationModel}}}
	c, rec := echotest.Post(t, "/v1/moderations", `{"input":"hi"}`)
	err := svc.CreateModeration(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "moderation is not supported")
}

func TestModerations_ChecksModelAccessAndBudget(t *testing.T) {
	t.Run("authorizer sees the resolved selector", func(t *testing.T) {
		mock := newModerationMock()
		mock.resolved = &core.ModelSelector{Provider: "openai", Model: core.DefaultModerationModel}
		authorizer := &recordingModelAuthorizer{}
		svc := &moderationService{provider: mock, modelAuthorizer: authorizer}
		c, rec := echotest.Post(t, "/v1/moderations", `{"input":"hi"}`)
		err := svc.CreateModeration(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, core.ModelSelector{Provider: "openai", Model: core.DefaultModerationModel}, authorizer.lastSelector)
	})

	t.Run("denied model never reaches the provider", func(t *testing.T) {
		mock := newModerationMock()
		authorizer := &recordingModelAuthorizer{err: core.NewInvalidRequestError("model not allowed", nil)}
		svc := &moderationService{provider: mock, modelAuthorizer: authorizer}
		c, rec := echotest.Post(t, "/v1/moderations", `{"input":"hi"}`)
		err := svc.CreateModeration(c)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Nil(t, mock.captured)
	})

	t.Run("failed budget check never reaches the provider", func(t *testing.T) {
		mock := newModerationMock()
		svc := &moderationService{provider: mock, budgetChecker: rejectingBudgetChecker{}}
		c, rec := echotest.Post(t, "/v1/moderations", `{"input":"hi"}`)
		err := svc.CreateModeration(c)
		require.NoError(t, err)
		assert.Contains(t, rec.Body.String(), "budget_check_failed")
		assert.Nil(t, mock.captured)
	})
}

func TestModerations_ProviderErrorIsSurfaced(t *testing.T) {
	mock := newModerationMock()
	mock.err = core.NewProviderError("openai", http.StatusBadRequest, "Invalid value for 'input'.", nil)
	svc := &moderationService{provider: mock}
	c, rec := echotest.Post(t, "/v1/moderations", `{"input":"hi"}`)
	err := svc.CreateModeration(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Invalid value for 'input'")
}

func TestModerations_NilProviderResponseIs502(t *testing.T) {
	mock := newModerationMock()
	mock.resp = nil
	svc := &moderationService{provider: mock}
	c, rec := echotest.Post(t, "/v1/moderations", `{"input":"hi"}`)
	err := svc.CreateModeration(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "empty moderation response")
}

func TestModerations_LogsUsageAndRequestBody(t *testing.T) {
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	mock := newModerationMock()
	mock.resolved = &core.ModelSelector{Provider: "openai", Model: core.DefaultModerationModel}
	svc := &moderationService{provider: mock, usageLogger: logger, logBodies: true}
	c, rec := echotest.Post(t, "/v1/moderations", `{"model":"omni-moderation-latest","input":"hi"}`)
	entry := &requestlog.LogEntry{}
	c.Set(string(requestlog.LogEntryKey), entry)
	err := svc.CreateModeration(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.NotNil(t, captured)
	assert.Equal(t, "/v1/moderations", captured.Endpoint)
	assert.Equal(t, core.DefaultModerationModel, captured.Model)
	assert.Equal(t, 1, captured.RawData["moderation_inputs"])

	assert.Equal(t, "openai/omni-moderation-latest", entry.ResolvedModel)
	body, ok := requestlog.BodyDocument(entry.Data.RequestBody).(map[string]any)
	require.True(t, ok, "request body = %T, want JSON object", entry.Data.RequestBody)
	assert.Equal(t, "hi", body["input"])
}

// TestModerations_HandlerRoute verifies POST /v1/moderations is registered on
// the HTTP server and reaches the moderation service end to end.
func TestModerations_HandlerRoute(t *testing.T) {
	mock := newModerationMock()
	srv := New(mock, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/moderations", strings.NewReader(`{"input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := echotest.Decode[map[string]any](t, rec)
	assert.Equal(t, "modr-1", got["id"])
	require.NotNil(t, mock.captured)
}
