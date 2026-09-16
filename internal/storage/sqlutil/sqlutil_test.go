package sqlutil

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			got := NullableJSONStrings(tt.values, "row-1")
			require.Equal(t, tt.want, got, "NullableJSONStrings(%v)", tt.values)
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
			got := StringsFromJSON(tt.raw, "row-1")
			require.Equal(t, tt.want, got, "StringsFromJSON(%q) = %v, want %v", tt.raw, got, tt.want)
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
			require.True(t, got.Equal(tt.want), "TimeFromUnix(%d) = %s, want %s", tt.value, got, tt.want)

			// Equal compares instants only; a stray location would change the
			// offset every API response serializes.
			assert.Same(t, time.UTC, got.Location(), "TimeFromUnix(%d) location = %s, want UTC", tt.value, got.Location())
			_, err := // The whole point of the clamp: the result must be encodable, or
				// one bad row takes down every listing that includes it.
				json.Marshal(got)
			require.NoError(t, err, "TimeFromUnix(%d) is not JSON-encodable: %v", tt.value, err)
		})
	}
}

func TestTimeFromUnixPtr(t *testing.T) {
	got := TimeFromUnixPtr(nil)
	require.Nil(t, got)

	out := int64(99999999999999)
	got = TimeFromUnixPtr(&out)
	require.NotNil(t, got)
	require.True(t, got.IsZero())
}
