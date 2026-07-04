package sqlutil

import (
	"reflect"
	"testing"
)

func TestNullableJSONStrings(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   any
	}{
		{name: "nil returns SQL NULL", values: nil, want: nil},
		{name: "empty returns SQL NULL", values: []string{}, want: nil},
		{name: "values marshal to a JSON array", values: []string{"team-a", "batch"}, want: `["team-a","batch"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NullableJSONStrings(tt.values, "row-1"); got != tt.want {
				t.Fatalf("NullableJSONStrings(%v) = %v, want %v", tt.values, got, tt.want)
			}
		})
	}
}

func TestStringsFromJSON(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty column yields nil", raw: "", want: nil},
		{name: "empty array yields nil", raw: "[]", want: nil},
		{name: "array parses", raw: `["team-a","batch"]`, want: []string{"team-a", "batch"}},
		{name: "malformed value yields nil", raw: "{not-json", want: nil},
		{name: "wrong JSON type yields nil", raw: `{"a":1}`, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StringsFromJSON(tt.raw, "row-1"); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("StringsFromJSON(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}
