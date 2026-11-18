package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/requestlog"
)

// voicesMockProvider adds voice listing to the audio fake.
type voicesMockProvider struct {
	*audioMockProvider
	voices    *core.AudioVoicesResponse
	voicesErr error
	captured  *core.AudioVoicesRequest
}

func (m *voicesMockProvider) ListVoices(_ context.Context, req *core.AudioVoicesRequest) (*core.AudioVoicesResponse, error) {
	m.captured = req
	if m.voicesErr != nil {
		return nil, m.voicesErr
	}
	return m.voices, nil
}

func newVoicesMock(voices ...core.AudioVoice) *voicesMockProvider {
	return &voicesMockProvider{
		audioMockProvider: &audioMockProvider{
			mockProvider: &mockProvider{supportedModels: []string{"kokoro"}},
			resolved:     &core.ModelSelector{Provider: "kokoro-local", Model: "kokoro"},
		},
		voices: &core.AudioVoicesResponse{Voices: voices},
	}
}

// aliasResolver maps one alias to a concrete selector, as a virtual model does.
type aliasResolver struct {
	alias  string
	target core.ModelSelector
}

func (r aliasResolver) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if requested.Model == r.alias {
		return r.target, true, nil
	}
	selector, err := requested.Normalize()
	return selector, false, err
}

func TestAudioVoices_HappyPath(t *testing.T) {
	mock := newVoicesMock(core.AudioVoice{ID: "af_bella", Name: "Bella"})
	handler := NewHandler(mock, nil, nil, nil)

	c, rec := echotest.Get(t, "/v1/audio/voices?model=kokoro")
	require.NoError(t, handler.AudioVoices(c))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"voices":[{"id":"af_bella","name":"Bella"}]}`, rec.Body.String())
	require.NotNil(t, mock.captured)
	assert.Equal(t, "kokoro", mock.captured.Model)
	assert.Equal(t, "kokoro-local", mock.captured.Provider)
}

// The request log row names the provider that served the listing, as speech does.
func TestAudioVoices_RequestLogEntryCarriesResolvedRoute(t *testing.T) {
	mock := newVoicesMock(core.AudioVoice{ID: "af_bella", Name: "Bella"})
	mock.providerTypes = map[string]string{"kokoro-local/kokoro": "openai"}
	mock.providerNames = map[string]string{"kokoro-local/kokoro": "kokoro-local"}
	svc := &audioService{provider: mock}
	entry := &requestlog.LogEntry{}

	c, rec := echotest.Get(t, "/v1/audio/voices?model=kokoro", echotest.WithValue(string(requestlog.LogEntryKey), entry))
	require.NoError(t, svc.ListVoices(c))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "kokoro", entry.RequestedModel)
	assert.Equal(t, "kokoro-local/kokoro", entry.ResolvedModel)
	assert.Equal(t, "openai", entry.Provider)
	assert.Equal(t, "kokoro-local", entry.ProviderName)
}

func TestAudioVoices_EmptyListIsAnArray(t *testing.T) {
	mock := newVoicesMock()
	mock.voices = nil
	handler := NewHandler(mock, nil, nil, nil)

	c, rec := echotest.Get(t, "/v1/audio/voices?model=kokoro")
	require.NoError(t, handler.AudioVoices(c))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"voices":[]}`, rec.Body.String())
}

func TestAudioVoices_RateLimitBreachNeverReachesProvider(t *testing.T) {
	mock := newVoicesMock(core.AudioVoice{ID: "af_bella", Name: "Bella"})
	svc := &audioService{
		provider:    mock,
		rateLimiter: rejectingRateLimiter{}}

	c, rec := echotest.Get(t, "/v1/audio/voices?model=kokoro")
	require.NoError(t, svc.ListVoices(c))

	require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	assert.Nil(t, mock.captured)
}

func TestAudioVoices_ResolvesAliasBeforeAuthorizing(t *testing.T) {
	mock := newVoicesMock(core.AudioVoice{ID: "alba", Name: "alba"})
	mock.resolved = nil
	target := core.ModelSelector{Provider: "audiocpp", Model: "pocket-tts"}
	authorizer := &recordingModelAuthorizer{}
	svc := &audioService{
		provider:        mock,
		modelResolver:   aliasResolver{alias: "narrator", target: target},
		modelAuthorizer: authorizer}

	c, rec := echotest.Get(t, "/v1/audio/voices?model=narrator")
	require.NoError(t, svc.ListVoices(c))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, target, authorizer.lastSelector)
	require.NotNil(t, mock.captured)
	assert.Equal(t, "pocket-tts", mock.captured.Model)
	assert.Equal(t, "audiocpp", mock.captured.Provider)
}

func TestAudioVoices_Errors(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		provider   core.RoutableProvider
		authorizer *recordingModelAuthorizer
		wantStatus int
		wantType   core.ErrorType
		wantMsg    string
	}{
		{
			name:       "missing model",
			target:     "/v1/audio/voices",
			provider:   newVoicesMock(),
			wantStatus: http.StatusBadRequest,
			wantType:   core.ErrorTypeInvalidRequest,
			wantMsg:    "model is required",
		},
		{
			name:       "blank model",
			target:     "/v1/audio/voices?model=%20",
			provider:   newVoicesMock(),
			wantStatus: http.StatusBadRequest,
			wantType:   core.ErrorTypeInvalidRequest,
			wantMsg:    "model is required",
		},
		{
			name:       "key not allowed to use the model",
			target:     "/v1/audio/voices?model=kokoro",
			provider:   newVoicesMock(),
			authorizer: &recordingModelAuthorizer{err: core.NewPermissionError("model not allowed for this key")},
			wantStatus: http.StatusForbidden,
			wantType:   core.ErrorTypePermission,
			wantMsg:    "model not allowed",
		},
		{
			name:   "provider without voice listing",
			target: "/v1/audio/voices?model=kokoro",
			provider: func() core.RoutableProvider {
				m := newVoicesMock()
				m.voicesErr = core.NewAudioVoicesUnsupportedError("kokoro")
				return m
			}(),
			wantStatus: http.StatusBadRequest,
			wantType:   core.ErrorTypeInvalidRequest,
			wantMsg:    "does not support listing voices",
		},
		{
			// The router resolves the model and reports an unknown one exactly
			// as it does for speech (see TestRouterListVoices_Errors).
			name:   "unknown model",
			target: "/v1/audio/voices?model=missing-model",
			provider: func() core.RoutableProvider {
				m := newVoicesMock()
				m.resolved = nil
				m.voicesErr = core.NewModelNotFoundError("missing-model")
				return m
			}(),
			wantStatus: http.StatusNotFound,
			wantType:   core.ErrorTypeNotFound,
			wantMsg:    "unsupported model: missing-model",
		},
		{
			name:   "upstream failure",
			target: "/v1/audio/voices?model=kokoro",
			provider: func() core.RoutableProvider {
				m := newVoicesMock()
				m.voicesErr = core.NewProviderError("kokoro-local", http.StatusServiceUnavailable, "upstream unavailable", nil)
				return m
			}(),
			wantStatus: http.StatusServiceUnavailable,
			wantType:   core.ErrorTypeProvider,
			wantMsg:    "upstream unavailable",
		},
		{
			name:       "router without voice listing",
			target:     "/v1/audio/voices?model=kokoro",
			provider:   &audioMockProvider{mockProvider: &mockProvider{supportedModels: []string{"kokoro"}}},
			wantStatus: http.StatusBadRequest,
			wantType:   core.ErrorTypeInvalidRequest,
			wantMsg:    "not supported",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &audioService{provider: tt.provider}
			if tt.authorizer != nil {
				svc.modelAuthorizer = tt.authorizer
			}

			c, rec := echotest.Get(t, tt.target)
			require.NoError(t, svc.ListVoices(c))

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			body := echotest.Decode[core.OpenAIErrorEnvelope](t, rec)
			assert.Equal(t, string(tt.wantType), string(body.Error.Type))
			assert.Contains(t, body.Error.Message, tt.wantMsg)
		})
	}
}

// TestAudioVoices_RouteIsRegisteredBehindAuth drives the full server so the
// route, the auth middleware, and the body-free GET admission are covered.
func TestAudioVoices_RouteIsRegisteredBehindAuth(t *testing.T) {
	srv := New(newVoicesMock(core.AudioVoice{ID: "af_bella", Name: "af_bella"}), &Config{MasterKey: "master-key"})

	tests := []struct {
		name       string
		bearer     string
		wantStatus int
	}{
		{name: "authenticated", bearer: "master-key", wantStatus: http.StatusOK},
		{name: "no credentials", wantStatus: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/audio/voices?model=kokoro", nil)
			if tt.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tt.bearer)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
		})
	}
}
