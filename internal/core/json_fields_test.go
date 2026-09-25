package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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

// Member names are matched literally. A gjson path would read "a.b" as a
// nested lookup and "x*" as a wildcard, quietly answering for a member the
// caller never asked about.
func TestUnknownJSONFieldsMatchMemberNamesLiterally(t *testing.T) {
	fields, err := extractUnknownJSONFields([]byte(`{"model":"m","a.b":1,"x*":2,"nested":{"leaf":3}}`), "model")
	require.NoError(t, err)

	assert.Equal(t, json.RawMessage("1"), fields.Lookup("a.b"), "a dotted name is a member, not a path")
	assert.Equal(t, json.RawMessage("2"), fields.Lookup("x*"), "an asterisk in a name is not a wildcard")
	assert.Nil(t, fields.Lookup("nested.leaf"), "a path must not reach into a nested object")
	assert.Nil(t, fields.Lookup("x_absent"))

	assert.True(t, fields.HasAny("x_absent", "a.b"))
	assert.True(t, fields.HasAny("x*"))
	assert.False(t, fields.HasAny("nested.leaf"), "a path must not reach into a nested object")
	assert.False(t, fields.HasAny("x_absent"))
	assert.False(t, fields.HasAny(), "no keys means nothing to find")
}

// HasAny answers for the same members Lookup finds, including ones whose value
// is null or an empty object, and finds nothing in an empty container.
func TestUnknownJSONFieldsHasAnyMatchesLookup(t *testing.T) {
	fields, err := extractUnknownJSONFields([]byte(`{"model":"m","x_null":null,"x_empty":{},"x_set":1}`), "model")
	require.NoError(t, err)

	for _, key := range []string{"x_null", "x_empty", "x_set"} {
		assert.True(t, fields.HasAny(key), "HasAny(%q)", key)
		assert.NotNil(t, fields.Lookup(key), "Lookup(%q)", key)
	}
	assert.True(t, fields.HasAny("x_absent", "x_null"), "one present key of several is enough")

	assert.False(t, UnknownJSONFields{}.HasAny("x_set"))
	assert.Nil(t, UnknownJSONFields{}.Lookup("x_set"))
}

func BenchmarkUnknownJSONFieldsHasAny(b *testing.B) {
	fields, err := extractUnknownJSONFields([]byte(`{"model":"m","name":"alice","x_message_meta":{"id":"msg-1"},"x_trace":{"id":"trace-1"}}`), "model")
	if err != nil {
		b.Fatal(err)
	}
	directives := []string{
		"cache_control",
		"cached_content",
		"prompt_cache_key",
		"prompt_cache_options",
		"prompt_cache_breakpoint",
		"x_gomodel_cache_point",
	}

	// The prompt-cache planner's shape: several keys tested for presence on a
	// container that has none of them.
	b.Run("has_any_absent", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if fields.HasAny(directives...) {
				b.Fatal("unexpected directive")
			}
		}
	})
	b.Run("lookup_per_key_absent", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for _, key := range directives {
				if len(fields.Lookup(key)) > 0 {
					b.Fatal("unexpected directive")
				}
			}
		}
	})
	b.Run("lookup_hit", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if len(fields.Lookup("x_trace")) == 0 {
				b.Fatal("expected the member")
			}
		}
	})
	// Lookup used to decode the stored object with a streaming json.Decoder.
	// Single lookups are on hundreds of translation paths, so keep the
	// replaced implementation measurable beside the one in use.
	b.Run("lookup_hit_decoder_baseline", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if len(lookupDecoderBaseline(fields, "x_trace")) == 0 {
				b.Fatal("expected the member")
			}
		}
	})
}

// lookupDecoderBaseline is UnknownJSONFields.Lookup as it was before the
// gjson scan replaced it, kept only as a benchmark reference.
func lookupDecoderBaseline(fields UnknownJSONFields, key string) json.RawMessage {
	if len(fields.raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(fields.raw))
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil
	}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil
		}
		fieldName, ok := keyToken.(string)
		if !ok {
			return nil
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil
		}
		if fieldName == key {
			return CloneRawJSON(value)
		}
	}
	return nil
}

// The request decoder is deliberately more lenient than strict JSON (see
// TestDecoderLeniencyIsBounded). A passthrough member the gateway accepted on
// the way in must still be visible on the way out, or it would be dropped
// from the request forwarded upstream.
func TestUnknownJSONFieldsSeeEveryValueTheDecoderAccepted(t *testing.T) {
	accepted := map[string]string{
		"trailing array comma":  `[1,]`,
		"trailing object comma": `{"a":1,}`,
		"leading-zero number":   `01`,
		"plain object":          `{"a":1}`,
	}
	for name, value := range accepted {
		t.Run(name, func(t *testing.T) {
			var req ChatRequest
			require.NoError(t, req.UnmarshalJSON([]byte(`{"model":"m","x_passthrough":`+value+`}`)),
				"the decoder accepts this body")

			assert.NotEmpty(t, req.ExtraFields.Lookup("x_passthrough"), "the member the decoder accepted must be visible")
			assert.True(t, req.ExtraFields.HasAny("x_passthrough"))
		})
	}
}

// Bytes the decoder itself rejects stay invisible: Lookup must never hand back
// a value a caller cannot decode (internal/providers/vllm relies on this).
func TestUnknownJSONFieldsHideValuesTheDecoderRejects(t *testing.T) {
	for _, value := range []string{`not-valid-json{{{`, `tru`} {
		fields := UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_broken": json.RawMessage(value)})
		assert.Nil(t, fields.Lookup("x_broken"), "value %q", value)
		assert.False(t, fields.HasAny("x_broken"), "value %q", value)
	}
}
