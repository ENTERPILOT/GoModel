package config

import (
	"strings"
	"testing"
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

	if err := applySessionEnv(cfg); err != nil {
		t.Fatalf("applySessionEnv() error = %v", err)
	}

	headers := cfg.Session.Headers
	if len(headers) != 3 {
		t.Fatalf("headers = %#v, want 3 entries", headers)
	}
	// Env replaces the whole YAML entry with the same name...
	if headers[0].Header != "X-My-Session" || headers[0].Transform != "session-uuid" {
		t.Fatalf("merged entry = %#v, want env override", headers[0])
	}
	// ...keeps unrelated YAML entries, and appends new env entries.
	if headers[1].Header != "X-Other" || headers[2].Header != "X-New-Session" {
		t.Fatalf("headers = %#v", headers)
	}
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
				if err != nil {
					t.Fatalf("normalizeSessionConfig() error = %v", err)
				}
				if got := tt.cfg.Headers[0].Header; got != "X-My-Session" {
					t.Fatalf("canonical header = %q", got)
				}
				if got := tt.cfg.Headers[0].Transform; got != "session-uuid" {
					t.Fatalf("canonical transform = %q", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestSessionDefaults(t *testing.T) {
	cfg := buildDefaultConfig()
	if !cfg.Session.Enabled || !cfg.Session.AutoDetect || !cfg.Session.BuiltinRules {
		t.Fatalf("session defaults = %+v, want all enabled", cfg.Session)
	}
}
