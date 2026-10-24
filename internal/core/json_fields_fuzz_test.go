package core

import (
	"bytes"
	stdjson "encoding/json"
	"slices"
	"testing"
	"unicode/utf8"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fuzzKnownFields stands in for a request type's typed members.
var fuzzKnownFields = []string{"model", "messages", "stream", "temperature"}

// FuzzUnknownJSONFieldsRoundTrip splits arbitrary JSON objects into known and
// unknown members and joins them again. The splitter must not panic, and for a
// valid object the rejoined document must mean the same as the input: no
// member lost, duplicated with another value, or altered.
func FuzzUnknownJSONFieldsRoundTrip(f *testing.F) {
	for _, seed := range []string{
		`{}`,
		`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"gpt-4o-mini","stream":true,"x_custom":{"nested":[1,2,{"a":null}]}}`,
		`{"temperature":0.2,"seed":1e3,"logit_bias":{"50256":-100}}`,
		`{"a":1,"a":2,"model":"m","model":"n"}`,
		`{"model.name":"dot","x*":"star","":"empty","model":"escaped"}`,
		` { "spaced" : [ ] , "model" : null } `,
		`{"unicode":"żółć","emoji":"😀"}`,
		`[1,2,3]`,
		`"string"`,
		`{"unterminated":`,
		`not json`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		extra, err := extractUnknownJSONFields(data, fuzzKnownFields...)
		trimmed := bytes.TrimSpace(data)
		if !utf8.Valid(trimmed) || !stdjson.Valid(trimmed) || len(trimmed) == 0 || trimmed[0] != '{' {
			return
		}
		require.NoError(t, err)

		// Known members, by exact name, the last one winning as in a decoder.
		var members map[string]json.RawMessage
		require.NoError(t, stdjson.Unmarshal(trimmed, &members))
		known := map[string]json.RawMessage{}
		for name, value := range members {
			if slices.Contains(fuzzKnownFields, name) {
				known[name] = value
			}
		}

		joined, err := marshalWithUnknownJSONFields(known, extra)
		require.NoError(t, err)
		assert.JSONEq(t, string(trimmed), string(joined))
	})
}
