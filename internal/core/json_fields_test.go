package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestExtractUnknownJSONFields_PreservesNestedValues(t *testing.T) {
	data := []byte(`{
		"known":"value",
		"x_object":{"nested":[1,{"ok":true}],"text":"hello"},
		"x_array":[{"type":"text","text":"hi"}],
		"x_bool":true
	}`)

	fields, err := extractUnknownJSONFields(data, "known")
	if err != nil {
		t.Fatalf("extractUnknownJSONFields() error = %v", err)
	}

	if fields.IsEmpty() {
		t.Fatal("expected unknown fields")
	}
	if got := fields.Lookup("x_bool"); !bytes.Equal(got, []byte("true")) {
		t.Fatalf("x_bool = %s, want true", got)
	}

	var nested map[string]any
	if err := json.Unmarshal(fields.Lookup("x_object"), &nested); err != nil {
		t.Fatalf("failed to unmarshal x_object: %v", err)
	}
	if nested["text"] != "hello" {
		t.Fatalf("x_object.text = %#v, want hello", nested["text"])
	}
}

func TestExtractUnknownJSONFields_HandlesEscapedStrings(t *testing.T) {
	data := []byte(`{
		"model":"gpt-5-mini",
		"x_text":"quote: \"ok\" and slash \\\\",
		"x_json":"{\"embedded\":true}"
	}`)

	fields, err := extractUnknownJSONFields(data, "model")
	if err != nil {
		t.Fatalf("extractUnknownJSONFields() error = %v", err)
	}

	if got := fields.Lookup("x_text"); !bytes.Equal(got, []byte(`"quote: \"ok\" and slash \\\\"`)) {
		t.Fatalf("x_text = %s", got)
	}
	if got := fields.Lookup("x_json"); !bytes.Equal(got, []byte(`"{\"embedded\":true}"`)) {
		t.Fatalf("x_json = %s", got)
	}
}

func TestExtractUnknownJSONFields_PreservesDuplicateUnknownKeys(t *testing.T) {
	data := []byte(`{"known":"value","x_meta":1,"x_meta":2}`)

	fields, err := extractUnknownJSONFields(data, "known")
	if err != nil {
		t.Fatalf("extractUnknownJSONFields() error = %v", err)
	}
	if got := string(fields.raw); got != `{"x_meta":1,"x_meta":2}` {
		t.Fatalf("raw = %s, want duplicate keys preserved", got)
	}
	if got := fields.Lookup("x_meta"); !bytes.Equal(got, []byte("1")) {
		t.Fatalf("Lookup(x_meta) = %s, want first duplicate value", got)
	}
}

func TestUnknownJSONFieldsFromMap_EmptyRawValueEncodesAsNull(t *testing.T) {
	fields := UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"x_nil": nil,
		"x_set": json.RawMessage(`true`),
	})

	if got := fields.Lookup("x_nil"); !bytes.Equal(got, []byte("null")) {
		t.Fatalf("x_nil = %q, want null", got)
	}
	if got := fields.Lookup("x_set"); !bytes.Equal(got, []byte("true")) {
		t.Fatalf("x_set = %q, want true", got)
	}
}

func TestMergeUnknownJSONFields_AddsAndOverrides(t *testing.T) {
	base := UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"keep":     json.RawMessage(`1`),
		"override": json.RawMessage(`"old"`),
	})

	merged, err := MergeUnknownJSONFields(base, map[string]json.RawMessage{
		"override": json.RawMessage(`"new"`),
		"added":    json.RawMessage(`true`),
	})
	if err != nil {
		t.Fatalf("MergeUnknownJSONFields() error = %v", err)
	}

	if got := merged.Lookup("keep"); !bytes.Equal(got, []byte(`1`)) {
		t.Fatalf("keep = %q, want 1", got)
	}
	if got := merged.Lookup("override"); !bytes.Equal(got, []byte(`"new"`)) {
		t.Fatalf("override = %q, want \"new\"", got)
	}
	if got := merged.Lookup("added"); !bytes.Equal(got, []byte(`true`)) {
		t.Fatalf("added = %q, want true", got)
	}
}

func TestMergeUnknownJSONFields_PreservesRawBaseMembers(t *testing.T) {
	base := UnknownJSONFields{
		raw: json.RawMessage(`{"keep":{"b":2,"a":1},"dup":"first","dup":"second","override":"old"}`),
	}

	merged, err := MergeUnknownJSONFields(base, map[string]json.RawMessage{
		"override": json.RawMessage(`"new"`),
		"added":    json.RawMessage(`true`),
	})
	if err != nil {
		t.Fatalf("MergeUnknownJSONFields() error = %v", err)
	}

	if bytes.Count(merged.raw, []byte(`"dup"`)) != 2 {
		t.Fatalf("merged raw = %s, want duplicate dup keys preserved", merged.raw)
	}
	if bytes.Contains(merged.raw, []byte(`"override":"old"`)) {
		t.Fatalf("merged raw = %s, old override value should be removed", merged.raw)
	}
	if got := merged.Lookup("dup"); !bytes.Equal(got, []byte(`"first"`)) {
		t.Fatalf("dup = %s, want first duplicate value", got)
	}
	if got := merged.Lookup("override"); !bytes.Equal(got, []byte(`"new"`)) {
		t.Fatalf("override = %s, want new value", got)
	}
	if got := merged.Lookup("added"); !bytes.Equal(got, []byte(`true`)) {
		t.Fatalf("added = %s, want true", got)
	}
}

func TestMergeUnknownJSONFields_ErrorPaths(t *testing.T) {
	tests := []struct {
		name      string
		base      UnknownJSONFields
		additions map[string]json.RawMessage
	}{
		{
			name: "malformed base raw",
			base: UnknownJSONFields{raw: json.RawMessage(`{"keep":`)},
			additions: map[string]json.RawMessage{
				"added": json.RawMessage(`true`),
			},
		},
		{
			name: "non object base raw",
			base: UnknownJSONFields{raw: json.RawMessage(`[1,2,3]`)},
			additions: map[string]json.RawMessage{
				"added": json.RawMessage(`true`),
			},
		},
		{
			name: "malformed addition raw",
			base: UnknownJSONFields{},
			additions: map[string]json.RawMessage{
				"added": json.RawMessage(`{`),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := MergeUnknownJSONFields(tt.base, tt.additions); err == nil {
				t.Fatal("MergeUnknownJSONFields() error = nil, want error")
			}
		})
	}
}

func TestMergeUnknownJSONFields_NoAdditionsReturnsBase(t *testing.T) {
	base := UnknownJSONFieldsFromMap(map[string]json.RawMessage{"a": json.RawMessage(`1`)})

	merged, err := MergeUnknownJSONFields(base, nil)
	if err != nil {
		t.Fatalf("MergeUnknownJSONFields() error = %v", err)
	}
	if !bytes.Equal(merged.Lookup("a"), []byte(`1`)) {
		t.Fatalf("a = %q, want 1", merged.Lookup("a"))
	}
}

// extractUnknownJSONFields assumes its input is already valid JSON: every
// production caller is an UnmarshalJSON method that runs json.Unmarshal on the
// same bytes first. This test pins the meaningful guarantee at that boundary —
// structurally malformed bodies are rejected before unknown-field extraction
// runs — rather than re-validating inside the helper.
//
// Note on the JSON decoder: the project uses github.com/goccy/go-json, which is
// slightly more lenient than encoding/json on a couple of malformed-input edge
// cases (notably trailing commas inside skipped unknown/passthrough fields, and
// leading-zero numbers). That extra input tolerance is acceptable under the
// gateway's "accept generously" principle, so this test covers structural
// errors that remain rejected; see TestDecoderLeniencyIsBounded for the
// documented, intentional acceptances.
func TestUnmarshalJSON_RejectsInvalidJSONSyntax(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "invalid bare literal", body: `{"model":"m","x":wat}`},
		{name: "missing object comma", body: `{"model":"m" "x":1}`},
		{name: "trailing object comma", body: `{"model":"m","x":1,}`},
		{name: "trailing top-level data", body: `{"model":"m","x":1}{"extra":true}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req ChatRequest
			if err := req.UnmarshalJSON([]byte(tt.body)); err == nil {
				t.Fatalf("ChatRequest.UnmarshalJSON(%q) error = nil, want syntax error", tt.body)
			}
		})
	}
}

// TestDecoderLeniencyIsBounded documents the known, intentional input-tolerance
// differences introduced by github.com/goccy/go-json relative to encoding/json.
// These are accepted (the gateway favors accepting generously and normalizing),
// but pinning them here makes the behavior explicit and flags any future change.
func TestDecoderLeniencyIsBounded(t *testing.T) {
	accepted := []struct {
		name string
		body string
	}{
		// Malformed values inside an unknown/passthrough field are skipped
		// leniently rather than rejected.
		{name: "trailing array comma in passthrough field", body: `{"model":"m","x":[1,]}`},
		// Leading-zero numbers are tolerated.
		{name: "leading-zero number in passthrough field", body: `{"model":"m","x":01}`},
	}

	for _, tt := range accepted {
		t.Run(tt.name, func(t *testing.T) {
			var req ChatRequest
			if err := req.UnmarshalJSON([]byte(tt.body)); err != nil {
				t.Fatalf("ChatRequest.UnmarshalJSON(%q) error = %v, want accepted", tt.body, err)
			}
		})
	}
}

func TestMergedJSONObjectCap_Overflow(t *testing.T) {
	if _, err := mergedJSONObjectCap(math.MaxInt, 2); err == nil {
		t.Fatal("mergedJSONObjectCap() error = nil, want overflow error")
	}
}

func TestExtractUnknownJSONFields_DoesNotRetainBodySizedCapacity(t *testing.T) {
	// A large request with tiny unknown extras: the retained raw bytes are
	// kept for the decoded request's whole lifetime, so they must not pin a
	// body-sized backing array (regression: the buffer was pre-sized to
	// len(data)).
	body := fmt.Sprintf(`{"model":"gpt-test","messages":[{"role":"user","content":%q}],"custom_flag":true}`,
		strings.Repeat("x", 1<<20))

	fields, err := extractUnknownJSONFields([]byte(body), "model", "messages")
	if err != nil {
		t.Fatalf("extractUnknownJSONFields() error = %v", err)
	}
	if got := string(fields.raw); got != `{"custom_flag":true}` {
		t.Fatalf("raw = %q, want custom_flag only", got)
	}
	if c := cap(fields.raw); c > 4096 {
		t.Fatalf("retained capacity = %d bytes for %d bytes of extras, want small", c, len(fields.raw))
	}
}
