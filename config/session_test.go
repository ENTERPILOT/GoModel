package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplySessionEnv_ParsesAndMerges(t *testing.T) {
	cfg := &Config{
		Session: SessionConfig{
			Headers: []SessionHeaderConfig{
				{Header: "X-My-Session"},
				{Header: "X-Other"},
			},
		},
	}
	t.Setenv("SESSION_HEADER_1", "X-My-Session")
	t.Setenv("SESSION_HEADER_1_TRANSFORM", "session-uuid")
	t.Setenv("SESSION_HEADER_2", "X-New-Session")
	err := applySessionEnv(cfg)
	require.NoError(t, err)

	headers := cfg.Session.Headers
	require.Len(t, headers, 3)

	// Env replaces the whole YAML entry with the same name...
	require.Equal(t, "X-My-Session", headers[0].Header)
	require.Equal(t, "session-uuid", headers[0].Transform, "merged entry = %#v, want env override", headers[0])

	// ...keeps unrelated YAML entries, and appends new env entries.
	require.Equal(t, "X-Other", headers[1].Header)
	require.Equal(t, "X-New-Session", headers[2].Header, "headers = %#v", headers)
}

func TestNormalizeSessionConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SessionConfig
		wantErr string
	}{
		{
			name: "valid entries canonicalized",
			cfg: SessionConfig{Headers: []SessionHeaderConfig{
				{Header: "x-my-session", Transform: "SESSION-UUID"},
			}},
		},
		{
			name:    "credential header rejected",
			cfg:     SessionConfig{Headers: []SessionHeaderConfig{{Header: "Authorization"}}},
			wantErr: "may carry credentials",
		},
		{
			name: "duplicate header rejected",
			cfg: SessionConfig{Headers: []SessionHeaderConfig{
				{Header: "X-A"}, {Header: "x-a"},
			}},
			wantErr: "duplicate header",
		},
		{
			name:    "unknown transform rejected",
			cfg:     SessionConfig{Headers: []SessionHeaderConfig{{Header: "X-A", Transform: "nope"}}},
			wantErr: "unknown transform",
		},
		{
			name:    "invalid header name rejected",
			cfg:     SessionConfig{Headers: []SessionHeaderConfig{{Header: "bad header"}}},
			wantErr: "invalid HTTP header name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeSessionConfig(&tt.cfg)
			if tt.wantErr == "" {
				require.NoError(t, err)
				got := tt.cfg.Headers[0].Header
				require.Equal(t, "X-My-Session", got)
				got = tt.cfg.Headers[0].Transform
				require.Equal(t, "session-uuid", got)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestSessionDefaults(t *testing.T) {
	cfg := buildDefaultConfig()
	require.True(t, cfg.Session.Enabled)
	require.True(t, cfg.Session.AutoDetect)
	require.True(t, cfg.Session.BuiltinRules, "session defaults = %+v, want all enabled", cfg.Session)
}
