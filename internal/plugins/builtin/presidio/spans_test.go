package presidio

import (
	"encoding/json"
	"reflect"
	"testing"
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
	if !reflect.DeepEqual(got, want) {
		t.Errorf("spans = %+v, want %+v", got, want)
	}
	if out := rewrite(text, got, func(s span, v string) string { return "<" + s.entity + ">" }); out != "<DATE_TIME>ë 😀 <PERSON>" {
		t.Errorf("rewrite = %q", out)
	}
	if runeBytes(text, 5) != 9 || runeBytes(text, 100) != len(text) || runeBytes(text, 0) != 0 {
		t.Error("runeBytes")
	}
}

func TestMapping(t *testing.T) {
	m := newMapping()
	if m.placeholder("PERSON", "Ann", true) != "<PERSON_1>" || m.placeholder("PERSON", "Bob", true) != "<PERSON_2>" || m.placeholder("PERSON", "Ann", false) != "<PERSON_1>" || m.placeholder("EMAIL_ADDRESS", "a@b", false) != "<EMAIL_ADDRESS_1>" {
		t.Errorf("placeholders = %v", m.byPlaceholder)
	}
	for i := 3; i <= 12; i++ {
		m.placeholder("PERSON", "P"+string(rune('0'+i%10))+string(rune('a'+i)), true)
	}
	text := "<PERSON_12> and <PERSON_1> and <EMAIL_ADDRESS_1> and <PERSON_99>"
	got, n := m.restore(text)
	if got != "P2m and Ann and <EMAIL_ADDRESS_1> and <PERSON_99>" || n != 2 {
		t.Errorf("restore = %q, %d", got, n)
	}
	if got, n := m.restore("nothing here"); got != "nothing here" || n != 0 {
		t.Errorf("restore = %q, %d", got, n)
	}
	if !m.hasRestorable() || newMapping().hasRestorable() {
		t.Error("hasRestorable")
	}
	var nilMap *mapping
	if got, _ := nilMap.restore("<PERSON_1>"); got != "<PERSON_1>" {
		t.Error("nil mapping")
	}
}

func TestArgStrings(t *testing.T) {
	tree, strs, ok := argStrings(json.RawMessage(`{"b": ["x", 1, {"c": "y"}], "a": "z", "d": null}`))
	if !ok || !reflect.DeepEqual(strs, []string{"z", "x", "y"}) {
		t.Fatalf("strings = %v, ok %v", strs, ok)
	}
	out, err := withArgStrings(tree, []string{"Z", "X", "<a&b>"})
	if err != nil || string(out) != `{"a":"Z","b":["X",1,{"c":"<a&b>"}],"d":null}` {
		t.Errorf("out = %s, %v", out, err)
	}
	for _, raw := range []string{`"text"`, `5`, `not json`, ``} {
		if _, _, ok := argStrings(json.RawMessage(raw)); ok {
			t.Errorf("%q accepted", raw)
		}
	}
}
