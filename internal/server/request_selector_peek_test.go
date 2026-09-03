package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestPeekRequestBodySelectorHintsModelOnlyIsNotParsed(t *testing.T) {
	body := `{"model":"gpt-4o-mini","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))

	hints := peekRequestBodySelectorHints(req, requestSelectorPeekLimit)
	if hints.model != "gpt-4o-mini" {
		t.Fatalf("model = %q, want gpt-4o-mini", hints.model)
	}
	if hints.parsed {
		t.Fatal("parsed = true, want false for model-only peek")
	}
	if hints.complete {
		t.Fatal("complete = true, want false for early model-only peek")
	}

	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if string(restored) != body {
		t.Fatalf("restored body = %q, want original body", string(restored))
	}
}

func TestPeekRequestBodySelectorHintsProviderAndModelIsSelectorParsedOnly(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"provider":"openai","model":"gpt-4o-mini","stream":true}`))

	hints := peekRequestBodySelectorHints(req, requestSelectorPeekLimit)
	if !hints.parsed {
		t.Fatal("parsed = false, want true after provider and model are observed")
	}
	if hints.complete {
		t.Fatal("complete = true, want false because stream was not fully scanned")
	}
	if hints.provider != "openai" || hints.model != "gpt-4o-mini" {
		t.Fatalf("selector = (%q, %q), want (gpt-4o-mini, openai)", hints.model, hints.provider)
	}
}

func TestSeedRequestBodySelectorHintsDoesNotMarkModelOnlyPeekAsParsed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","stream":true}`))
	env := &core.WhiteBoxPrompt{}

	if err := seedRequestBodySelectorHints(req, core.BodyModeJSON, env); err != nil {
		t.Fatalf("seedRequestBodySelectorHints() error = %v", err)
	}

	if env.JSONBodyParsed {
		t.Fatal("JSONBodyParsed = true, want false for model-only peek")
	}
	if env.StreamRequested {
		t.Fatal("StreamRequested = true, want false until a full scan")
	}
	if env.RouteHints.Model != "" {
		t.Fatalf("RouteHints.Model = %q, want empty", env.RouteHints.Model)
	}
}

func TestSeedRequestBodySelectorHintsAppliesCompleteModelForOpaqueBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","stream":true}`))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	env := &core.WhiteBoxPrompt{}
	core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "openai"})

	if err := seedRequestBodySelectorHints(req, core.BodyModeOpaque, env); err != nil {
		t.Fatalf("seedRequestBodySelectorHints() error = %v", err)
	}

	if !env.JSONBodyParsed {
		t.Fatal("JSONBodyParsed = false, want true for complete model-only peek")
	}
	if !env.StreamRequested {
		t.Fatal("StreamRequested = false, want true")
	}
	if env.RouteHints.Model != "gpt-4o-mini" {
		t.Fatalf("RouteHints.Model = %q, want gpt-4o-mini", env.RouteHints.Model)
	}
	info := env.CachedPassthroughRouteInfo()
	if info == nil {
		t.Fatal("CachedPassthroughRouteInfo() = nil")
	}
	if info.Model != "gpt-4o-mini" {
		t.Fatalf("PassthroughRouteInfo.Model = %q, want gpt-4o-mini", info.Model)
	}
	if !info.Stream || info.StreamUncertain {
		t.Fatalf("stream state = stream %v uncertain %v, want true and false", info.Stream, info.StreamUncertain)
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if string(restored) != `{"model":"gpt-4o-mini","stream":true}` {
		t.Fatalf("restored body = %q, want original body", string(restored))
	}
}

func TestSeedRequestBodySelectorHintsScansOversizedOpaqueBodyCompletely(t *testing.T) {
	padding := strings.Repeat("x", int(requestSelectorPeekLimit))
	tests := []struct {
		name          string
		body          string
		wantModel     string
		wantStream    bool
		wantDuplicate string
		knownLength   bool
	}{
		{name: "model past the peek window", body: `{"stream":true,"padding":"` + padding + `","model":"late-model"}`, wantModel: "late-model", wantStream: true},
		{name: "model past the peek window with known length", body: `{"stream":false,"padding":"` + padding + `","model":"late-model"}`, wantModel: "late-model", knownLength: true},
		{name: "duplicate model past the peek window", body: `{"model":"allowed-model","padding":"` + padding + `","model":"restricted-model"}`, wantDuplicate: "model"},
		{name: "duplicate stream past the peek window", body: `{"stream":true,"padding":"` + padding + `","stream":false}`, wantDuplicate: "stream"},
		{name: "duplicate provider past the peek window with known length", body: `{"provider":"a","padding":"` + padding + `","provider":"b"}`, wantDuplicate: "provider", knownLength: true},
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

			if test.wantDuplicate != "" {
				assertDuplicateSelectorError(t, err, test.wantDuplicate)
				if env.JSONBodyParsed || env.RouteHints.Model != "" {
					t.Fatalf("env = %+v, want no hints seeded from a duplicate body", env)
				}
			} else {
				if err != nil {
					t.Fatalf("seedRequestBodySelectorHints() error = %v", err)
				}
				if !env.JSONBodyParsed {
					t.Fatal("JSONBodyParsed = false, want true for a fully scanned body")
				}
				info := env.CachedPassthroughRouteInfo()
				if info == nil {
					t.Fatal("CachedPassthroughRouteInfo() = nil")
				}
				if info.Model != test.wantModel || info.Stream != test.wantStream || info.StreamUncertain {
					t.Fatalf("passthrough info = model %q stream %v uncertain %v, want model %q stream %v certain", info.Model, info.Stream, info.StreamUncertain, test.wantModel, test.wantStream)
				}
			}
			assertBodyReplays(t, req, test.body)
		})
	}
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
	if env.StreamRequested || env.JSONBodyParsed {
		t.Fatal("duplicate stream fields must not seed any hint")
	}
	assertBodyReplays(t, req, body)
}

func TestSeedRequestBodySelectorHintsRejectsDuplicateStreamBeyondPeekBoundary(t *testing.T) {
	body := `{"stream":true,"padding":"` + strings.Repeat("x", int(requestSelectorPeekLimit)) + `","stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(body))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	env := &core.WhiteBoxPrompt{}
	core.CachePassthroughRouteInfo(env, &core.PassthroughRouteInfo{Provider: "openai"})

	err := seedRequestBodySelectorHints(req, core.BodyModeOpaque, env)

	assertDuplicateSelectorError(t, err, "stream")
	if env.StreamRequested {
		t.Fatal("StreamRequested = true, want no hint from a duplicate body")
	}
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
			if env.JSONBodyParsed {
				t.Fatal("JSONBodyParsed = true, want false for duplicate fields")
			}
			if env.RouteHints.Model != "" {
				t.Fatalf("RouteHints.Model = %q, want empty", env.RouteHints.Model)
			}
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
	if env.JSONBodyParsed || env.StreamRequested || env.RouteHints.Model != "" {
		t.Fatalf("env = %+v, want no hints seeded from a duplicate body", env)
	}
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
	if env.JSONBodyParsed || env.RouteHints.Provider != "" {
		t.Fatalf("env = %+v, want no hints seeded from a duplicate body", env)
	}
	assertBodyReplays(t, req, body)
}

func assertDuplicateSelectorError(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want duplicate %q rejection", field)
	}
	if !strings.Contains(err.Error(), `duplicate top-level "`+field+`" field`) {
		t.Fatalf("error = %q, want duplicate %q rejection", err.Error(), field)
	}
}

// assertBodyReplays verifies the peek left the request body byte-for-byte
// intact for whoever reads it next.
func assertBodyReplays(t *testing.T, req *http.Request, want string) {
	t.Helper()
	got, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read replayed body: %v", err)
	}
	if string(got) != want {
		t.Fatalf("replayed body = %q, want %q", got, want)
	}
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
		if hints.complete {
			t.Fatalf("decodeCompleteRequestBodySelectorHints(%q).complete = true, want false", tt.body)
		}
		if hints.duplicate != tt.wantDuplicate {
			t.Fatalf("decodeCompleteRequestBodySelectorHints(%q).duplicate = %q, want %q", tt.body, hints.duplicate, tt.wantDuplicate)
		}
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

			if err := seedRequestBodySelectorHints(req, core.BodyModeJSON, env); err != nil {
				t.Fatalf("seedRequestBodySelectorHints() error = %v", err)
			}

			info := env.CachedPassthroughRouteInfo()
			if info == nil {
				t.Fatal("CachedPassthroughRouteInfo() = nil")
			}
			if info.Stream != test.wantStream || info.StreamUncertain != test.wantUncertain {
				t.Errorf("stream metadata = stream %v uncertain %v, want stream %v uncertain %v",
					info.Stream, info.StreamUncertain, test.wantStream, test.wantUncertain)
			}
		})
	}
}
