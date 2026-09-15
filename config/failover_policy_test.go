package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFailoverRetryStatuses(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		want    []int
		wantNot []int
		wantErr string
	}{
		{"default", nil, []int{429, 500, 529, 599}, []int{408, 404, 400}, ""},
		{"exact codes", []string{"408", "503"}, []int{408, 503}, []int{429, 500}, ""},
		{"class", []string{"4xx"}, []int{400, 429, 499}, []int{500}, ""},
		{"mixed case class", []string{"5XX"}, []int{500}, nil, ""},
		{"invalid code", []string{"999"}, nil, nil, "not an HTTP status code"},
		{"invalid class", []string{"6xx"}, nil, nil, "not an HTTP status code"},
		{"garbage", []string{"abc"}, nil, nil, "not an HTTP status code"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFailoverRetryStatuses(tt.entries)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)

			for _, code := range tt.want {
				assert.True(t, got[code], "status %d missing", code)
			}
			for _, code := range tt.wantNot {
				assert.False(t, got[code], "status %d unexpectedly present", code)
			}
		})
	}
}

func TestParseFailoverRetryErrors(t *testing.T) {
	phrases, err := parseFailoverRetryErrors([]string{"Model Not Found", "404 deprecated", "4xx upstream"})
	require.NoError(t, err)
	require.Len(t, phrases, 3)
	got := strings.Join(phrases[0].Words, " ")
	assert.Equal(t, "model not found", got)
	assert.Nil(t, phrases[0].Statuses, "phrase 0 = %+v, want lower-cased words and no status", phrases[0])

	assert.Equal(t, "deprecated", strings.Join(phrases[1].Words, " "))
	assert.True(t, phrases[1].Statuses[404], "phrase 1 must be scoped to 404")
	assert.False(t, phrases[1].Statuses[400], "phrase 1 must not match 400")
	assert.True(t, phrases[2].Statuses[400], "phrase 2 must cover the 4xx class")
	assert.True(t, phrases[2].Statuses[499], "phrase 2 must cover the 4xx class")
	assert.False(t, phrases[2].Statuses[500], "phrase 2 must not cover 500")
	defaults, err := parseFailoverRetryErrors(nil)
	assert.NoError(t, err)
	assert.Len(t, defaults, len(DefaultFailoverRetryErrors))
	_, err = parseFailoverRetryErrors([]string{"404"})
	assert.Error(t, err)
	_, err = parseFailoverRetryErrors([]string{"  "})
	assert.Error(t, err)
}

func TestLoadFailoverConfig_Policy(t *testing.T) {
	cfg := FailoverConfig{Enabled: true}
	err := loadFailoverConfig(&cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, cfg.MaxAttempts)
	assert.True(t, cfg.RetryStatuses[429])
	assert.True(t, cfg.RetryStatuses[502])
	assert.NotEmpty(t, cfg.RetryErrors, "defaults not applied: %+v", cfg)

	cfg = FailoverConfig{Enabled: true, MaxAttempts: -1}
	err = loadFailoverConfig(&cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "max_attempts")

	cfg = FailoverConfig{Enabled: true, RetryOnStatuses: []string{"503"}, RetryOnErrors: []string{"overloaded"}}
	err = loadFailoverConfig(&cfg)
	require.NoError(t, err)
	assert.False(t, cfg.RetryStatuses[429], "overrides must replace default statuses")
	assert.True(t, cfg.RetryStatuses[503])
	assert.Len(t, cfg.RetryErrors, 1, "overrides must replace default errors")
}

func TestLoadFailoverPolicy_FromYAML(t *testing.T) {
	var cfg FailoverConfig
	withTempDir(t, func(dir string) {
		result := loadConfigYAML(t, dir, "failover:\n  max_attempts: 2\n  retry_on_statuses: [408, 5xx]\n  retry_on_errors: [\"model not found\", overloaded]\n")

		cfg = result.Config.Failover
	})
	assert.Equal(t, 2, cfg.MaxAttempts)
	assert.True(t, cfg.RetryStatuses[408])
	assert.True(t, cfg.RetryStatuses[503])
	assert.False(t, cfg.RetryStatuses[429], "RetryStatuses = %v, want 408 and 5xx only", cfg.RetryStatuses)
	assert.Len(t, cfg.RetryErrors, 2)
	assert.Equal(t, "overloaded", strings.Join(cfg.RetryErrors[1].Words, " "))
}

func TestLoadFailoverPolicy_FromEnv(t *testing.T) {
	t.Setenv("FAILOVER_MAX_ATTEMPTS", "3")
	t.Setenv("FAILOVER_RETRY_ON_STATUSES", "503, 4xx")
	t.Setenv("FAILOVER_RETRY_ON_ERRORS", "overloaded,404 gone")

	result, err := Load()
	require.NoError(t, err)

	cfg := result.Config.Failover
	assert.Equal(t, 3, cfg.MaxAttempts)
	assert.True(t, cfg.RetryStatuses[503])
	assert.True(t, cfg.RetryStatuses[400])
	assert.False(t, cfg.RetryStatuses[500], "env policy not applied: statuses=%v", cfg.RetryStatuses)
	assert.Len(t, cfg.RetryErrors, 2)
	assert.True(t, cfg.RetryErrors[1].Statuses[404])
}
