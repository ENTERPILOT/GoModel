package server

import (
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

// An opaque body larger than the peek limit is read in full, so a selector
// field repeated after a long value is still caught: the upstream's parser
// would take the second one, which the gateway never authorized.
func TestSeedRequestBodySelectorHintsRejectsDuplicateSelectorBeyondPeekLimit(t *testing.T) {
	padding := strings.Repeat("x", int(requestSelectorPeekLimit))
	tests := []struct {
		name          string
		body          string
		wantDuplicate string
		knownLength   bool
	}{
		{name: "model first", body: `{"model":"allowed-model","padding":"` + padding + `","model":"restricted-model"}`, wantDuplicate: "model"},
		{name: "stream first", body: `{"stream":true,"model":"allowed-model","padding":"` + padding + `","model":"restricted-model"}`, wantDuplicate: "model"},
		{name: "model before stream with known length", body: `{"model":"allowed-model","stream":true,"padding":"` + padding + `","model":"restricted-model"}`, wantDuplicate: "model", knownLength: true},
		{name: "duplicate provider with known length", body: `{"provider":"a","padding":"` + padding + `","provider":"b"}`, wantDuplicate: "provider", knownLength: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(test.body))
			if !test.knownLength {
				req.ContentLength = -1
			}
			req.Header.Set("Content-Type", "application/json")
			env := &core.WhiteBoxPrompt{}
			core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "openai"})

			err := seedRequestBodySelectorHints(req, core.BodyModeOpaque, env)

			assertDuplicateSelectorError(t, err, test.wantDuplicate)
			assert.False(t, env.JSONBodyParsed)
			assert.Empty(t, env.RouteHints.Model)
			assertBodyReplays(t, req, test.body)
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

	err := seedRequestBodySelectorHints(req, core.BodyModeOpaque, env)

	assertDuplicateSelectorError(t, err, "stream")
	assert.False(t, env.StreamRequested)
	assert.False(t, env.JSONBodyParsed)
	assertBodyReplays(t, req, body)
}

// A stream field repeated after a value longer than the peek limit is read
// whole and rejected like any duplicate, rather than trusting the first value
// the upstream would not use.
func TestSeedRequestBodySelectorHintsRejectsDuplicateStreamBeyondPeekLimit(t *testing.T) {
	body := `{"stream":true,"padding":"` + strings.Repeat("x", int(requestSelectorPeekLimit)) + `","stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(body))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	env := &core.WhiteBoxPrompt{}
	core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "openai"})

	err := seedRequestBodySelectorHints(req, core.BodyModeOpaque, env)

	assertDuplicateSelectorError(t, err, "stream")
	assert.False(t, env.StreamRequested)
	assertBodyReplays(t, req, body)
}

func TestSeedRequestBodySelectorHintsRejectsCompleteDuplicateOpaqueFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "stream", body: `{"stream":true,"stream":false}`},
		{name: "provider", body: `{"provider":"first","provider":"second","stream":true}`},
		{name: "model", body: `{"model":"allowed-model","stream":true,"model":"restricted-model"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			env := &core.WhiteBoxPrompt{}
			core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "openai"})

			err := seedRequestBodySelectorHints(req, core.BodyModeOpaque, env)

			assertDuplicateSelectorError(t, err, test.name)
			assert.False(t, env.JSONBodyParsed)
			assert.Empty(t, env.RouteHints.Model)
			assertBodyReplays(t, req, test.body)
		})
	}
}

func TestSeedRequestBodySelectorHintsRejectsDuplicateOpaqueModel(t *testing.T) {
	body := `{"model":"allowed-model","stream":true,"model":"restricted-model"}`
	req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(body))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	env := &core.WhiteBoxPrompt{}
	core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "openai"})

	err := seedRequestBodySelectorHints(req, core.BodyModeOpaque, env)

	assertDuplicateSelectorError(t, err, "model")
	assert.False(t, env.JSONBodyParsed)
	assert.False(t, env.StreamRequested)
	assert.Empty(t, env.RouteHints.Model)
	assertBodyReplays(t, req, body)
}

func TestSeedRequestBodySelectorHintsRejectsDuplicateTranslatedSelectorWithinPeek(t *testing.T) {
	body := `{"provider":"openai","provider":"groq","model":"gpt-4o-mini","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	env := &core.WhiteBoxPrompt{}

	err := seedRequestBodySelectorHints(req, core.BodyModeJSON, env)

	assertDuplicateSelectorError(t, err, "provider")
	assert.False(t, env.JSONBodyParsed)
	assert.Empty(t, env.RouteHints.Provider)
	assertBodyReplays(t, req, body)
}

func assertDuplicateSelectorError(t *testing.T, err error, field string) {
	t.Helper()
	require.Error(t, err, "want duplicate %q rejection", field)
	assert.Contains(t, err.Error(), `duplicate top-level "`+field+`" field`)
}

// assertBodyReplays verifies the peek left the request body byte-for-byte
// intact for whoever reads it next.
func assertBodyReplays(t *testing.T, req *http.Request, want string) {
	t.Helper()
	got, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, want, string(got))
}

func TestDecodeCompleteRequestBodySelectorHintsRejectsAmbiguousBodies(t *testing.T) {
	tests := []struct {
		body          string
		wantDuplicate string
	}{
		{body: `{"model":"first","model":"second"}`, wantDuplicate: "model"},
		{body: `{"provider":"first","provider":"second"}`, wantDuplicate: "provider"},
		{body: `{"stream":false,"stream":true}`, wantDuplicate: "stream"},
		{body: `{"model":"gpt-4o-mini"`},
		{body: `{"model":"gpt-4o-mini"} {}`},
	}

	for _, tt := range tests {
		hints := decodeCompleteRequestBodySelectorHints(strings.NewReader(tt.body))
		assert.False(t, hints.complete, "body %q", tt.body)
		assert.Equal(t, tt.wantDuplicate, hints.duplicate, "body %q", tt.body)
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
