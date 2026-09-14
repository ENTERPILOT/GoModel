package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractUnknownJSONFields_PreservesNestedValues(t *testing.T) {
	data := []byte(`{
		"known":"value",
		"x_object":{"nested":[1,{"ok":true}],"text":"hello"},
		"x_array":[{"type":"text","text":"hi"}],
		"x_bool":true
	}`)

	fields, err := extractUnknownJSONFields(data, "known")
	require.NoError(t, err)
	require.False(t, fields.IsEmpty())
	got := fields.Lookup("x_bool")
	require.Equal(t, json.RawMessage("true"), got)

	var nested map[string]any
	err = json.Unmarshal(fields.Lookup("x_object"), &nested)
	require.NoError(t, err)
	require.Equal(t, "hello", nested["text"])
}

func TestExtractUnknownJSONFields_HandlesEscapedStrings(t *testing.T) {
	data := []byte(`{
		"model":"gpt-5-mini",
		"x_text":"quote: \"ok\" and slash \\\\",
		"x_json":"{\"embedded\":true}"
	}`)

	fields, err := extractUnknownJSONFields(data, "model")
	require.NoError(t, err)
	got := fields.Lookup("x_text")
	require.Equal(t, json.RawMessage(`"quote: \"ok\" and slash \\\\"`), got)
	got = fields.Lookup("x_json")
	require.Equal(t, json.RawMessage(`"{\"embedded\":true}"`), got)
}

func TestExtractUnknownJSONFields_PreservesDuplicateUnknownKeys(t *testing.T) {
	data := []byte(`{"known":"value","x_meta":1,"x_meta":2}`)

	fields, err := extractUnknownJSONFields(data, "known")
	require.NoError(t, err)
	got := string(fields.raw)
	require.Equal(t, `{"x_meta":1,"x_meta":2}`, got)
	require.Equal(t, json.RawMessage("1"), fields.Lookup("x_meta"), "first duplicate value wins")
}

func TestUnknownJSONFieldsFromMap_EmptyRawValueEncodesAsNull(t *testing.T) {
	fields := UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"x_nil": nil,
		"x_set": json.RawMessage(`true`),
	})
	got := fields.Lookup("x_nil")
	require.Equal(t, json.RawMessage("null"), got)
	got = fields.Lookup("x_set")
	require.Equal(t, json.RawMessage("true"), got)
}

func TestUnknownJSONFieldsWithoutRemovesOnlyNamedMembers(t *testing.T) {
	fields, err := extractUnknownJSONFields(
		[]byte(`{"known":true,"cache_control":{"type":"ephemeral"},"x":1,"x":2}`),
		"known",
	)
	require.NoError(t, err)

	filtered := fields.Without("cache_control")
	got := filtered.Lookup("cache_control")
	require.Nil(t, got)
	require.Equal(t, `{"x":1,"x":2}`, string(filtered.raw), "duplicate unrelated fields must be preserved")
	got = fields.Lookup("cache_control")
	require.NotNil(t, got)
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
	require.NoError(t, err)
	got := merged.Lookup("keep")
	require.Equal(t, json.RawMessage(`1`), got)
	got = merged.Lookup("override")
	require.Equal(t, json.RawMessage(`"new"`), got)
	got = merged.Lookup("added")
	require.Equal(t, json.RawMessage(`true`), got)
}

func TestMergeUnknownJSONFields_PreservesRawBaseMembers(t *testing.T) {
	base := UnknownJSONFields{
		raw: json.RawMessage(`{"keep":{"b":2,"a":1},"dup":"first","dup":"second","override":"old"}`),
	}

	merged, err := MergeUnknownJSONFields(base, map[string]json.RawMessage{
		"override": json.RawMessage(`"new"`),
		"added":    json.RawMessage(`true`),
	})
	require.NoError(t, err)
	require.Equal(t, 2, bytes.Count(merged.raw, []byte(`"dup"`)), "merged raw = %s, want duplicate dup keys preserved", merged.raw)
	require.False(t, bytes.Contains(merged.raw, []byte(`"override":"old"`)), "merged raw = %s, old override value should be removed", merged.raw)
	got := merged.Lookup("dup")
	require.Equal(t, json.RawMessage(`"first"`), got)
	got = merged.Lookup("override")
	require.Equal(t, json.RawMessage(`"new"`), got)
	got = merged.Lookup("added")
	require.Equal(t, json.RawMessage(`true`), got)
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
			_, err := MergeUnknownJSONFields(tt.base, tt.additions)
			require.Error(t, err)
		})
	}
}

func TestMergeUnknownJSONFields_NoAdditionsReturnsBase(t *testing.T) {
	base := UnknownJSONFieldsFromMap(map[string]json.RawMessage{"a": json.RawMessage(`1`)})

	merged, err := MergeUnknownJSONFields(base, nil)
	require.NoError(t, err)
	require.Equal(t, json.RawMessage(`1`), merged.Lookup("a"))
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
			require.Error(t, req.UnmarshalJSON([]byte(tt.body)), "ChatRequest.UnmarshalJSON(%q) error = nil, want syntax error", tt.body)
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
			err := req.UnmarshalJSON([]byte(tt.body))
			require.NoError(t, err)
		})
	}
}

func TestMergedJSONObjectCap_Overflow(t *testing.T) {
	_, err := mergedJSONObjectCap(math.MaxInt, 2)
	require.Error(t, err)
}

func TestCloneOptionalJSONObject(t *testing.T) {
	tests := []struct {
		name    string
		raw     json.RawMessage
		want    string
		wantErr bool
	}{
		{name: "empty"},
		{name: "null", raw: json.RawMessage(` null `)},
		{name: "object", raw: json.RawMessage(` {"type":"ephemeral"} `), want: `{"type":"ephemeral"}`},
		{name: "array", raw: json.RawMessage(`[]`), wantErr: true},
		{name: "invalid object", raw: json.RawMessage(`{"type":`), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CloneOptionalJSONObject(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, string(got))
		})
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
	require.NoError(t, err)
	got := string(fields.raw)
	require.Equal(t, `{"custom_flag":true}`, got)
	c := cap(fields.raw)
	require.LessOrEqual(t, c, 4096)
}
