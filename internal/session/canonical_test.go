package session

import (
	"bytes"
	encjson "encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

// TestCanonicalSegmentMatchesStdlib pins that goccy-based canonicalization
// produces byte-identical output to the original encoding/json implementation.
// Auto-detected session ids hash these bytes, so any divergence would silently
// re-anchor every in-flight conversation on upgrade (breaking virtual-model
// affinity pins and Pro compression epochs mid-session).
func TestCanonicalSegmentMatchesStdlib(t *testing.T) {
	segments := []string{
		`"gpt-4o-mini"`,
		`"with \"escapes\" and é unicode 😀"`,
		`"html <b>&amp;</b> specials"`,
		`123`,
		`1e2`,
		`1.50`,
		`-0.0031415926535897932384626433e4`,
		`9007199254740993`,
		`true`,
		`null`,
		`[]`,
		`{}`,
		`[1, "two", {"three": 3}, [4]]`,
		`{"b":2,"a":1,"nested":{"z":null,"y":[1.0,"x"]}}`,
		`{"role":"user","content":[{"type":"text","text":"line one\nline two\ttabbed"}]}`,
		`{ "spaced" : { "keys" : [ 1 , 2 ] } }`,
	}

	for _, segment := range segments {
		parsed := gjson.Parse(segment)
		got := canonicalSegment(parsed)
		want := stdlibCanonical(t, parsed.Raw)
		if !bytes.Equal(got, want) {
			t.Errorf("canonicalSegment(%s) = %s, stdlib canonical = %s", segment, got, want)
		}
	}
}

// stdlibCanonical is the pre-switch implementation, kept verbatim as the oracle.
func stdlibCanonical(t *testing.T, raw string) []byte {
	t.Helper()
	decoder := encjson.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return []byte(raw)
	}
	canonical, err := encjson.Marshal(value)
	if err != nil {
		return []byte(raw)
	}
	return canonical
}
