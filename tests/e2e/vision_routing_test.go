//go:build e2e

package e2e

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// A virtual model with vision routing sends a request carrying an image to
// the target whose metadata reports vision, ahead of a text-only primary,
// in every request dialect; text requests keep the declared order.
func TestVisionRoutingSkipsTextOnlyTargets_E2E(t *testing.T) {
	// gpt-4.1 accepts images; gpt-4 and gpt-3.5-turbo report no capability.
	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithType(NewTestProvider(mockLLMURL, "sk-test-key-12345"), "test")
	registry.SetProviderMetadataOverrides("test", map[string]*core.ModelMetadata{
		"gpt-4.1": {Capabilities: map[string]bool{"vision": true}},
	})
	require.NoError(t, registry.Initialize(context.Background()))

	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	vmDB, err := sqlx.NewSQLite(db)
	require.NoError(t, err)
	vmStore, err := virtualmodels.NewSQLStore(t.Context(), vmDB)
	require.NoError(t, err)
	vmService, err := virtualmodels.NewService(vmStore, registry, true)
	require.NoError(t, err)
	for _, vm := range []virtualmodels.VirtualModel{
		{Source: "vision-chat", VisionRouting: true},
		{Source: "plain-chat"},
	} {
		vm.Strategy = virtualmodels.StrategyFailover
		vm.Targets = []virtualmodels.Target{
			{Provider: "test", Model: "gpt-4"},
			{Provider: "test", Model: "gpt-4.1"},
		}
		vm.Enabled = true
		require.NoError(t, vmService.Upsert(t.Context(), vm))
	}

	ts := httptest.NewServer(setupE2EServer(t, e2eServerOptions{
		registry:      registry,
		modelResolver: vmService,
	}))
	defer ts.Close()

	// Past the ingress and audit capture limits, so the body is read in
	// full only because vision routing asked for it.
	image := "data:image/png;base64," + strings.Repeat("A", 2<<20)
	chatImage := func(model string) map[string]any {
		return map[string]any{
			"model": model,
			"messages": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "what is this?"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": image}},
			}}},
		}
	}
	messagesImage := map[string]any{
		"model":      "vision-chat",
		"max_tokens": 16,
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "image", "source": map[string]any{
				"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo=",
			}},
			map[string]any{"type": "text", "text": "what is this?"},
		}}},
	}
	chatText := map[string]any{
		"model":    "vision-chat",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}

	tests := []struct {
		name    string
		path    string
		payload map[string]any
		want    string
	}{
		{name: "chat image", path: chatCompletionsPath, payload: chatImage("vision-chat"), want: "gpt-4.1"},
		{name: "messages image", path: messagesPath, payload: messagesImage, want: "gpt-4.1"},
		{name: "chat text", path: chatCompletionsPath, payload: chatText, want: "gpt-4"},
		{name: "vision routing off", path: chatCompletionsPath, payload: chatImage("plain-chat"), want: "gpt-4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockServer.ResetRequests()
			resp := sendBudgetJSONRequestWithHeaders(t, http.MethodPost, ts.URL+tt.path, tt.payload, nil)
			closeBody(resp)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, []string{tt.want}, recordedChatModels(t))
		})
	}
}
