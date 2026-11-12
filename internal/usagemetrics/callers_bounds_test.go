package usagemetrics

import (
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
)

func allDimensions(maxValues int) config.MetricsCallerDimensionsConfig {
	return config.MetricsCallerDimensionsConfig{
		UserPath: true, UserPathDepth: 1, AuthKey: true, RequestLabels: true, MaxValues: maxValues,
	}
}

// Over-long, reserved, and non-UTF-8 values go to the overflow bucket without
// taking a slot, so the two legitimate values after them are still admitted.
func TestCallersRejectOverlongAndReservedValues(t *testing.T) {
	long := strings.Repeat("x", MaxValueBytes+1)
	tests := []struct {
		name     string
		userPath string
		keyName  string
		labels   []string
	}{
		{name: "over-long", userPath: "/" + long, keyName: long, labels: []string{long}},
		{name: "spoofed overflow", userPath: OtherUserPath, keyName: OtherValue, labels: []string{OtherValue}},
		{name: "spoofed unknown", userPath: "/" + UnknownAuthKey, keyName: UnknownAuthKey, labels: []string{UnknownAuthKey}},
		{name: "invalid UTF-8", userPath: "/team\xff", keyName: "ci\xff", labels: []string{"prod", "team\xff"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ResetCallerValues()
			t.Cleanup(ResetCallerValues)
			callers := NewCallers(allDimensions(2), keyNames(map[string]string{"bad": tt.keyName, "a": "a", "b": "b"}))

			for range 3 {
				assert.Equal(t, Caller{UserPath: OtherUserPath, AuthKey: OtherValue, RequestLabels: OtherValue},
					callers.Resolve(tt.userPath, "bad", tt.labels))
			}
			for _, n := range []string{"a", "b"} {
				assert.Equal(t, Caller{UserPath: "/" + n, AuthKey: n, RequestLabels: n},
					callers.Resolve("/"+n, n, []string{n}), "the rejected value took a slot")
			}
		})
	}
}

func TestCallersAdmitValuesUpToTheByteLimit(t *testing.T) {
	ResetCallerValues()
	t.Cleanup(ResetCallerValues)
	callers := NewCallers(config.MetricsCallerDimensionsConfig{RequestLabels: true}, nil)
	label := strings.Repeat("x", MaxValueBytes)

	assert.Equal(t, label, callers.Resolve("", "", []string{label}).RequestLabels)
}

func TestCallersUnknownAuthKeyTakesNoSlot(t *testing.T) {
	ResetCallerValues()
	t.Cleanup(ResetCallerValues)
	callers := NewCallers(config.MetricsCallerDimensionsConfig{AuthKey: true, MaxValues: 1}, keyNames(map[string]string{"a": "a"}))

	assert.Equal(t, UnknownAuthKey, callers.Resolve("", "deleted", nil).AuthKey)
	assert.Equal(t, "a", callers.Resolve("", "a", nil).AuthKey)
}

// Every distinct label set is one value: a client cycling through subsets of
// a few labels fills the cap, and later sets, legitimate or not, share the
// overflow bucket until restart.
func TestCallersLabelSetsExhaustTheCap(t *testing.T) {
	ResetCallerValues()
	t.Cleanup(ResetCallerValues)
	callers := NewCallers(config.MetricsCallerDimensionsConfig{RequestLabels: true, MaxValues: 4}, nil)

	for _, set := range [][]string{{"a"}, {"b"}, {"a", "b"}, {"a", "b", "c"}} {
		require.Equal(t, joinLabels(set), callers.Resolve("", "", set).RequestLabels)
	}

	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{name: "admitted set in another order", labels: []string{"b", "a"}, want: "a,b"},
		{name: "new legitimate set", labels: []string{"prod", "team-a"}, want: OtherValue},
		{name: "new single label", labels: []string{"prod"}, want: OtherValue},
		{name: "no labels", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, callers.Resolve("", "", tt.labels).RequestLabels)
		})
	}
}

func TestCallersResolveRacesReset(t *testing.T) {
	ResetCallerValues()
	t.Cleanup(ResetCallerValues)
	callers := NewCallers(allDimensions(8), func(id string) string { return "name-" + id })

	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for i := range 200 {
				n := strconv.Itoa((w + i) % 16)
				got := callers.Resolve("/"+n, n, []string{n})
				assert.Contains(t, []string{"/" + n, OtherUserPath}, got.UserPath)
				assert.Contains(t, []string{"name-" + n, OtherValue}, got.AuthKey)
				assert.Contains(t, []string{n, OtherValue}, got.RequestLabels)
			}
		})
	}
	wg.Go(func() {
		for range 50 {
			ResetCallerValues()
		}
	})
	wg.Wait()
}

// A comma in the value always separates two labels, so no two label sets
// share a value.
func TestJoinLabelsIsUnambiguous(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{name: "no labels", want: ""},
		{name: "one label", labels: []string{"prod"}, want: "prod"},
		{name: "sorted and deduplicated", labels: []string{"team-a", "prod", " prod "}, want: "prod,team-a"},
		{name: "two labels", labels: []string{"a", "b"}, want: "a,b"},
		{name: "one label with a comma", labels: []string{"a,b"}, want: "a%2Cb"},
		{name: "a label spelled like an escaped comma", labels: []string{"a%2Cb"}, want: "a%252Cb"},
		{name: "sorted before escaping", labels: []string{"b", "a,c"}, want: "a%2Cc,b"},
	}
	seen := map[string]string{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinLabels(tt.labels)
			assert.Equal(t, tt.want, got)
			assert.Empty(t, seen[got], "another label set has the same value")
			seen[got] = tt.name
		})
	}
}
