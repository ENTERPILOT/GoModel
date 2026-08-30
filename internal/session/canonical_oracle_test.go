package session

import (
	"bytes"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"
)

// TestCanonicalSegmentMatchesOracle drives the tree-walking canonicalizer
// with generated documents (nested objects, duplicate keys, every escape
// class, awkward numbers, malformed input) and pins it byte-for-byte to the
// decode/re-encode path it replaced, so auto-detected session ids do not
// change with the implementation.
func TestCanonicalSegmentMatchesOracle(t *testing.T) {
	fixed := []string{
		`"\b\f\n\r\t"`,
		"\"<b>&amp;</b>     <\"",
		`"  escaped separator"`,
		`"😀 paired surrogate"`,
		`"\ud800 lone surrogate"`,
		`"\ud800A mismatched surrogate"`,
		`"😀 upper hex"`,
		`"\/ solidus \" quote \\ backslash"`,
		"\"invalid \xff utf8\"",
		`{"a":1,"a":2,"b":3}`,
		`{"a":1,"a":2}`,
		`{"b":{"z":1,"a":[]},"a":{}}`,
		`{"":"empty key","x":""}`,
		`[[[]],[{}],[null,true,false]]`,
		`-0`,
		`0.000`,
		`1E+2`,
		`-1.5e-7`,
		` 42 `,
		`{"k" : "v" , "n" : [ 1 , 2 ] }`,
		`{"é":"ü","😀":"x"}`,
		`{"a":1}{"b":2}`,
		`1]`,
		`{"unterminated":`,
	}
	for _, raw := range fixed {
		assertCanonicalMatchesOracle(t, raw)
	}

	rng := rand.New(rand.NewSource(1))
	for range 2000 {
		assertCanonicalMatchesOracle(t, randomJSON(rng, 0))
	}
}

func assertCanonicalMatchesOracle(t *testing.T, raw string) {
	t.Helper()
	result := gjson.Parse(raw)
	if !result.Exists() {
		result = gjson.Result{Type: gjson.JSON, Raw: raw}
	}
	got := canonicalSegment(result)
	want := canonicalSegmentDecoded(json.RawMessage(strings.Clone(result.Raw)))
	if !bytes.Equal(got, want) {
		t.Errorf("canonicalSegment(%q) = %q, oracle = %q", raw, got, want)
	}
}

var randomStringPieces = []string{
	"a", "Z", " ", "é", "😀", `\n`, `\t`, `\"`, `\\`, `\/`, `A`, `é`,
	`😀`, ` `, "<", ">", "&", `<`, " ", "\x01",
}

func randomJSON(rng *rand.Rand, depth int) string {
	kind := rng.Intn(7)
	if depth > 3 {
		kind = rng.Intn(4)
	}
	switch kind {
	case 0:
		return []string{"null", "true", "false"}[rng.Intn(3)]
	case 1:
		return randomNumber(rng)
	case 2, 3:
		return randomString(rng)
	case 4, 5:
		n := rng.Intn(4)
		parts := make([]string, n)
		for i := range parts {
			parts[i] = randomJSON(rng, depth+1)
		}
		return "[" + strings.Join(parts, randomSpace(rng)+","+randomSpace(rng)) + "]"
	default:
		n := rng.Intn(4)
		parts := make([]string, n)
		keys := []string{`"a"`, `"b"`, `"a"`, `"b"`, `""`, `"z z"`}
		for i := range parts {
			parts[i] = keys[rng.Intn(len(keys))] + randomSpace(rng) + ":" + randomSpace(rng) + randomJSON(rng, depth+1)
		}
		return "{" + randomSpace(rng) + strings.Join(parts, ",") + randomSpace(rng) + "}"
	}
}

func randomNumber(rng *rand.Rand) string {
	switch rng.Intn(4) {
	case 0:
		return strconv.Itoa(rng.Intn(2000) - 1000)
	case 1:
		return strconv.FormatFloat(rng.NormFloat64()*1e6, 'g', -1, 64)
	case 2:
		return []string{"0", "-0", "1.0", "1e5", "1E-5", "12345678901234567890", "0.1000"}[rng.Intn(7)]
	default:
		return strconv.FormatFloat(rng.Float64(), 'e', rng.Intn(10), 64)
	}
}

func randomString(rng *rand.Rand) string {
	n := rng.Intn(6)
	var b strings.Builder
	b.WriteByte('"')
	for range n {
		b.WriteString(randomStringPieces[rng.Intn(len(randomStringPieces))])
	}
	b.WriteByte('"')
	return b.String()
}

func randomSpace(rng *rand.Rand) string {
	return []string{"", " ", "\n\t"}[rng.Intn(3)]
}
