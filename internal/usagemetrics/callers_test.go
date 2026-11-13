package usagemetrics

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
)

func keyNames(names map[string]string) func(string) string {
	return func(id string) string { return names[id] }
}

func TestTruncateUserPath(t *testing.T) {
	tests := []struct {
		userPath string
		depth    int
		want     string
	}{
		{userPath: "", depth: 2, want: "/"},
		{userPath: "/", depth: 2, want: "/"},
		{userPath: "/team", depth: 2, want: "/team"},
		{userPath: "/team/alice", depth: 2, want: "/team/alice"},
		{userPath: "/team/alice/laptop", depth: 2, want: "/team/alice"},
		{userPath: "/team/alice/laptop", depth: 1, want: "/team"},
		{userPath: "/team/alice/laptop", depth: 5, want: "/team/alice/laptop"},
		{userPath: "team//alice/ laptop ", depth: 3, want: "/team/alice/laptop"},
		{userPath: "/team/../alice", depth: 2, want: "/"},
	}
	for _, tt := range tests {
		t.Run(tt.userPath+"@"+strconv.Itoa(tt.depth), func(t *testing.T) {
			assert.Equal(t, tt.want, truncateUserPath(tt.userPath, tt.depth))
		})
	}
}

func TestNewCallersIsNilWhenEveryDimensionIsOff(t *testing.T) {
	callers := NewCallers(config.MetricsCallerDimensionsConfig{UserPathDepth: 2, MaxValues: 100}, keyNames(nil))

	assert.Nil(t, callers)
	assert.Equal(t, Caller{}, callers.Resolve("/team/alice", "key-1", []string{"prod"}))
}

func TestCallersResolveEachDimension(t *testing.T) {
	names := keyNames(map[string]string{"key-1": "ci-pipeline"})
	tests := []struct {
		name string
		cfg  config.MetricsCallerDimensionsConfig
		want Caller
	}{
		{
			name: "user path only",
			cfg:  config.MetricsCallerDimensionsConfig{UserPath: true, UserPathDepth: 2},
			want: Caller{UserPath: "/team/alice"},
		},
		{
			name: "user path depth falls back to the default",
			cfg:  config.MetricsCallerDimensionsConfig{UserPath: true},
			want: Caller{UserPath: "/team/alice"},
		},
		{
			name: "auth key only",
			cfg:  config.MetricsCallerDimensionsConfig{AuthKey: true},
			want: Caller{AuthKey: "ci-pipeline"},
		},
		{
			name: "request labels only",
			cfg:  config.MetricsCallerDimensionsConfig{RequestLabels: true},
			want: Caller{RequestLabels: "prod,team-a"},
		},
		{
			name: "all dimensions",
			cfg:  config.MetricsCallerDimensionsConfig{UserPath: true, UserPathDepth: 1, AuthKey: true, RequestLabels: true},
			want: Caller{UserPath: "/team", AuthKey: "ci-pipeline", RequestLabels: "prod,team-a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ResetCallerValues()
			callers := NewCallers(tt.cfg, names)
			require.NotNil(t, callers)

			got := callers.Resolve("/team/alice/laptop", "key-1", []string{"team-a", " prod ", "team-a", ""})

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCallersResolveAuthKey(t *testing.T) {
	ResetCallerValues()
	callers := NewCallers(config.MetricsCallerDimensionsConfig{AuthKey: true}, keyNames(map[string]string{"key-1": "ci-pipeline"}))

	assert.Equal(t, "ci-pipeline", callers.Resolve("", " key-1 ", nil).AuthKey)
	assert.Empty(t, callers.Resolve("", "", nil).AuthKey, "the master key and unauthenticated traffic have no key")
	assert.Equal(t, UnknownAuthKey, callers.Resolve("", "deleted-key", nil).AuthKey, "a key ID is never exported")
}

func TestCallersWithoutKeyLookupKeepAuthKeyOff(t *testing.T) {
	assert.Nil(t, NewCallers(config.MetricsCallerDimensionsConfig{AuthKey: true}, nil))
}

func TestCallersCapDistinctValuesPerDimension(t *testing.T) {
	ResetCallerValues()
	t.Cleanup(ResetCallerValues)
	callers := NewCallers(config.MetricsCallerDimensionsConfig{
		UserPath: true, UserPathDepth: 1, AuthKey: true, RequestLabels: true, MaxValues: 2,
	}, func(id string) string { return "name-" + id })

	for _, n := range []string{"a", "b"} {
		got := callers.Resolve("/"+n, n, []string{n})
		assert.Equal(t, Caller{UserPath: "/" + n, AuthKey: "name-" + n, RequestLabels: n}, got)
	}

	assert.Equal(t, Caller{UserPath: OtherUserPath, AuthKey: OtherValue, RequestLabels: OtherValue},
		callers.Resolve("/c", "c", []string{"c"}), "values past the cap share the overflow bucket")
	assert.Equal(t, Caller{UserPath: "/a", AuthKey: "name-a", RequestLabels: "a"},
		callers.Resolve("/a/deeper", "a", []string{"a"}), "admitted values keep their own series")
	assert.Equal(t, Caller{UserPath: "/a"},
		callers.Resolve("/a", "", nil), "a dimension without a value is not an overflow")
}

func TestCallersCapOutlivesTheResolver(t *testing.T) {
	ResetCallerValues()
	t.Cleanup(ResetCallerValues)
	cfg := config.MetricsCallerDimensionsConfig{RequestLabels: true, MaxValues: 1}

	assert.Equal(t, "a", NewCallers(cfg, nil).Resolve("", "", []string{"a"}).RequestLabels)
	assert.Equal(t, OtherValue, NewCallers(cfg, nil).Resolve("", "", []string{"b"}).RequestLabels,
		"a reload must not start a new set of series")
}

func TestCallersMaxValuesFallsBackToTheDefault(t *testing.T) {
	ResetCallerValues()
	t.Cleanup(ResetCallerValues)
	callers := NewCallers(config.MetricsCallerDimensionsConfig{RequestLabels: true}, nil)

	for i := range config.DefaultMetricsCallerMaxValues {
		label := "label-" + strconv.Itoa(i)
		require.Equal(t, label, callers.Resolve("", "", []string{label}).RequestLabels)
	}
	assert.Equal(t, OtherValue, callers.Resolve("", "", []string{"one-more"}).RequestLabels)
}
