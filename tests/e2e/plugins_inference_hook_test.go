//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookServer is a fake AI security server answering every frame with one
// status and verdict body.
type hookServer struct {
	url    string
	mu     sync.Mutex
	frames []map[string]any
	signed []bool
}

func newHookServer(t *testing.T, status int, verdict string) *hookServer {
	t.Helper()
	h := &hookServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var frame map[string]any
		_ = json.Unmarshal(body, &frame)
		h.mu.Lock()
		h.frames = append(h.frames, frame)
		h.signed = append(h.signed, r.Header.Get("webhook-signature") != "" && r.Header.Get("webhook-id") == frame["request_id"])
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, verdict)
	}))
	t.Cleanup(srv.Close)
	h.url = srv.URL
	return h
}

// userFrames returns the frames of client requests, leaving out the
// connection tests the health probe sends.
func (h *hookServer) userFrames() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []map[string]any
	for i, frame := range h.frames {
		source, _ := frame["source"].(map[string]any)
		if source["application"] == "gomodel" && h.signed[i] {
			out = append(out, frame)
		}
	}
	return out
}

// hookSecret is a Standard Webhooks secret: whsec_ plus base64 key bytes.
var hookSecret = "whsec_" + base64.StdEncoding.EncodeToString([]byte("a signing key for the tests only"))

func (h *hookServer) config() map[string]any {
	return map[string]any{"endpoint_url": h.url, "signing_secret": hookSecret}
}

func TestPlugins_InferenceHook_E2E(t *testing.T) {
	fx := setupPluginServer(t)

	t.Run("deny verdict rejects the request before the provider", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		hook := newHookServer(t, http.StatusOK, `{"action":"deny","deny_reason":"Card numbers are not allowed.","reference_id":"scan_1"}`)
		fx.mustPutGuardrail(t, guardrailDef("dlp", "inference_hook", hook.config(), nil))
		fx.activate(t, workflowStep{Ref: "dlp", Phase: "prompt", Step: 1})

		mockServer.ResetRequests()
		envelope := readError(t, fx.chat(t, "my card is 4111111111111111", false), http.StatusBadRequest)
		assert.Equal(t, "inference_hook_denied", envelope.Error.Code)
		assert.Equal(t, "Card numbers are not allowed.", envelope.Error.Message)
		assert.Empty(t, mockServer.Requests(), "a denied prompt never reaches the provider")

		frames := hook.userFrames()
		require.Len(t, frames, 1)
		assert.Equal(t, "prompt", frames[0]["type"])
		transcript, err := json.Marshal(frames[0]["messages"])
		require.NoError(t, err)
		assert.Contains(t, string(transcript), "my card is 4111111111111111")
	})

	t.Run("allow verdict passes the request through", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		hook := newHookServer(t, http.StatusOK, `{"action":"allow"}`)
		fx.mustPutGuardrail(t, guardrailDef("dlp", "inference_hook", hook.config(), nil))
		fx.activate(t, workflowStep{Ref: "dlp", Phase: "prompt", Step: 1})

		resp := fx.chat(t, "what is the weather", false)
		assert.Empty(t, resp.Header.Get(guardrailHeader))
		_, text := readChat(t, resp)
		assert.Equal(t, "Mock response to: what is the weather", text)
		assert.Len(t, hook.userFrames(), 1)
	})

	t.Run("warn action is a shadow mode", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		hook := newHookServer(t, http.StatusOK, `{"action":"deny","deny_reason":"Would block."}`)
		cfg := hook.config()
		cfg["action"] = "warn"
		fx.mustPutGuardrail(t, guardrailDef("dlp", "inference_hook", cfg, nil))
		fx.activate(t, workflowStep{Ref: "dlp", Phase: "prompt", Step: 1})

		resp := fx.chat(t, "hello", false)
		assert.Contains(t, resp.Header.Get(guardrailHeader), "inference_hook_denied")
		_, text := readChat(t, resp)
		assert.Equal(t, "Mock response to: hello", text)
	})

	t.Run("a failed hook fails closed by default", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		hook := newHookServer(t, http.StatusForbidden, `{"action":"deny"}`)
		fx.mustPutGuardrail(t, guardrailDef("dlp", "inference_hook", hook.config(), nil))
		fx.activate(t, workflowStep{Ref: "dlp", Phase: "prompt", Step: 1})

		mockServer.ResetRequests()
		envelope := readError(t, fx.chat(t, "hello", false), http.StatusInternalServerError)
		assert.Equal(t, "plugin_failure", envelope.Error.Code)
		assert.Empty(t, mockServer.Requests(), "an uninspected prompt never reaches the provider")
	})

	t.Run("fail_mode open lets an uninspected request through", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		hook := newHookServer(t, http.StatusForbidden, `{"action":"deny"}`)
		fx.mustPutGuardrail(t, guardrailDef("dlp", "inference_hook", hook.config(), map[string]any{"fail_mode": "open"}))
		fx.activate(t, workflowStep{Ref: "dlp", Phase: "prompt", Step: 1})

		_, text := readChat(t, fx.chat(t, "hello", false))
		assert.Equal(t, "Mock response to: hello", text)
	})

	t.Run("the signing secret is never returned by the admin API", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		hook := newHookServer(t, http.StatusOK, `{"action":"allow"}`)
		fx.mustPutGuardrail(t, guardrailDef("dlp", "inference_hook", hook.config(), nil))

		status, body := fx.admin(t, http.MethodGet, adminGuardrailsPath, nil)
		require.Equal(t, http.StatusOK, status)
		assert.NotContains(t, string(body), hookSecret)
		assert.Contains(t, string(body), `"inference_hook"`)
	})
}
