package presidio

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestByteSpans(t *testing.T) {
	text := "Zoë 😀 Ann"
	// Code points: Z0 o1 ë2 ' '3 😀4 ' '5 A6 n7 n8.
	results := []analyzerResult{
		{EntityType: "PERSON", Start: 0, End: 3, Score: 0.85},
		{EntityType: "PERSON", Start: 6, End: 9, Score: 0.85},
		{EntityType: "LOCATION", Start: 6, End: 9, Score: 0.4},  // same span, lower score
		{EntityType: "DATE_TIME", Start: 0, End: 2, Score: 0.9}, // overlaps, shorter but higher score
		{EntityType: "X", Start: 8, End: 20},                    // out of range
		{EntityType: "", Start: 4, End: 5},                      // no type
		{EntityType: "Y", Start: 5, End: 5},                     // empty
	}
	got := byteSpans(text, results)
	want := []span{
		{entity: "DATE_TIME", start: 0, end: 2, score: 0.9},
		{entity: "PERSON", start: 10, end: 13, score: 0.85},
	}
	assert.Equal(t, want, got)
	out := rewrite(text, got, func(s span, v string) string { return "<" + s.entity + ">" })
	assert.Equal(t, "<DATE_TIME>ë 😀 <PERSON>", out)
	// One offset per code point, allocated for the 9 runes, not the 13 bytes.
	o := runeOffsets(text)
	assert.Len(t, o, 9)
	assert.Equal(t, 9, cap(o))
	assert.Equal(t, 5, o[4])
	assert.Equal(t, 12, o[8])
	assert.Equal(t, 9, runeBytes(text, 5))
	assert.Equal(t, len(text), runeBytes(text, 100))
	assert.Equal(t, 0, runeBytes(text, 0))
}

func TestMapping(t *testing.T) {
	m := newMapping()
	assert.Equal(t, "<PERSON_1>", m.placeholder("PERSON", "Ann", true))
	assert.Equal(t, "<PERSON_2>", m.placeholder("PERSON", "Bob", true))
	assert.Equal(t, "<PERSON_1>", m.placeholder("PERSON", "Ann", false))
	assert.Equal(t, "<EMAIL_ADDRESS_1>", m.placeholder("EMAIL_ADDRESS", "a@b", false), "placeholders = %v", m.byPlaceholder)

	for i := 3; i <= 12; i++ {
		m.placeholder("PERSON", "P"+string(rune('0'+i%10))+string(rune('a'+i)), true)
	}
	text := "<PERSON_12> and <PERSON_1> and <EMAIL_ADDRESS_1> and <PERSON_99>"
	got, n := m.restore(text)
	assert.Equal(t, "P2m and Ann and <EMAIL_ADDRESS_1> and <PERSON_99>", got)
	assert.Equal(t, 2, n)
	got, n = m.restore("nothing here")
	assert.Equal(t, "nothing here", got)
	assert.Equal(t, 0, n)
	assert.True(t, m.hasRestorable())
	assert.False(t, newMapping().hasRestorable())

	var nilMap *mapping
	got, _ = nilMap.restore("<PERSON_1>")
	assert.Equal(t, "<PERSON_1>", got)
}

func TestArgStrings(t *testing.T) {
	tree, strs, ok := argStrings(json.RawMessage(`{"b": ["x", 1, {"c": "y"}], "a": "z", "d": null}`))
	require.True(t, ok)
	require.Equal(t, []string{"z", "x", "y"}, strs)

	out, err := withArgStrings(tree, []string{"Z", "X", "<a&b>"})
	assert.NoError(t, err)
	assert.Equal(t, `{"a":"Z","b":["X",1,{"c":"<a&b>"}],"d":null}`, string(out), "out = %s, %v", out, err)

	for _, raw := range []string{`"text"`, `5`, `not json`, ``} {
		_, _, ok := argStrings(json.RawMessage(raw))
		assert.False(t, ok, "%q accepted", raw)
	}
}
