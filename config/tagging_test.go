package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyTaggingEnv_ParsesAndMerges(t *testing.T) {
	cfg := &Config{Tagging: TaggingConfig{Headers: []TaggingHeaderConfig{
		{Header: "X-Team", Prefix: "team-"},
		{Header: "X-Keep"},
	}}}
	t.Setenv("TAGGING_HEADER_1", "X-Team")
	t.Setenv("TAGGING_HEADER_1_DONOTPASS", "true")
	t.Setenv("TAGGING_HEADER_2", "X-Cost-Center")
	t.Setenv("TAGGING_HEADER_2_PREFIX", "cc-")
	t.Setenv("TAGGING_HEADER_2_DELIMITER", ";")
	err := applyTaggingEnv(cfg)
	require.NoError(t, err)

	headers := cfg.Tagging.Headers
	require.Len(t, headers, 3)

	// "X-Team" is overridden in place (env wins) and keeps its position.
	team := headers[0]
	require.Equal(t, "X-Team", team.Header)
	require.True(t, team.DoNotPass)
	require.Empty(t, team.Prefix, "env did not override X-Team: %#v", team)

	// "X-Keep" is untouched; "X-Cost-Center" is appended.
	require.Equal(t, "X-Keep", headers[1].Header, "merge order wrong: %#v", headers)

	cc := headers[2]
	require.Equal(t, "X-Cost-Center", cc.Header)
	require.Equal(t, "cc-", cc.Prefix)
	require.Equal(t, ";", cc.Delimiter)
	require.False(t, cc.DoNotPass, "env entry wrong: %#v", cc)
}

func TestApplyTaggingEnv_SortsByIndexAndSkipsGaps(t *testing.T) {
	cfg := &Config{}
	t.Setenv("TAGGING_HEADER_10", "X-Ten")
	t.Setenv("TAGGING_HEADER_2", "X-Two")
	err := applyTaggingEnv(cfg)
	require.NoError(t, err)

	headers := cfg.Tagging.Headers
	require.Len(t, headers, 2)
	require.Equal(t, "X-Two", headers[0].Header)
	require.Equal(t, "X-Ten", headers[1].Header)
}

func TestApplyTaggingEnv_Unset(t *testing.T) {
	cfg := &Config{Tagging: TaggingConfig{Headers: []TaggingHeaderConfig{{Header: "X-Team"}}}}
	err := applyTaggingEnv(cfg)
	require.NoError(t, err)
	require.Len(t, cfg.Tagging.Headers, 1)
}

func TestNormalizeTaggingConfig(t *testing.T) {
	cfg := &TaggingConfig{Headers: []TaggingHeaderConfig{
		{Header: "x-team "},
		{Header: "X-Cost-Center", Delimiter: ";"},
	}}
	err := normalizeTaggingConfig(cfg)
	require.NoError(t, err)
	require.Equal(t, "X-Team", cfg.Headers[0].Header, "header not canonicalized: %#v", cfg.Headers[0])
	require.Equal(t, DefaultTaggingDelimiter, cfg.Headers[0].Delimiter, "default delimiter not applied: %#v", cfg.Headers[0])
	require.Equal(t, ";", cfg.Headers[1].Delimiter, "explicit delimiter overwritten: %#v", cfg.Headers[1])
}

func TestNormalizeTaggingConfig_Rejections(t *testing.T) {
	tests := []struct {
		name    string
		headers []TaggingHeaderConfig
	}{
		{name: "invalid header name", headers: []TaggingHeaderConfig{{Header: "bad header"}}},
		{name: "empty header name", headers: []TaggingHeaderConfig{{Header: "  "}}},
		{name: "duplicate header name", headers: []TaggingHeaderConfig{{Header: "X-Team"}, {Header: "x-team"}}},
		{name: "credential header authorization", headers: []TaggingHeaderConfig{{Header: "Authorization"}}},
		{name: "credential header cookie", headers: []TaggingHeaderConfig{{Header: "cookie"}}},
		{name: "credential header api key", headers: []TaggingHeaderConfig{{Header: "X-Api-Key"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TaggingConfig{Headers: tt.headers}
			require.Error(t, normalizeTaggingConfig(cfg), "expected error for %s", tt.name)
		})
	}
}

func TestLoad_TaggingFromYAMLAndEnv(t *testing.T) {
	clearAllConfigEnvVars(t)
	withTempDir(t, func(dir string) {
		yaml := `
tagging:
  headers:
    - header: X-Team
      prefix: "team-"
      do_not_pass: true
    - header: X-Env
`
		writeConfigYAML(t, dir, yaml)

		t.Setenv("TAGGING_HEADER_1", "X-Cost-Center")
		t.Setenv("TAGGING_HEADER_1_PREFIX", "cc-")

		result, err := Load()
		require.NoError(t, err)

		headers := result.Config.Tagging.Headers
		require.Len(t, headers, 3)
		require.Equal(t, "X-Team", headers[0].Header)
		require.Equal(t, "team-", headers[0].Prefix)
		require.True(t, headers[0].DoNotPass, "yaml entry wrong: %#v", headers[0])
		require.Equal(t, DefaultTaggingDelimiter, headers[0].Delimiter, "default delimiter not applied: %#v", headers[0])
		require.Equal(t, "X-Cost-Center", headers[2].Header)
		require.Equal(t, "cc-", headers[2].Prefix, "env entry wrong: %#v", headers[2])
	})
}
