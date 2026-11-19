package inferencehook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/enterpilot/gomodel/pluginapi/plugintest"
)

// testSecret is a Standard Webhooks secret: whsec_ plus base64 key bytes.
var testSecret = "whsec_" + base64.StdEncoding.EncodeToString([]byte("a signing key for the tests only"))

// delivery is one request the fake security server received.
type delivery struct {
	header http.Header
	body   []byte
}

// securityServer is a fake AI security server: it records every delivery
// and answers with a fixed status and body.
type securityServer struct {
	srv        *httptest.Server
	mu         sync.Mutex
	deliveries []delivery
	status     int
	reply      string
	delay      time.Duration
}

func newSecurityServer(t *testing.T, reply string) *securityServer {
	t.Helper()
	s := &securityServer{status: http.StatusOK, reply: reply}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *securityServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.deliveries = append(s.deliveries, delivery{header: r.Header.Clone(), body: body})
	status, reply, delay := s.status, s.reply, s.delay
	s.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if status >= 300 && status < 400 {
		w.Header().Set("Location", "/elsewhere")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, reply)
}

func (s *securityServer) received() []delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]delivery(nil), s.deliveries...)
}

// frames decodes every delivery body.
func (s *securityServer) frames(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, d := range s.received() {
		var f map[string]any
		require.NoError(t, json.Unmarshal(d.body, &f))
		out = append(out, f)
	}
	return out
}

func newPlugin(t *testing.T, s *securityServer, cfg string) *Plugin {
	t.Helper()
	m := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(cfg), &m))
	if _, ok := m["endpoint_url"]; !ok && s != nil {
		m["endpoint_url"] = s.srv.URL + "/verdict"
	}
	if _, ok := m["signing_secret"]; !ok {
		m["signing_secret"] = testSecret
	}
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	return plugintest.Init(t, New, string(raw), nil).(*Plugin)
}

// verifySignature is the receiving side of Standard Webhooks, as
// Anthropic's Inference hooks documentation gives it for a server.
func verifySignature(secret string, header http.Header, body []byte) bool {
	id, timestamp, signatures := header.Get("webhook-id"), header.Get("webhook-timestamp"), header.Get("webhook-signature")
	if id == "" || timestamp == "" || signatures == "" {
		return false
	}
	signedAt, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if age := time.Now().Unix() - signedAt; age > 300 || age < -300 {
		return false
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(body)
	expected := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for candidate := range strings.FieldsSeq(signatures) {
		if hmac.Equal([]byte(candidate), []byte(expected)) {
			return true
		}
	}
	return false
}

func conversation() *pluginapi.Prompt {
	return plugintest.Prompt(
		plugintest.Text(pluginapi.RoleSystem, "m0", "Be brief."),
		plugintest.Text(pluginapi.RoleUser, "m1", "Look up order 42."),
		pluginapi.Message{ID: "m2", Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
			{Kind: pluginapi.PartReasoning, Text: "I need the order tool."},
			{Kind: pluginapi.PartText, Text: "Checking."},
			{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "call_1", Name: "get_order", Arguments: json.RawMessage(`{"id":42}`)}},
		}},
		pluginapi.Message{ID: "m3", Role: pluginapi.RoleTool, ToolCallID: "call_1", Parts: []pluginapi.Part{
			{Kind: pluginapi.PartToolResult, ToolResult: &pluginapi.ToolResult{CallID: "call_1", Parts: []pluginapi.Part{
				{Kind: pluginapi.PartText, Text: "shipped"},
				{Kind: pluginapi.PartImage, MediaType: "image/png"},
			}}},
		}},
		pluginapi.Message{ID: "m4", Role: pluginapi.RoleUser, Parts: []pluginapi.Part{
			{Kind: pluginapi.PartText, Text: "And this label?"},
			{Kind: pluginapi.PartImage, MediaType: "image/png", Data: []byte("raw-bytes")},
		}},
		plugintest.Text(pluginapi.RoleUser, "m5", ""),
	)
}

func TestManifest(t *testing.T) {
	m := New().Manifest()
	require.Equal(t, "inference_hook", m.Name)
	require.False(t, m.Mutates)
	require.True(t, m.Guardrail)
	assert.Equal(t, []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse, pluginapi.KindStream}, m.Kinds)

	want := []string{"endpoint_url", "signing_secret", "auth_header", "auth_value", "tenant_id", "roles", "action", "message", "block_status", "verdict_timeout_ms"}
	var keys []string
	for _, f := range m.ConfigSchema {
		keys = append(keys, f.Key)
		assert.NotEmpty(t, f.Label)
		assert.NotEmpty(t, f.Help, "field %s lacks help", f.Key)
		if f.Input == pluginapi.InputSelect || f.Input == pluginapi.InputCheckboxes {
			assert.NotEmpty(t, f.Options, "field %s lacks options", f.Key)
		}
		if f.Key == "signing_secret" || f.Key == "auth_value" {
			assert.Equal(t, pluginapi.InputSecret, f.Input, "field %s must be a secret", f.Key)
		}
		assert.Equal(t, f.Key == "endpoint_url", f.Destination, "only the endpoint decides where secrets go")
	}
	assert.Equal(t, want, keys)

	p := New()
	for name, ok := range map[string]bool{
		"PromptHook":    func() bool { _, ok := p.(pluginapi.PromptHook); return ok }(),
		"ResponseHook":  func() bool { _, ok := p.(pluginapi.ResponseHook); return ok }(),
		"StreamHook":    func() bool { _, ok := p.(pluginapi.StreamHook); return ok }(),
		"HealthChecker": func() bool { _, ok := p.(pluginapi.HealthChecker); return ok }(),
	} {
		assert.True(t, ok, "must implement %s", name)
	}
	assert.Equal(t, pluginapi.StreamBuffer, p.(pluginapi.StreamHook).StreamPolicy().Mode)
}

func TestInitErrors(t *testing.T) {
	const url = `"endpoint_url": "https://security.example.com/verdict"`
	secret := `"signing_secret": "` + testSecret + `"`
	tests := []struct {
		name string
		cfg  string
		want string
	}{
		{"unknown key", `{"bogus": 1}`, `unknown field "bogus"`},
		{"no endpoint", `{` + secret + `}`, "endpoint_url is required"},
		{"endpoint scheme", `{"endpoint_url": "security.example.com", ` + secret + `}`, "endpoint_url must be an http:// or https:// URL"},
		{"endpoint credentials", `{"endpoint_url": "https://user:pass@security.example.com", ` + secret + `}`, "endpoint_url must not carry credentials"},
		{"no secret", `{` + url + `}`, "signing_secret is required"},
		{"secret not base64", `{` + url + `, "signing_secret": "whsec_not base64!"}`, "signing_secret must be standard base64"},
		{"bad action", `{` + url + `, ` + secret + `, "action": "redact"}`, "action must be one of block, respond, warn"},
		{"bad role", `{` + url + `, ` + secret + `, "roles": ["robot"]}`, "unknown role"},
		{"no role", `{` + url + `, ` + secret + `, "roles": []}`, "roles must select at least one role"},
		{"status low", `{` + url + `, ` + secret + `, "block_status": 302}`, "block_status must be an HTTP status between 400 and 599"},
		{"timeout zero", `{` + url + `, ` + secret + `, "verdict_timeout_ms": 0}`, "verdict_timeout_ms must be between 1 and 60000"},
		{"timeout high", `{` + url + `, ` + secret + `, "verdict_timeout_ms": 60001}`, "verdict_timeout_ms must be between 1 and 60000"},
		{"auth header name", `{` + url + `, ` + secret + `, "auth_header": "X Key", "auth_value": "k"}`, "auth_header"},
		{"auth header reserved", `{` + url + `, ` + secret + `, "auth_header": "webhook-signature", "auth_value": "k"}`, "auth_header"},
		{"auth value multi-line", `{` + url + `, ` + secret + `, "auth_value": "a\nb: c"}`, "auth_value must be a single line"},
		{"auth over plain http", `{"endpoint_url": "http://security.example.com", ` + secret + `, "auth_value": "k"}`, "auth_value needs an https:// endpoint_url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New().Init(context.Background(), json.RawMessage(tt.cfg), plugintest.NewHost())
			require.ErrorContains(t, err, tt.want)
			assert.NotContains(t, err.Error(), testSecret)
		})
	}
}

func TestDefaults(t *testing.T) {
	p := newPlugin(t, nil, `{"endpoint_url": "http://localhost:8000/verdict", "auth_value": "Bearer tok"}`)
	assert.Equal(t, "http://localhost:8000/verdict", p.endpointURL)
	assert.Equal(t, "Authorization", p.authHeader)
	assert.Equal(t, 5*time.Second, p.timeout)
	assert.Equal(t, pluginapi.ActionBlock, p.enforcement.Action)
	assert.Equal(t, DefaultMessage, p.enforcement.Message)
	assert.Zero(t, p.enforcement.BlockStatus)
	assert.Empty(t, p.tenantID)
	want := map[pluginapi.Role]bool{pluginapi.RoleSystem: true, pluginapi.RoleDeveloper: true, pluginapi.RoleUser: true, pluginapi.RoleAssistant: true, pluginapi.RoleTool: true}
	assert.Equal(t, want, p.roles)
	// The stored config a summary is rendered from may hold the secret as an
	// unresolved reference.
	assert.Equal(t, "block, localhost:8000", p.Summarize(json.RawMessage(`{"endpoint_url": "http://localhost:8000/verdict", "signing_secret": "${env:HOOK_SECRET}"}`)))
	assert.Equal(t, "warn, security.example.com", p.Summarize(json.RawMessage(`{"endpoint_url": "https://security.example.com/verdict", "action": "warn"}`)))
	assert.Empty(t, p.Summarize(json.RawMessage(`{"endpoint_url": "nowhere"}`)))

	// A secret without the whsec_ prefix is the same key.
	bare := newPlugin(t, nil, `{"endpoint_url": "http://localhost:8000", "signing_secret": "`+strings.TrimPrefix(testSecret, "whsec_")+`"}`)
	assert.Equal(t, p.signingKey, bare.signingKey)
}

func TestOnPromptSendsSignedPromptFrame(t *testing.T) {
	s := newSecurityServer(t, `{"action": "allow"}`)
	p := newPlugin(t, s, `{"auth_header": "X-Api-Key", "auth_value": "tok", "tenant_id": "org-1"}`)
	x := plugintest.Exchange(conversation(), nil)
	x.Meta = pluginapi.Meta{RequestID: "req-1", AuthKeyID: "key-7", UserPath: "/team/alpha", SessionID: "sess-9", Provider: "openai", Model: "gpt-4o", RequestedModel: "smart"}

	d, err := p.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	require.Equal(t, pluginapi.ActionAllow, d.Action)

	got := s.received()
	require.Len(t, got, 1)
	header := got[0].header
	assert.Equal(t, "application/json", header.Get("Content-Type"))
	assert.Equal(t, "gomodel-inference-hook/1", header.Get("User-Agent"))
	assert.Equal(t, "identity", header.Get("Accept-Encoding"))
	assert.Equal(t, "tok", header.Get("X-Api-Key"))
	assert.True(t, verifySignature(testSecret, header, got[0].body), "signature must verify against the raw body")
	assert.False(t, verifySignature("whsec_"+base64.StdEncoding.EncodeToString([]byte("another key")), header, got[0].body))

	var f map[string]any
	require.NoError(t, json.Unmarshal(got[0].body, &f))
	requestID, _ := f["request_id"].(string)
	require.NotEmpty(t, requestID)
	assert.Equal(t, requestID, header.Get("webhook-id"), "request_id is the webhook-id")
	assert.Equal(t, map[string]any{"verdict": "allow", "hook_request_id": requestID}, d.Detail)

	f["request_id"] = "ID"
	normalized, err := json.Marshal(f)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"type": "prompt",
		"request_id": "ID",
		"tenant_id": "org-1",
		"actor": {"type": "user", "id": "key-7", "email_address": null},
		"source": {"application": "gomodel"},
		"session_id": "sess-9",
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Be brief."}]},
			{"role": "user", "content": [{"type": "text", "text": "Look up order 42."}]},
			{"role": "assistant", "content": [
				{"type": "text", "text": "I need the order tool."},
				{"type": "text", "text": "Checking."},
				{"type": "tool_use", "id": "call_1", "tool_name": "get_order", "input": {"id": 42}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "content": "shipped\n[image]", "is_error": false, "tool_name": "get_order", "tool_use_id": "call_1"}
			]},
			{"role": "user", "content": [
				{"type": "text", "text": "And this label?"},
				{"type": "attachment", "file_name": null, "media_type": "image/png", "size_bytes": null, "text": null}
			]}
		],
		"metadata": {"gomodel_request_id": "req-1", "gomodel_user_path": "/team/alpha", "gomodel_provider": "openai"}
	}`, string(normalized))
	assert.NotContains(t, string(got[0].body), "raw-bytes", "media bytes are never sent")
}

func TestOnPromptUnknownIdentityIsNull(t *testing.T) {
	s := newSecurityServer(t, `{"action": "allow"}`)
	p := newPlugin(t, s, `{}`)
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "hi")), nil)
	x.Meta = pluginapi.Meta{RequestedModel: "smart"}

	_, err := p.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	frames := s.frames(t)
	require.Len(t, frames, 1)
	f := frames[0]
	for _, key := range []string{"tenant_id", "session_id"} {
		value, present := f[key]
		assert.True(t, present, "%s must be sent", key)
		assert.Nil(t, value, "%s must be null", key)
	}
	assert.Equal(t, "smart", f["model"], "the requested model stands in before routing")
	assert.Equal(t, map[string]any{"type": "user", "id": nil, "email_address": nil}, f["actor"])
	assert.Equal(t, map[string]any{}, f["metadata"])
	assert.Empty(t, s.received()[0].header.Get("Authorization"), "no auth header without an auth value")
}

func TestOnPromptRoles(t *testing.T) {
	tests := []struct {
		name      string
		cfg       string
		wantRoles []string
		wantSent  bool
	}{
		{"all roles by default", `{}`, []string{"user", "user", "assistant", "user", "user"}, true},
		{"without system", `{"roles": ["user", "assistant", "tool"]}`, []string{"user", "assistant", "user", "user"}, true},
		{"user only", `{"roles": "user"}`, []string{"user", "user"}, true},
		{"assistant only finds nothing in a user turn", `{"roles": ["assistant"]}`, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSecurityServer(t, `{"action": "deny"}`)
			p := newPlugin(t, s, tt.cfg)
			prompt := conversation()
			if !tt.wantSent {
				prompt = plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "hi"))
			}
			d, err := p.OnPrompt(context.Background(), plugintest.Exchange(prompt, nil))
			require.NoError(t, err)
			frames := s.frames(t)
			if !tt.wantSent {
				assert.Empty(t, frames)
				assert.Equal(t, pluginapi.ActionAllow, d.Action)
				return
			}
			require.Len(t, frames, 1)
			var roles []string
			for _, m := range frames[0]["messages"].([]any) {
				roles = append(roles, m.(map[string]any)["role"].(string))
			}
			assert.Equal(t, tt.wantRoles, roles)
		})
	}
}

// A role GoModel does not model is not a way around the hook: its content
// is sent whatever roles are selected.
func TestOnPromptSendsUnknownRoles(t *testing.T) {
	s := newSecurityServer(t, `{"action": "allow"}`)
	p := newPlugin(t, s, `{"roles": ["assistant"]}`)
	x := plugintest.Exchange(plugintest.Prompt(
		plugintest.Text(pluginapi.RoleUser, "m1", "skipped by the roles setting"),
		plugintest.Text(pluginapi.Role("function"), "m2", "card 4111111111111111"),
	), nil)

	_, err := p.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	frames := s.frames(t)
	require.Len(t, frames, 1)
	messages, err := json.Marshal(frames[0]["messages"])
	require.NoError(t, err)
	assert.JSONEq(t, `[{"role": "user", "content": [{"type": "text", "text": "card 4111111111111111"}]}]`, string(messages))
}

func TestOnPromptWithoutPrompt(t *testing.T) {
	s := newSecurityServer(t, `{"action": "deny"}`)
	p := newPlugin(t, s, `{}`)
	d, err := p.OnPrompt(context.Background(), plugintest.Exchange(nil, nil))
	require.NoError(t, err)
	assert.Equal(t, pluginapi.ActionAllow, d.Action)
	assert.Empty(t, s.received())
}

func TestVerdicts(t *testing.T) {
	long := strings.Repeat("é", 600)
	tests := []struct {
		name        string
		cfg         string
		reply       string
		wantAction  pluginapi.Action
		wantStatus  int
		wantMessage string
		wantReply   string
		wantDetail  map[string]any
	}{
		{
			name: "allow", cfg: `{}`, reply: `{"action": "allow", "deny_reason": "ignored on allow", "extra": {"score": 0.1}}`,
			wantAction: pluginapi.ActionAllow,
			wantDetail: map[string]any{"verdict": "allow"},
		},
		{
			name: "deny blocks with the server's reason", cfg: `{"block_status": 403}`,
			reply:      `{"action": "deny", "deny_reason": "Payment card data is not allowed.", "reference_id": "scan_01HXPT4R9V"}`,
			wantAction: pluginapi.ActionBlock, wantStatus: 403, wantMessage: "Payment card data is not allowed.",
			wantDetail: map[string]any{"verdict": "deny", "reason": "Payment card data is not allowed.", "reference_id": "scan_01HXPT4R9V"},
		},
		{
			name: "deny without a reason uses the message", cfg: `{"message": "Blocked by ACME policy"}`, reply: `{"action": "deny"}`,
			wantAction: pluginapi.ActionBlock, wantMessage: "Blocked by ACME policy",
			wantDetail: map[string]any{"verdict": "deny"},
		},
		{
			name: "null reason uses the default message", cfg: `{}`, reply: `{"action": "deny", "deny_reason": null, "reference_id": null}`,
			wantAction: pluginapi.ActionBlock, wantMessage: DefaultMessage,
			wantDetail: map[string]any{"verdict": "deny"},
		},
		{
			name: "respond answers with the reason", cfg: `{"action": "respond"}`, reply: `{"action": "deny", "deny_reason": "Remove the card number."}`,
			wantAction: pluginapi.ActionRespond, wantReply: "Remove the card number.",
			wantDetail: map[string]any{"verdict": "deny", "reason": "Remove the card number."},
		},
		{
			name: "warn is shadow mode", cfg: `{"action": "warn"}`, reply: `{"action": "deny", "deny_reason": "Would block."}`,
			wantAction: pluginapi.ActionWarn, wantMessage: "Would block.",
			wantDetail: map[string]any{"verdict": "deny", "reason": "Would block."},
		},
		{
			name: "oversize reason is truncated", cfg: `{}`, reply: `{"action": "deny", "deny_reason": "` + long + `"}`,
			wantAction: pluginapi.ActionBlock, wantMessage: strings.Repeat("é", 500),
			wantDetail: map[string]any{"verdict": "deny", "reason": strings.Repeat("é", 500)},
		},
		{
			name: "malformed fields never discard a deny", cfg: `{}`, reply: `{"action": "deny", "deny_reason": 7, "reference_id": "has spaces and <tags>"}`,
			wantAction: pluginapi.ActionBlock, wantMessage: DefaultMessage,
			wantDetail: map[string]any{"verdict": "deny"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSecurityServer(t, tt.reply)
			p := newPlugin(t, s, tt.cfg)
			d, err := p.OnPrompt(context.Background(), plugintest.Exchange(conversation(), nil))
			require.NoError(t, err)
			assert.Equal(t, tt.wantAction, d.Action)
			assert.Equal(t, tt.wantStatus, d.Status)
			if tt.wantAction != pluginapi.ActionAllow {
				assert.Equal(t, Code, d.Code)
			}
			if tt.wantAction == pluginapi.ActionRespond {
				require.NotNil(t, d.Response)
				assert.Equal(t, tt.wantReply, d.Response.Text(0))
			} else {
				assert.Equal(t, tt.wantMessage, d.Message)
			}
			frames := s.frames(t)
			require.Len(t, frames, 1)
			tt.wantDetail["hook_request_id"] = frames[0]["request_id"]
			assert.Equal(t, tt.wantDetail, d.Detail)
		})
	}
}

// A hook that cannot deliver a verdict is an error, never a deny or an
// allow: the instance fail mode decides what happens to the request.
func TestHookFailuresAreErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		reply  string
		delay  time.Duration
		want   string
	}{
		{"error status with a deny body", http.StatusForbidden, `{"action": "deny", "deny_reason": "transcript echo"}`, 0, "returned HTTP 403"},
		{"server error", http.StatusInternalServerError, `transcript echo`, 0, "returned HTTP 500"},
		{"redirect is not followed", http.StatusFound, ``, 0, "returned HTTP 302"},
		{"not json", http.StatusOK, `transcript echo`, 0, "verdict is not a JSON object"},
		{"json array", http.StatusOK, `["allow"]`, 0, "verdict is not a JSON object"},
		{"missing action", http.StatusOK, `{"deny_reason": "transcript echo"}`, 0, `verdict action must be "allow" or "deny"`},
		{"unknown action", http.StatusOK, `{"action": "block"}`, 0, `verdict action must be "allow" or "deny"`},
		{"action of another type", http.StatusOK, `{"action": true}`, 0, `verdict action must be "allow" or "deny"`},
		{"oversized body", http.StatusOK, `{"action": "allow", "pad": "` + strings.Repeat("x", maxVerdictBytes) + `"}`, 0, "verdict is larger than"},
		{"verdict slower than the timeout", http.StatusOK, `{"action": "allow"}`, 5 * time.Second, "security server unreachable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSecurityServer(t, tt.reply)
			s.status, s.delay = tt.status, tt.delay
			cfg := `{}`
			if tt.delay > 0 {
				cfg = `{"verdict_timeout_ms": 20}`
			}
			p := newPlugin(t, s, cfg)
			d, err := p.OnPrompt(context.Background(), plugintest.Exchange(conversation(), nil))
			require.ErrorContains(t, err, tt.want)
			assert.Equal(t, pluginapi.Decision{}, d)
			assert.NotContains(t, err.Error(), "transcript echo", "errors must not carry the response body")
			assert.NotContains(t, err.Error(), s.srv.URL, "errors must not carry the endpoint URL")
			assert.Len(t, s.received(), 1, "a failed hook is not retried, and a redirect is not followed")
		})
	}
}

func TestUnreachableServerIsAnError(t *testing.T) {
	s := newSecurityServer(t, `{"action": "allow"}`)
	p := newPlugin(t, s, `{}`)
	url := s.srv.URL
	s.srv.Close()
	_, err := p.OnPrompt(context.Background(), plugintest.Exchange(conversation(), nil))
	require.ErrorContains(t, err, "security server unreachable")
	assert.NotContains(t, err.Error(), url)
}

func toolCompletion() *pluginapi.Completion {
	return &pluginapi.Completion{Choices: []pluginapi.Choice{
		{Index: 0, FinishReason: "stop", Message: plugintest.Text(pluginapi.RoleAssistant, "", "No tools here.")},
		{Index: 1, FinishReason: "tool_calls", Message: pluginapi.Message{Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
			{Kind: pluginapi.PartReasoning, Text: "hidden reasoning"},
			{Kind: pluginapi.PartText, Text: "Running it."},
			{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "call_1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)}},
			{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "call_2", Name: "crm_search", Arguments: json.RawMessage(`{"q": `)}},
			{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "call_3", Name: "ping"}},
		}}},
	}}
}

func TestOnResponseSendsToolCallFrame(t *testing.T) {
	s := newSecurityServer(t, `{"action": "deny", "deny_reason": "bash is not allowed."}`)
	p := newPlugin(t, s, `{}`)
	x := plugintest.Exchange(conversation(), toolCompletion())

	d, err := p.OnResponse(context.Background(), x)
	require.NoError(t, err)
	assert.Equal(t, pluginapi.ActionBlock, d.Action)
	assert.Equal(t, "bash is not allowed.", d.Message)

	got := s.received()
	require.Len(t, got, 1, "only the choice with tool calls is sent")
	assert.True(t, verifySignature(testSecret, got[0].header, got[0].body))
	var f map[string]any
	require.NoError(t, json.Unmarshal(got[0].body, &f))
	assert.Equal(t, "tool_call", f["type"])
	messages, err := json.Marshal(f["messages"])
	require.NoError(t, err)
	assert.JSONEq(t, `[{"role": "assistant", "content": [
		{"type": "text", "text": "Running it."},
		{"type": "tool_use", "id": "call_1", "tool_name": "bash", "input": {"command": "ls"}, "tool_info": {"type": "client"}},
		{"type": "tool_use", "id": "call_2", "tool_name": "crm_search", "input": "{\"q\": ", "tool_info": {"type": "client"}},
		{"type": "tool_use", "id": "call_3", "tool_name": "ping", "input": null, "tool_info": {"type": "client"}}
	]}]`, string(messages), "the frame holds the assistant message only, without its reasoning")
}

func TestOnResponseWithoutToolCallsSendsNothing(t *testing.T) {
	s := newSecurityServer(t, `{"action": "deny"}`)
	p := newPlugin(t, s, `{}`)
	for name, resp := range map[string]*pluginapi.Completion{
		"text only":   plugintest.Completion("Just text.", "More text."),
		"no response": nil,
	} {
		t.Run(name, func(t *testing.T) {
			d, err := p.OnResponse(context.Background(), plugintest.Exchange(conversation(), resp))
			require.NoError(t, err)
			assert.Equal(t, pluginapi.ActionAllow, d.Action)
		})
	}
	assert.Empty(t, s.received())
}

func TestOnResponseStopsAtTheFirstDeny(t *testing.T) {
	calls := &pluginapi.Completion{}
	for i := range 3 {
		calls.Choices = append(calls.Choices, pluginapi.Choice{Index: i, Message: pluginapi.Message{Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
			{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "call", Name: "bash"}},
		}}})
	}
	tests := []struct {
		name       string
		reply      string
		wantAction pluginapi.Action
		wantFrames int
	}{
		{"every choice is checked while allowed", `{"action": "allow"}`, pluginapi.ActionAllow, 3},
		{"a deny ends the check", `{"action": "deny"}`, pluginapi.ActionBlock, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSecurityServer(t, tt.reply)
			p := newPlugin(t, s, `{}`)
			d, err := p.OnResponse(context.Background(), plugintest.Exchange(nil, calls))
			require.NoError(t, err)
			assert.Equal(t, tt.wantAction, d.Action)
			assert.Len(t, s.received(), tt.wantFrames)
		})
	}
}

func TestOnResponseFailureIsAnError(t *testing.T) {
	s := newSecurityServer(t, `{}`)
	p := newPlugin(t, s, `{}`)
	_, err := p.OnResponse(context.Background(), plugintest.Exchange(nil, toolCompletion()))
	require.ErrorContains(t, err, "verdict action must be")
}

func TestHealth(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		reply   string
		wantErr string
	}{
		{"allow is healthy", http.StatusOK, `{"action": "allow"}`, ""},
		{"deny is healthy too", http.StatusOK, `{"action": "deny", "deny_reason": "test"}`, ""},
		{"rejected signature", http.StatusUnauthorized, `{}`, "returned HTTP 401"},
		{"not a verdict", http.StatusOK, `ok`, "verdict is not a JSON object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSecurityServer(t, tt.reply)
			s.status = tt.status
			p := newPlugin(t, s, `{"tenant_id": "org-1"}`)
			err := p.Health(context.Background())
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
			got := s.received()
			require.Len(t, got, 1)
			assert.True(t, verifySignature(testSecret, got[0].header, got[0].body), "the test frame is signed like any other")
			f := s.frames(t)[0]
			assert.Equal(t, "prompt", f["type"])
			assert.Equal(t, map[string]any{"application": "config-test"}, f["source"])
			assert.Equal(t, "org-1", f["tenant_id"])
		})
	}
}

func TestStreamHooksLeaveTheVerdictToOnResponse(t *testing.T) {
	s := newSecurityServer(t, `{"action": "deny"}`)
	p := newPlugin(t, s, `{}`)
	x := plugintest.Exchange(conversation(), nil)
	sd, err := p.OnStreamEvent(context.Background(), x, plugintest.TextDelta("hi"))
	require.NoError(t, err)
	assert.Equal(t, pluginapi.Pass(), sd)
	d, err := p.OnStreamEnd(context.Background(), x)
	require.NoError(t, err)
	assert.Equal(t, pluginapi.ActionAllow, d.Action)
	assert.Empty(t, s.received())
}
