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

// customVoicesMockProvider adds OpenAI's own voice list to the voices fake.
type customVoicesMockProvider struct {
	*voicesMockProvider
	serving        []string
	custom         *core.AudioCustomVoicesResponse
	customErr      error
	calledProvider string
	calledParams   *core.AudioCustomVoicesParams
}

func (m *customVoicesMockProvider) AudioCustomVoiceProviders() []string { return m.serving }

func (m *customVoicesMockProvider) ListCustomVoices(_ context.Context, provider string, params core.AudioCustomVoicesParams) (*core.AudioCustomVoicesResponse, error) {
	m.calledProvider = provider
	m.calledParams = &params
	if m.customErr != nil {
		return nil, m.customErr
	}
	return m.custom, nil
}

func newCustomVoicesMock() *customVoicesMockProvider {
	last := "voice_1"
	mock := &customVoicesMockProvider{
		voicesMockProvider: newVoicesMock(),
		serving:            []string{"openai-main"},
		custom: &core.AudioCustomVoicesResponse{
			Object:  "list",
			Data:    []core.AudioCustomVoice{{ID: "voice_1", Object: "audio.voice", Name: "Narrator", CreatedAt: 1734220800, Type: "audio_sample"}},
			FirstID: &last,
			LastID:  &last,
			HasMore: true,
		},
	}
	mock.providerTypes = map[string]string{"openai-main/tts-1": "openai", "kokoro-local/kokoro": "openai"}
	mock.providerNames = map[string]string{"openai-main/tts-1": "openai-main", "kokoro-local/kokoro": "kokoro-local"}
	return mock
}

// Without a model the endpoint is OpenAI's own voice list, in OpenAI's shape.
func TestAudioVoices_WithoutModelServesOpenAIVoiceList(t *testing.T) {
	mock := newCustomVoicesMock()
	authorizer := &recordingModelAuthorizer{}
	svc := &audioService{provider: mock, modelAuthorizer: authorizer}
	entry := &requestlog.LogEntry{}

	c, rec := echotest.Get(t, "/v1/audio/voices?limit=%201&after=voice_0", echotest.WithValue(string(requestlog.LogEntryKey), entry))
	require.NoError(t, svc.ListVoices(c))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"object":"list","data":[{"id":"voice_1","object":"audio.voice","name":"Narrator","created_at":1734220800,"type":"audio_sample"}],"first_id":"voice_1","last_id":"voice_1","has_more":true}`, rec.Body.String())
	assert.Equal(t, "openai-main", mock.calledProvider)
	assert.Equal(t, &core.AudioCustomVoicesParams{Limit: " 1", After: "voice_0"}, mock.calledParams, "forwarded as sent; the upstream validates")
	assert.Equal(t, "openai-main", authorizer.lastProvider, "a model-less call needs the whole provider")
	assert.Nil(t, mock.captured, "the per-model listing is not used")
	assert.Equal(t, "openai", entry.Provider)
	assert.Equal(t, "openai-main", entry.ProviderName)
}

func TestAudioVoices_WithoutModelEmptyListKeepsOpenAIShape(t *testing.T) {
	mock := newCustomVoicesMock()
	mock.custom = nil
	svc := &audioService{provider: mock}

	c, rec := echotest.Get(t, "/v1/audio/voices")
	require.NoError(t, svc.ListVoices(c))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"object":"list","data":[],"first_id":null,"last_id":null,"has_more":false}`, rec.Body.String())
}

// The provider parameter names an instance or a provider type; either way the
// request goes to an instance that serves the list.
func TestAudioVoices_WithoutModelHonorsProviderParameter(t *testing.T) {
	for _, provider := range []string{"openai-eu", "openai"} {
		t.Run(provider, func(t *testing.T) {
			mock := newCustomVoicesMock()
			mock.serving = []string{"openai-eu", "openai-main"}
			mock.providerTypes["openai-eu/tts-1"] = "openai"
			mock.providerNames["openai-eu/tts-1"] = "openai-eu"
			authorizer := &recordingModelAuthorizer{}
			svc := &audioService{provider: mock, modelAuthorizer: authorizer}

			c, rec := echotest.Get(t, "/v1/audio/voices?provider="+provider)
			require.NoError(t, svc.ListVoices(c))

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "openai-eu", mock.calledProvider)
			assert.Equal(t, "openai-eu", authorizer.lastProvider)
		})
	}
}

func TestAudioVoices_WithoutModelErrors(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		mutate     func(*customVoicesMockProvider)
		authorizer *recordingModelAuthorizer
		limiter    bool
		wantStatus int
		wantMsg    string
		wantCalled bool
	}{
		{
			name:       "no provider serves the list",
			target:     "/v1/audio/voices",
			mutate:     func(m *customVoicesMockProvider) { m.serving = nil },
			wantStatus: http.StatusBadRequest,
			wantMsg:    "model is required",
		},
		{
			// kokoro-local is of type openai too, but does not serve the list.
			name:       "a named provider that does not serve it",
			target:     "/v1/audio/voices?provider=kokoro-local",
			wantStatus: http.StatusBadRequest,
			wantMsg:    `provider "kokoro-local" does not support listing custom voices`,
		},
		{
			name:       "an unknown provider",
			target:     "/v1/audio/voices?provider=nope",
			wantStatus: http.StatusBadRequest,
			wantMsg:    `provider "nope" does not support listing custom voices`,
		},
		{
			name:       "key not granted the whole provider",
			target:     "/v1/audio/voices",
			authorizer: &recordingModelAuthorizer{err: core.NewPermissionError("provider not allowed for this key")},
			wantStatus: http.StatusForbidden,
			wantMsg:    "provider not allowed",
		},
		{
			name:       "rate limit breach",
			target:     "/v1/audio/voices",
			limiter:    true,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:   "upstream rejects the cursor",
			target: "/v1/audio/voices?after=voice_missing",
			mutate: func(m *customVoicesMockProvider) {
				m.customErr = core.NewNotFoundError("No voice found with id 'voice_missing'")
			},
			wantStatus: http.StatusNotFound,
			wantMsg:    "No voice found",
			wantCalled: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newCustomVoicesMock()
			if tt.mutate != nil {
				tt.mutate(mock)
			}
			svc := &audioService{provider: mock}
			if tt.authorizer != nil {
				svc.modelAuthorizer = tt.authorizer
			}
			if tt.limiter {
				svc.rateLimiter = rejectingRateLimiter{}
			}

			c, rec := echotest.Get(t, tt.target)
			require.NoError(t, svc.ListVoices(c))

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assert.Contains(t, echotest.Decode[core.OpenAIErrorEnvelope](t, rec).Error.Message, tt.wantMsg)
			assert.Equal(t, tt.wantCalled, mock.calledParams != nil)
		})
	}
}
