package minimax

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oversizedStatusBody builds a MiniMax HTTP-200 failure whose status_msg
// exceeds the message bound. The diagnostic is multi-byte so truncation must
// land on a rune boundary.
func oversizedStatusBody(t *testing.T, statusCode int) (body string, diagnostic string) {
	t.Helper()
	diagnostic = strings.Repeat("参数错误", maxStatusMessageLength)
	require.Greater(t, len(diagnostic), maxStatusMessageLength)
	encoded, err := json.Marshal(map[string]any{
		"base_resp": map[string]any{"status_code": statusCode, "status_msg": diagnostic},
	})
	require.NoError(t, err)
	return string(encoded), diagnostic
}

func assertBoundedDiagnostic(t *testing.T, gatewayErr *core.GatewayError, diagnostic string) {
	t.Helper()
	assert.LessOrEqual(t, len(gatewayErr.Message), maxStatusMessageLength+len("minimax speech request failed (status 2013): "))
	assert.True(t, utf8.ValidString(gatewayErr.Message), "truncation split a rune")
	assert.NotContains(t, gatewayErr.Message, diagnostic)
	assert.Contains(t, gatewayErr.Message, "参数错误")
}

func TestCreateImageBoundsNativeMessage(t *testing.T) {
	body, diagnostic := oversizedStatusBody(t, 2013)
	server, _ := providertest.JSONServer(t, http.StatusOK, body)
	p := NewWithHTTPClient("test-key", server.URL, server.Client(), llmclient.Hooks{})

	_, err := p.CreateImage(context.Background(), &core.ImageGenerationRequest{Model: "image-01", Prompt: "A lighthouse"})

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assertBoundedDiagnostic(t, gatewayErr, diagnostic)
	// The full diagnostic stays available for auditing.
	assert.JSONEq(t, body, string(gatewayErr.ResponseBody))
}

func TestCreateSpeechBoundsNativeMessage(t *testing.T) {
	body, diagnostic := oversizedStatusBody(t, 2013)
	server, _ := providertest.JSONServer(t, http.StatusOK, body)
	p := NewWithHTTPClient("test-key", server.URL, server.Client(), llmclient.Hooks{})

	_, err := p.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "speech-2.8-hd",
		Input: "hello",
		Voice: "voice-id",
	})

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assertBoundedDiagnostic(t, gatewayErr, diagnostic)
}

func TestTruncateStatusMessage(t *testing.T) {
	assert.Equal(t, "short", truncateStatusMessage("short"))

	exact := strings.Repeat("a", maxStatusMessageLength)
	assert.Equal(t, exact, truncateStatusMessage(exact))

	// A multi-byte rune straddling the limit is dropped whole, never split.
	straddling := strings.Repeat("a", maxStatusMessageLength-1) + "参数"
	truncated := truncateStatusMessage(straddling)
	assert.Len(t, truncated, maxStatusMessageLength-1)
	assert.True(t, utf8.ValidString(truncated))
}
