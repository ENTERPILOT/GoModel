package sqlutil

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
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

func TestTimeFromUnix(t *testing.T) {
	tests := []struct {
		name  string
		value int64
		want  time.Time
	}{
		{name: "epoch", value: 0, want: time.Unix(0, 0).UTC()},
		{name: "recent timestamp", value: 1788518356, want: time.Unix(1788518356, 0).UTC()},
		{name: "zero time round trip", value: time.Time{}.Unix(), want: time.Time{}},
		{name: "beyond year 9999 drops to zero time", value: 99999999999999, want: time.Time{}},
		{name: "before year 0 drops to zero time", value: -99999999999999, want: time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TimeFromUnix(tt.value)
			if !got.Equal(tt.want) {
				t.Fatalf("TimeFromUnix(%d) = %s, want %s", tt.value, got, tt.want)
			}
			// The whole point of the clamp: the result must be encodable, or
			// one bad row takes down every listing that includes it.
			if _, err := json.Marshal(got); err != nil {
				t.Fatalf("TimeFromUnix(%d) is not JSON-encodable: %v", tt.value, err)
			}
		})
	}
}

func TestTimeFromUnixPtr(t *testing.T) {
	if got := TimeFromUnixPtr(nil); got != nil {
		t.Fatalf("TimeFromUnixPtr(nil) = %v, want nil", got)
	}
	out := int64(99999999999999)
	if got := TimeFromUnixPtr(&out); got == nil || !got.IsZero() {
		t.Fatalf("TimeFromUnixPtr(out-of-range) = %v, want zero time", got)
	}
}
