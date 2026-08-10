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

	seedRequestBodySelectorHints(req, core.BodyModeJSON, env)

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

			seedRequestBodySelectorHints(req, core.BodyModeJSON, env)

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
