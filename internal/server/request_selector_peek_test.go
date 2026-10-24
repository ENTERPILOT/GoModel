package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestPeekRequestBodySelectorHintsModelOnlyIsNotParsed(t *testing.T) {
	body := `{"model":"gpt-4o-mini","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))

	hints := peekRequestBodySelectorHints(req, requestSelectorPeekLimit)
	require.Equal(t, "gpt-4o-mini", hints.model)
	require.False(t, hints.parsed)
	require.False(t, hints.complete)

	restored, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(restored))
}

func TestPeekRequestBodySelectorHintsProviderAndModelIsSelectorParsedOnly(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"provider":"openai","model":"gpt-4o-mini","stream":true}`))

	hints := peekRequestBodySelectorHints(req, requestSelectorPeekLimit)
	require.True(t, hints.parsed)
	require.False(t, hints.complete)
	require.Equal(t, "openai", hints.provider)
	require.Equal(t, "gpt-4o-mini", hints.model)
}

func TestSeedRequestBodySelectorHintsDoesNotMarkModelOnlyPeekAsParsed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","stream":true}`))
	env := &core.WhiteBoxPrompt{}

	require.NoError(t, seedRequestBodySelectorHints(req, core.BodyModeJSON, env))

	require.False(t, env.JSONBodyParsed)
	require.False(t, env.StreamRequested)
	require.Empty(t, env.RouteHints.Model)
}

func TestSeedRequestBodySelectorHintsAppliesCompleteModelForOpaqueBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","stream":true}`))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	env := &core.WhiteBoxPrompt{}
	core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "openai"})

	require.NoError(t, seedRequestBodySelectorHints(req, core.BodyModeOpaque, env))

	require.True(t, env.JSONBodyParsed)
	require.True(t, env.StreamRequested)
	require.Equal(t, "gpt-4o-mini", env.RouteHints.Model)

	info := env.CachedPassthroughRouteInfo()
	require.NotNil(t, info)
	require.Equal(t, "gpt-4o-mini", info.Model)
	require.True(t, info.Stream)
	require.False(t, info.StreamUncertain)

	restored, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, `{"model":"gpt-4o-mini","stream":true}`, string(restored))
}

// An opaque body larger than the peek limit is read in full, so a model field
// repeated after a long value is still caught: the upstream's parser would
// take the second one, which the gateway never authorized.
func TestSeedRequestBodySelectorHintsDetectsDuplicateModelBeyondPeekLimit(t *testing.T) {
	tests := []struct {
		name          string
		prefix        string
		wantStream    bool
		wantUncertain bool
		knownLength   bool
	}{
		{name: "model first", prefix: `{"model":"allowed-model","padding":"`, wantUncertain: true},
		{name: "stream first", prefix: `{"stream":true,"model":"allowed-model","padding":"`, wantStream: true},
		{name: "model before stream", prefix: `{"model":"allowed-model","stream":true,"padding":"`, wantStream: true},
		{name: "model before stream with known length", prefix: `{"model":"allowed-model","stream":true,"padding":"`, wantStream: true, knownLength: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := test.prefix + strings.Repeat("x", int(requestSelectorPeekLimit)) + `","model":"restricted-model"}`
			req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(body))
			if !test.knownLength {
				req.ContentLength = -1
			}
			req.Header.Set("Content-Type", "application/json")
			env := &core.WhiteBoxPrompt{}
			core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "openai"})

			require.NoError(t, seedRequestBodySelectorHints(req, core.BodyModeOpaque, env))

			require.False(t, env.JSONBodyParsed)
			require.Empty(t, env.RouteHints.Model)

			info := env.CachedPassthroughRouteInfo()
			require.NotNil(t, info)
			require.Empty(t, info.Model)
			require.True(t, info.ModelAmbiguous)
			require.Equal(t, test.wantStream, info.Stream)
			require.Equal(t, test.wantUncertain, info.StreamUncertain)

			restored, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, body, string(restored))
		})
	}
}

// A single model named after a value longer than the peek limit (a System One
// state, a long prompt) is authoritative: the whole body was read, so the
// allowlist can check it, and the bytes forwarded upstream are unchanged.
func TestSeedRequestBodySelectorHintsAppliesModelFromOversizedOpaqueBody(t *testing.T) {
	body := `{"stream":true,"state":"` + strings.Repeat("x", int(requestSelectorPeekLimit)) + `","model":"jev-latest"}`
	req := httptest.NewRequest(http.MethodPost, "/p/jev/systemone", strings.NewReader(body))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	env := &core.WhiteBoxPrompt{}
	core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "jev"})

	require.NoError(t, seedRequestBodySelectorHints(req, core.BodyModeOpaque, env))

	require.True(t, env.JSONBodyParsed)
	require.True(t, env.StreamRequested)
	require.Equal(t, "jev-latest", env.RouteHints.Model)

	info := env.CachedPassthroughRouteInfo()
	require.NotNil(t, info)
	require.Equal(t, "jev-latest", info.Model)
	require.False(t, info.ModelAmbiguous)
	require.True(t, info.Stream)
	require.False(t, info.StreamUncertain)

	restored, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(restored))
}

// erroringReader yields err on every read, standing in for the body-limit
// reader once a body exceeds the configured limit.
type erroringReader struct{ err error }

func (r erroringReader) Read([]byte) (int, error) { return 0, r.err }

// Reading past the peek limit can fail (the body limit's 413); the error must
// reach the caller instead of leaving the model unset and the request
// forwarded unchecked.
func TestSeedRequestBodySelectorHintsReturnsBodyReadError(t *testing.T) {
	prefix := `{"model":"jev-latest","state":"` + strings.Repeat("x", int(requestSelectorPeekLimit))
	req := httptest.NewRequest(http.MethodPost, "/p/jev/systemone", io.MultiReader(strings.NewReader(prefix), erroringReader{err: echo.ErrStatusRequestEntityTooLarge}))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	env := &core.WhiteBoxPrompt{}
	core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "jev"})

	err := seedRequestBodySelectorHints(req, core.BodyModeOpaque, env)
	require.ErrorIs(t, err, echo.ErrStatusRequestEntityTooLarge)
	require.False(t, env.JSONBodyParsed)
	require.Empty(t, env.CachedPassthroughRouteInfo().Model)
}

func TestSeedRequestBodySelectorHintsRejectsAmbiguousStreamBeforePeekLimit(t *testing.T) {
	body := `{"stream":true,"stream":false,"padding":"` + strings.Repeat("x", int(requestSelectorPeekLimit)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(body))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	env := &core.WhiteBoxPrompt{}
	core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "openai"})

	require.NoError(t, seedRequestBodySelectorHints(req, core.BodyModeOpaque, env))

	require.False(t, env.StreamRequested)

	info := env.CachedPassthroughRouteInfo()
	require.NotNil(t, info)
	require.False(t, info.Stream)
	require.True(t, info.StreamUncertain)
}

// A stream field repeated after a value longer than the peek limit is read
// whole and treated like any duplicate: no stream intent is claimed, and the
// uncertainty is recorded, rather than trusting the first value the upstream
// would not use.
func TestSeedRequestBodySelectorHintsRejectsDuplicateStreamBeyondPeekLimit(t *testing.T) {
	body := `{"stream":true,"padding":"` + strings.Repeat("x", int(requestSelectorPeekLimit)) + `","stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(body))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	env := &core.WhiteBoxPrompt{}
	core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "openai"})

	require.NoError(t, seedRequestBodySelectorHints(req, core.BodyModeOpaque, env))

	require.False(t, env.StreamRequested)

	info := env.CachedPassthroughRouteInfo()
	require.NotNil(t, info)
	require.False(t, info.Stream)
	require.True(t, info.StreamUncertain)

	restored, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(restored))

	var forwarded struct {
		Stream bool `json:"stream"`
	}
	err = json.Unmarshal(restored, &forwarded)
	require.NoError(t, err)
	require.False(t, forwarded.Stream)
}

func TestSeedRequestBodySelectorHintsRejectsCompleteDuplicateOpaqueFields(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantStream    bool
		wantUncertain bool
	}{
		{
			name:          "stream",
			body:          `{"stream":true,"stream":false}`,
			wantUncertain: true,
		},
		{
			name:       "provider",
			body:       `{"provider":"first","provider":"second","stream":true}`,
			wantStream: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			env := &core.WhiteBoxPrompt{}
			core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "openai"})

			require.NoError(t, seedRequestBodySelectorHints(req, core.BodyModeOpaque, env))

			require.False(t, env.JSONBodyParsed)
			require.Equal(t, "openai", env.RouteHints.Provider)

			info := env.CachedPassthroughRouteInfo()
			require.NotNil(t, info)
			require.Equal(t, test.wantStream, info.Stream)
			require.Equal(t, test.wantUncertain, info.StreamUncertain)
		})
	}
}

func TestSeedRequestBodySelectorHintsRejectsDuplicateOpaqueModel(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(`{"model":"allowed-model","stream":true,"model":"restricted-model"}`))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	env := &core.WhiteBoxPrompt{}
	core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "openai"})

	require.NoError(t, seedRequestBodySelectorHints(req, core.BodyModeOpaque, env))

	require.False(t, env.JSONBodyParsed)
	require.Empty(t, env.RouteHints.Model)
	require.True(t, env.StreamRequested)

	info := env.CachedPassthroughRouteInfo()
	require.NotNil(t, info)
	require.True(t, info.ModelAmbiguous)
	require.True(t, info.Stream)
	require.False(t, info.StreamUncertain)
}

func TestDecodeCompleteRequestBodySelectorHintsRejectsAmbiguousBodies(t *testing.T) {
	bodies := []string{
		`{"model":"first","model":"second"}`,
		`{"provider":"first","provider":"second"}`,
		`{"stream":false,"stream":true}`,
		`{"model":"gpt-4o-mini"`,
		`{"model":"gpt-4o-mini"} {}`,
	}

	for _, body := range bodies {
		hints := decodeCompleteRequestBodySelectorHints(strings.NewReader(body))
		require.False(t, hints.complete, "decodeCompleteRequestBodySelectorHints(%q).complete = true, want false", body)
	}
}

func TestSeedRequestBodySelectorHintsTracksStreamConfidenceIndependently(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantStream    bool
		wantUncertain bool
	}{
		{
			name:          "stream before model and provider",
			body:          `{"stream":true,"model":"gpt-4o-mini","provider":"openai","padding":"` + strings.Repeat("x", 65*1024) + `"}`,
			wantStream:    true,
			wantUncertain: false,
		},
		{
			name:          "stream absent before bounded peek stops",
			body:          `{"model":"gpt-4o-mini","provider":"openai","padding":"` + strings.Repeat("x", 65*1024) + `"}`,
			wantStream:    false,
			wantUncertain: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(test.body))
			env := &core.WhiteBoxPrompt{}
			core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "openai"})

			require.NoError(t, seedRequestBodySelectorHints(req, core.BodyModeJSON, env))

			info := env.CachedPassthroughRouteInfo()
			require.NotNil(t, info)
			assert.Equal(t, test.wantStream, info.Stream)
			assert.Equal(t, test.wantUncertain, info.StreamUncertain)
		})
	}
}
