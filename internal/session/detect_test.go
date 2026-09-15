package session

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/core"
)

func chatSnapshot(headers map[string][]string, body string) *core.RequestSnapshot {
	return core.NewRequestSnapshot(
		"POST", "/v1/chat/completions", nil, nil, headers,
		"application/json", []byte(body), false, "req-1", nil,
	)
}

func newBuiltinDetector(autoDetect bool) *Detector {
	return NewDetector(BuiltinRules(), autoDetect)
}

func TestDetectPrecedence(t *testing.T) {
	body := `{"model":"gpt-4o","session_id":"body-session","messages":[{"role":"user","content":"hi"}]}`
	tests := []struct {
		name    string
		headers map[string][]string
		body    string
		want    string
	}{
		{
			name:    "header beats body field",
			headers: map[string][]string{"X-Session-Id": {"11111111-2222-3333-4444-555555555555"}},
			body:    body,
			want:    "11111111-2222-3333-4444-555555555555",
		},
		{
			name: "claude code session header",
			headers: map[string][]string{
				"X-Claude-Code-Session-Id": {"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
			},
			body: body,
			want: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		},
		{
			name: "opencode zen session header",
			headers: map[string][]string{
				"X-Opencode-Session": {"ses_0123456789abcdef"},
			},
			body: body,
			want: "ses_0123456789abcdef",
		},
		{
			name: "codex session-id header",
			headers: map[string][]string{
				"Session-Id": {"99999999-8888-7777-6666-555555555555"},
			},
			body: body,
			want: "99999999-8888-7777-6666-555555555555",
		},
		{
			name: "body field beats auto detection",
			body: body,
			want: "body-session",
		},
		{
			name: "header value with control characters falls through to body",
			headers: map[string][]string{
				"X-Session-Id": {"bad\r\nvalue"},
			},
			body: body,
			want: "body-session",
		},
		{
			name: "oversized header value falls through to body",
			headers: map[string][]string{
				"X-Session-Id": {strings.Repeat("x", 300)},
			},
			body: body,
			want: "body-session",
		},
	}
	detector := newBuiltinDetector(true)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detector.Detect(chatSnapshot(tt.headers, tt.body), "")
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDetectBodySignals(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "anthropic metadata user_id json object format",
			body: `{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":"{\"device_id\":\"abc\",\"session_id\":\"12345678-1234-1234-1234-123456789012\"}"}}`,
			want: "12345678-1234-1234-1234-123456789012",
		},
		{
			name: "anthropic metadata user_id legacy format",
			body: `{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":"user_deadbeef_account_x_session_87654321-4321-4321-4321-210987654321"}}`,
			want: "87654321-4321-4321-4321-210987654321",
		},
		{
			name: "litellm session id body field",
			body: `{"model":"gpt-4o","litellm_session_id":"lls-1","messages":[{"role":"user","content":"hi"}]}`,
			want: "lls-1",
		},
		{
			name: "prompt cache key",
			body: `{"model":"gpt-4o","prompt_cache_key":"thread-42","messages":[{"role":"user","content":"hi"}]}`,
			want: "thread-42",
		},
		{
			name: "responses conversation string",
			body: `{"model":"gpt-4o","conversation":"conv_123","input":"hi"}`,
			want: "conv_123",
		},
		{
			name: "responses conversation object",
			body: `{"model":"gpt-4o","conversation":{"id":"conv_456"},"input":"hi"}`,
			want: "conv_456",
		},
	}
	detector := newBuiltinDetector(false)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detector.Detect(chatSnapshot(nil, tt.body), "")
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDetectMetadataWithoutSessionFallsThrough(t *testing.T) {
	// A metadata.user_id with no embedded session uuid must not become a
	// session id itself (it is a per-install value).
	body := `{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":"user_deadbeef"}}`
	got := newBuiltinDetector(false).Detect(chatSnapshot(nil, body), "")
	require.Empty(t, got)
}

func TestDetectUserPathScoping(t *testing.T) {
	detector := newBuiltinDetector(true)

	uuidHeaders := map[string][]string{"X-Session-Id": {"11111111-2222-3333-4444-555555555555"}}
	scopedUUID := detector.Detect(chatSnapshot(uuidHeaders, `{}`), "team/app")
	require.True(t, strings.HasPrefix(scopedUUID, "scoped-"), "uuid-shaped client id must be user-path scoped, got %q", scopedUUID)
	other := detector.Detect(chatSnapshot(uuidHeaders, `{}`), "team/other")
	require.NotEqual(t, scopedUUID, other)

	weakHeaders := map[string][]string{"Agent-Session-Id": {"20260727_3"}}
	scoped := detector.Detect(chatSnapshot(weakHeaders, `{}`), "team/app")
	require.True(t, strings.HasPrefix(scoped, "scoped-"), "weak id must be user-path scoped, got %q", scoped)
	again := detector.Detect(chatSnapshot(weakHeaders, `{}`), "team/app")
	require.Equal(t, scoped, again)
	other = // Hash scoping (with the \x00 separator plus cleanSessionID's control-char
		// rejection) is unambiguous: no path/id split can forge another tenant's
		// id the way plain "path|id" concatenation could.
		detector.Detect(chatSnapshot(weakHeaders, `{}`), "team/other")
	require.NotEqual(t, scoped, other)
	got := detector.Detect(chatSnapshot(weakHeaders, `{}`), "")
	require.Equal(t, "20260727_3", got)
	got = detector.Detect(chatSnapshot(uuidHeaders, `{}`), "")
	require.Equal(t, "11111111-2222-3333-4444-555555555555", got)
}

func TestDetectAutoStability(t *testing.T) {
	detector := newBuiltinDetector(true)
	first := `{"model":"gpt-4o","messages":[{"role":"user","content":"open the pod bay doors"}]}`
	second := `{"model":"gpt-4o","messages":[{"role":"user","content":"open the pod bay doors"},{"role":"assistant","content":"no"},{"role":"user","content":"please"}]}`
	other := `{"model":"gpt-4o","messages":[{"role":"user","content":"different opener"}]}`

	idFirst := detector.Detect(chatSnapshot(nil, first), "")
	idSecond := detector.Detect(chatSnapshot(nil, second), "")
	idOther := detector.Detect(chatSnapshot(nil, other), "")

	require.NotEmpty(t, idFirst)
	require.True(t, strings.HasPrefix(idFirst, "auto-"))
	require.Equal(t, idSecond, idFirst)
	require.NotEqual(t, idOther, idFirst)
	scoped := detector.Detect(chatSnapshot(nil, first), "team")
	require.NotEqual(t, idFirst, scoped)
}

func TestDetectAutoCanonicalizesStablePrefixJSON(t *testing.T) {
	detector := newBuiltinDetector(true)
	first := `{
		"model":"gpt-4o",
		"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"path":{"type":"string"},"line":{"type":"integer"}}}}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"open\u0020file"}]}]
	}`
	reordered := `{"messages":[{"content":[{"text":"open file","type":"text"}],"role":"user"}],"tools":[{"function":{"parameters":{"properties":{"line":{"type":"integer"},"path":{"type":"string"}},"type":"object"},"name":"read"},"type":"function"}],"model":"gpt-4o"}`

	idFirst := detector.Detect(chatSnapshot(nil, first), "team")
	idReordered := detector.Detect(chatSnapshot(nil, reordered), "team")
	require.NotEmpty(t, idFirst)
	require.Equal(t, idReordered, idFirst)
}

func TestCanonicalSegmentFallsBackToExactRawJSON(t *testing.T) {
	for _, raw := range []string{
		`{"unterminated":`,
		`{"first":1}{"second":2}`,
		// Stray closing brackets: Decoder.More treats these as end of input,
		// so the trailing-data guard must decode to io.EOF to catch them.
		`1]`,
		`1}`,
	} {
		result := gjson.Result{Type: gjson.JSON, Raw: raw}
		got := string(canonicalSegment(result))
		assert.Equal(t, raw, got)
	}
}

func TestDetectAutoPreservesArrayOrderAndValues(t *testing.T) {
	detector := newBuiltinDetector(true)
	base := `{"model":"gpt-4o","tools":[{"type":"function","function":{"name":"first"}},{"type":"function","function":{"name":"second"}}],"messages":[{"role":"user","content":"hello"}]}`
	reorderedTools := `{"model":"gpt-4o","tools":[{"type":"function","function":{"name":"second"}},{"type":"function","function":{"name":"first"}}],"messages":[{"role":"user","content":"hello"}]}`
	changedValue := `{"model":"gpt-4o","tools":[{"type":"function","function":{"name":"first"}},{"type":"function","function":{"name":"second"}}],"messages":[{"role":"user","content":"hello!"}]}`

	id := detector.Detect(chatSnapshot(nil, base), "")
	require.NotEqual(t, detector.Detect(chatSnapshot(nil, reorderedTools), ""), id)
	require.NotEqual(t, detector.Detect(chatSnapshot(nil, changedValue), ""), id)
}

func TestDetectAutoSystemPromptShape(t *testing.T) {
	detector := newBuiltinDetector(true)
	first := `{"model":"gpt-4o","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"opener A"}]}`
	followUp := `{"model":"gpt-4o","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"opener A"},{"role":"assistant","content":"ok"},{"role":"user","content":"more"}]}`
	sibling := `{"model":"gpt-4o","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"opener B"}]}`

	idFirst := detector.Detect(chatSnapshot(nil, first), "")
	require.Equal(t, detector.Detect(chatSnapshot(nil, followUp), ""), idFirst)
	require.NotEqual(t, detector.Detect(chatSnapshot(nil, sibling), ""), idFirst)
}

func TestDetectAutoResponsesStringInput(t *testing.T) {
	snapshot := core.NewRequestSnapshot(
		"POST", "/v1/responses", nil, nil, nil,
		"application/json", []byte(`{"model":"gpt-4o","input":"hello"}`), false, "req-1", nil,
	)
	got := newBuiltinDetector(true).Detect(snapshot, "")
	require.True(t, strings.HasPrefix(got, "auto-"), "Detect() = %q, want auto- prefix", got)
}

func TestDetectAutoSkipsNonConversationEndpoints(t *testing.T) {
	snapshot := core.NewRequestSnapshot(
		"POST", "/v1/embeddings", nil, nil, nil,
		"application/json", []byte(`{"model":"text-embedding-3-small","input":"hi"}`), false, "req-1", nil,
	)
	got := newBuiltinDetector(true).Detect(snapshot, "")
	require.Empty(t, got)
}

func TestDetectBodyNotCaptured(t *testing.T) {
	snapshot := core.NewRequestSnapshot(
		"POST", "/v1/chat/completions", nil, nil,
		map[string][]string{"X-Session-Id": {"header-wins"}},
		"application/json", nil, true, "req-1", nil,
	)
	detector := newBuiltinDetector(true)
	got := detector.Detect(snapshot, "")
	require.Equal(t, "header-wins", got)

	snapshot = core.NewRequestSnapshot(
		"POST", "/v1/chat/completions", nil, nil, nil,
		"application/json", nil, true, "req-1", nil,
	)
	got = detector.Detect(snapshot, "")
	require.Empty(t, got)
}

func TestDetectAutoDisabled(t *testing.T) {
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	got := newBuiltinDetector(false).Detect(chatSnapshot(nil, body), "")
	require.Empty(t, got)
}

func TestDetectNilReceiverAndSnapshot(t *testing.T) {
	var detector *Detector
	got := detector.Detect(chatSnapshot(nil, `{}`), "")
	require.Empty(t, got)
	got = newBuiltinDetector(true).Detect(nil, "")
	require.Empty(t, got)
}
