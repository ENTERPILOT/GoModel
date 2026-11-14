package providers

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

type mockVoicesProvider struct {
	*mockProvider
	voices  *core.AudioVoicesResponse
	lastReq *core.AudioVoicesRequest
}

func (m *mockVoicesProvider) ListVoices(_ context.Context, req *core.AudioVoicesRequest) (*core.AudioVoicesResponse, error) {
	m.lastReq = req
	if m.err != nil {
		return nil, m.err
	}
	return m.voices, nil
}

func TestRouterListVoices(t *testing.T) {
	lister := &mockVoicesProvider{
		mockProvider: &mockProvider{name: "audiocpp"},
		voices:       &core.AudioVoicesResponse{Voices: []core.AudioVoice{{ID: "alba", Name: "alba"}}},
	}
	lookup := newMockLookup()
	lookup.addModel("audiocpp/pocket-tts", lister, "audiocpp")
	router, err := NewRouter(lookup)
	require.NoError(t, err)

	resp, err := router.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "pocket-tts", Provider: "audiocpp"})
	require.NoError(t, err)
	assert.Equal(t, lister.voices, resp)
	require.NotNil(t, lister.lastReq)
	assert.Equal(t, "pocket-tts", lister.lastReq.Model)
	assert.Empty(t, lister.lastReq.Provider, "routing hint is stripped before dispatch")
}

func TestRouterListVoices_Errors(t *testing.T) {
	tests := []struct {
		name       string
		req        *core.AudioVoicesRequest
		wantStatus int
		wantType   core.ErrorType
		wantMsg    string
	}{
		{
			name:       "provider without voice listing",
			req:        &core.AudioVoicesRequest{Model: "whisper-1", Provider: "openai"},
			wantStatus: http.StatusBadRequest,
			wantType:   core.ErrorTypeInvalidRequest,
			wantMsg:    `model "whisper-1" does not support listing voices`,
		},
		{
			name:       "unknown model",
			req:        &core.AudioVoicesRequest{Model: "nope", Provider: "openai"},
			wantStatus: http.StatusNotFound,
			wantType:   core.ErrorTypeNotFound,
		},
		{
			name:       "nil request",
			wantStatus: http.StatusBadRequest,
			wantType:   core.ErrorTypeInvalidRequest,
			wantMsg:    "audio voices request is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := newMockLookup()
			lookup.addModel("openai/whisper-1", &mockProvider{name: "openai"}, "openai")
			router, err := NewRouter(lookup)
			require.NoError(t, err)

			_, err = router.ListVoices(context.Background(), tt.req)
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tt.wantStatus, gatewayErr.HTTPStatusCode())
			assert.Equal(t, tt.wantType, gatewayErr.Type)
			assert.Contains(t, gatewayErr.Message, tt.wantMsg)
		})
	}
}
