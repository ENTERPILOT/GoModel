package session

import (
	"strings"
	"testing"

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
			if got != tt.want {
				t.Fatalf("Detect() = %q, want %q", got, tt.want)
			}
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
			if got != tt.want {
				t.Fatalf("Detect() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectMetadataWithoutSessionFallsThrough(t *testing.T) {
	// A metadata.user_id with no embedded session uuid must not become a
	// session id itself (it is a per-install value).
	body := `{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":"user_deadbeef"}}`
	if got := newBuiltinDetector(false).Detect(chatSnapshot(nil, body), ""); got != "" {
		t.Fatalf("Detect() = %q, want empty", got)
	}
}

func TestDetectUserPathScoping(t *testing.T) {
	detector := newBuiltinDetector(true)

	uuidHeaders := map[string][]string{"X-Session-Id": {"11111111-2222-3333-4444-555555555555"}}
	scopedUUID := detector.Detect(chatSnapshot(uuidHeaders, `{}`), "team/app")
	if !strings.HasPrefix(scopedUUID, "scoped-") {
		t.Fatalf("uuid-shaped client id must be user-path scoped, got %q", scopedUUID)
	}
	if other := detector.Detect(chatSnapshot(uuidHeaders, `{}`), "team/other"); other == scopedUUID {
		t.Fatal("same UUID-shaped id under different user paths must not collide")
	}

	weakHeaders := map[string][]string{"Agent-Session-Id": {"20260727_3"}}
	scoped := detector.Detect(chatSnapshot(weakHeaders, `{}`), "team/app")
	if !strings.HasPrefix(scoped, "scoped-") {
		t.Fatalf("weak id must be user-path scoped, got %q", scoped)
	}
	if again := detector.Detect(chatSnapshot(weakHeaders, `{}`), "team/app"); again != scoped {
		t.Fatalf("scoping must be deterministic: %q vs %q", scoped, again)
	}
	// Hash scoping (with the \x00 separator plus cleanSessionID's control-char
	// rejection) is unambiguous: no path/id split can forge another tenant's
	// id the way plain "path|id" concatenation could.
	if other := detector.Detect(chatSnapshot(weakHeaders, `{}`), "team/other"); other == scoped {
		t.Fatal("same weak id under different user paths must not collide")
	}
	if got := detector.Detect(chatSnapshot(weakHeaders, `{}`), ""); got != "20260727_3" {
		t.Fatalf("weak id without user path stays raw, got %q", got)
	}
	if got := detector.Detect(chatSnapshot(uuidHeaders, `{}`), ""); got != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("UUID-shaped id without user path stays raw, got %q", got)
	}
}

func TestDetectAutoStability(t *testing.T) {
	detector := newBuiltinDetector(true)
	first := `{"model":"gpt-4o","messages":[{"role":"user","content":"open the pod bay doors"}]}`
	second := `{"model":"gpt-4o","messages":[{"role":"user","content":"open the pod bay doors"},{"role":"assistant","content":"no"},{"role":"user","content":"please"}]}`
	other := `{"model":"gpt-4o","messages":[{"role":"user","content":"different opener"}]}`

	idFirst := detector.Detect(chatSnapshot(nil, first), "")
	idSecond := detector.Detect(chatSnapshot(nil, second), "")
	idOther := detector.Detect(chatSnapshot(nil, other), "")

	if idFirst == "" || !strings.HasPrefix(idFirst, "auto-") {
		t.Fatalf("auto id = %q, want auto- prefix", idFirst)
	}
	if idFirst != idSecond {
		t.Fatalf("appending a turn changed the id: %q vs %q", idFirst, idSecond)
	}
	if idFirst == idOther {
		t.Fatal("different conversations must get different auto ids")
	}
	if scoped := detector.Detect(chatSnapshot(nil, first), "team"); scoped == idFirst {
		t.Fatal("auto id must fold in the user path")
	}
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
	if idFirst == "" || idFirst != idReordered {
		t.Fatalf("semantic JSON changes split auto session: %q vs %q", idFirst, idReordered)
	}
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
		if got := string(canonicalSegment(result)); got != raw {
			t.Errorf("canonicalSegment(%q) = %q, want exact raw fallback", raw, got)
		}
	}
}

func TestDetectAutoPreservesArrayOrderAndValues(t *testing.T) {
	detector := newBuiltinDetector(true)
	base := `{"model":"gpt-4o","tools":[{"type":"function","function":{"name":"first"}},{"type":"function","function":{"name":"second"}}],"messages":[{"role":"user","content":"hello"}]}`
	reorderedTools := `{"model":"gpt-4o","tools":[{"type":"function","function":{"name":"second"}},{"type":"function","function":{"name":"first"}}],"messages":[{"role":"user","content":"hello"}]}`
	changedValue := `{"model":"gpt-4o","tools":[{"type":"function","function":{"name":"first"}},{"type":"function","function":{"name":"second"}}],"messages":[{"role":"user","content":"hello!"}]}`

	id := detector.Detect(chatSnapshot(nil, base), "")
	if id == detector.Detect(chatSnapshot(nil, reorderedTools), "") {
		t.Fatal("tool array order must remain part of the session anchor")
	}
	if id == detector.Detect(chatSnapshot(nil, changedValue), "") {
		t.Fatal("changed message value must change the session anchor")
	}
}

func TestDetectAutoSystemPromptShape(t *testing.T) {
	detector := newBuiltinDetector(true)
	first := `{"model":"gpt-4o","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"opener A"}]}`
	followUp := `{"model":"gpt-4o","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"opener A"},{"role":"assistant","content":"ok"},{"role":"user","content":"more"}]}`
	sibling := `{"model":"gpt-4o","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"opener B"}]}`

	idFirst := detector.Detect(chatSnapshot(nil, first), "")
	if idFirst != detector.Detect(chatSnapshot(nil, followUp), "") {
		t.Fatal("follow-up with appended turns must keep the id")
	}
	if idFirst == detector.Detect(chatSnapshot(nil, sibling), "") {
		t.Fatal("same system prompt with a different first user message must get a new id")
	}
}

func TestDetectAutoResponsesStringInput(t *testing.T) {
	snapshot := core.NewRequestSnapshot(
		"POST", "/v1/responses", nil, nil, nil,
		"application/json", []byte(`{"model":"gpt-4o","input":"hello"}`), false, "req-1", nil,
	)
	if got := newBuiltinDetector(true).Detect(snapshot, ""); !strings.HasPrefix(got, "auto-") {
		t.Fatalf("Detect() = %q, want auto- prefix", got)
	}
}

func TestDetectAutoSkipsNonConversationEndpoints(t *testing.T) {
	snapshot := core.NewRequestSnapshot(
		"POST", "/v1/embeddings", nil, nil, nil,
		"application/json", []byte(`{"model":"text-embedding-3-small","input":"hi"}`), false, "req-1", nil,
	)
	if got := newBuiltinDetector(true).Detect(snapshot, ""); got != "" {
		t.Fatalf("Detect() = %q, want empty", got)
	}
}

func TestDetectBodyNotCaptured(t *testing.T) {
	snapshot := core.NewRequestSnapshot(
		"POST", "/v1/chat/completions", nil, nil,
		map[string][]string{"X-Session-Id": {"header-wins"}},
		"application/json", nil, true, "req-1", nil,
	)
	detector := newBuiltinDetector(true)
	if got := detector.Detect(snapshot, ""); got != "header-wins" {
		t.Fatalf("header detection must survive uncaptured bodies, got %q", got)
	}
	snapshot = core.NewRequestSnapshot(
		"POST", "/v1/chat/completions", nil, nil, nil,
		"application/json", nil, true, "req-1", nil,
	)
	if got := detector.Detect(snapshot, ""); got != "" {
		t.Fatalf("Detect() = %q, want empty for uncaptured body", got)
	}
}

func TestDetectAutoDisabled(t *testing.T) {
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	if got := newBuiltinDetector(false).Detect(chatSnapshot(nil, body), ""); got != "" {
		t.Fatalf("Detect() = %q, want empty with auto detection off", got)
	}
}

func TestDetectNilReceiverAndSnapshot(t *testing.T) {
	var detector *Detector
	if got := detector.Detect(chatSnapshot(nil, `{}`), ""); got != "" {
		t.Fatalf("nil detector Detect() = %q, want empty", got)
	}
	if got := newBuiltinDetector(true).Detect(nil, ""); got != "" {
		t.Fatalf("nil snapshot Detect() = %q, want empty", got)
	}
}
