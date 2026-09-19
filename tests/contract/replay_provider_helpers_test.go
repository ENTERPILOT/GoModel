//go:build contract

package contract

import (
	"strconv"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/gemini"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func newGeminiReplayProvider(t *testing.T, routes map[string]replayRoute) core.Provider {
	t.Helper()
	return newGeminiReplayProviderWithMode(t, routes, false)
}

// newGeminiNativeReplayProvider replays Gemini's native API surface
// (generateContent, batchEmbedContents, image generation): the same adapter,
// with the native mode toggle on.
func newGeminiNativeReplayProvider(t *testing.T, routes map[string]replayRoute) *gemini.Provider {
	t.Helper()
	return newGeminiReplayProviderWithMode(t, routes, true)
}

func newGeminiReplayProviderWithMode(t *testing.T, routes map[string]replayRoute, native bool) *gemini.Provider {
	t.Helper()

	t.Setenv("USE_GOOGLE_GEMINI_NATIVE_API", strconv.FormatBool(native))
	client := newReplayHTTPClient(t, routes)
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = client
	provider := gemini.New(providers.ProviderConfig{APIKey: "test-api-key"}, opts).(*gemini.Provider)
	provider.SetBaseURL("https://replay.local")
	provider.SetModelsURL("https://replay.local")
	return provider
}
