package config

import (
	"strings"
	"testing"
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
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, code := range tt.want {
				if !got[code] {
					t.Errorf("status %d missing", code)
				}
			}
			for _, code := range tt.wantNot {
				if got[code] {
					t.Errorf("status %d unexpectedly present", code)
				}
			}
		})
	}
}

func TestParseFailoverRetryErrors(t *testing.T) {
	phrases, err := parseFailoverRetryErrors([]string{"Model Not Found", "404 deprecated", "4xx upstream"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(phrases) != 3 {
		t.Fatalf("got %d phrases, want 3", len(phrases))
	}
	if got := strings.Join(phrases[0].Words, " "); got != "model not found" || phrases[0].Statuses != nil {
		t.Errorf("phrase 0 = %+v, want lower-cased words and no status", phrases[0])
	}
	if got := strings.Join(phrases[1].Words, " "); got != "deprecated" || !phrases[1].Statuses[404] || phrases[1].Statuses[400] {
		t.Errorf("phrase 1 = %+v, want word 'deprecated' scoped to 404", phrases[1])
	}
	if !phrases[2].Statuses[400] || !phrases[2].Statuses[499] || phrases[2].Statuses[500] {
		t.Errorf("phrase 2 = %+v, want status class 4xx", phrases[2])
	}

	if defaults, err := parseFailoverRetryErrors(nil); err != nil || len(defaults) != len(DefaultFailoverRetryErrors) {
		t.Errorf("empty list: got %d phrases, err %v; want the %d defaults", len(defaults), err, len(DefaultFailoverRetryErrors))
	}
	if _, err := parseFailoverRetryErrors([]string{"404"}); err == nil {
		t.Error("a status-only phrase must be rejected")
	}
	if _, err := parseFailoverRetryErrors([]string{"  "}); err == nil {
		t.Error("a blank phrase must be rejected")
	}
}

func TestLoadFailoverConfig_Policy(t *testing.T) {
	cfg := FailoverConfig{Enabled: true}
	if err := loadFailoverConfig(&cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MaxAttempts != 0 || !cfg.RetryStatuses[429] || !cfg.RetryStatuses[502] || len(cfg.RetryErrors) == 0 {
		t.Errorf("defaults not applied: %+v", cfg)
	}

	cfg = FailoverConfig{Enabled: true, MaxAttempts: -1}
	if err := loadFailoverConfig(&cfg); err == nil || !strings.Contains(err.Error(), "max_attempts") {
		t.Errorf("negative max_attempts: err = %v", err)
	}

	cfg = FailoverConfig{Enabled: true, RetryOnStatuses: []string{"503"}, RetryOnErrors: []string{"overloaded"}}
	if err := loadFailoverConfig(&cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RetryStatuses[429] || !cfg.RetryStatuses[503] || len(cfg.RetryErrors) != 1 {
		t.Errorf("overrides must replace defaults: %+v", cfg)
	}
}

func TestLoadFailoverPolicy_FromYAML(t *testing.T) {
	var cfg FailoverConfig
	withTempDir(t, func(dir string) {
		writeConfigYAML(t, dir, "failover:\n  max_attempts: 2\n  retry_on_statuses: [408, 5xx]\n  retry_on_errors: [\"model not found\", overloaded]\n")
		result, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		cfg = result.Config.Failover
	})
	if cfg.MaxAttempts != 2 {
		t.Errorf("MaxAttempts = %d, want 2", cfg.MaxAttempts)
	}
	if !cfg.RetryStatuses[408] || !cfg.RetryStatuses[503] || cfg.RetryStatuses[429] {
		t.Errorf("RetryStatuses = %v, want 408 and 5xx only", cfg.RetryStatuses)
	}
	if len(cfg.RetryErrors) != 2 || strings.Join(cfg.RetryErrors[1].Words, " ") != "overloaded" {
		t.Errorf("RetryErrors = %+v, want the two configured phrases", cfg.RetryErrors)
	}
}

func TestLoadFailoverPolicy_FromEnv(t *testing.T) {
	t.Setenv("FAILOVER_MAX_ATTEMPTS", "3")
	t.Setenv("FAILOVER_RETRY_ON_STATUSES", "503, 4xx")
	t.Setenv("FAILOVER_RETRY_ON_ERRORS", "overloaded,404 gone")

	result, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg := result.Config.Failover
	if cfg.MaxAttempts != 3 || !cfg.RetryStatuses[503] || !cfg.RetryStatuses[400] || cfg.RetryStatuses[500] {
		t.Errorf("env policy not applied: max=%d statuses=%v", cfg.MaxAttempts, cfg.RetryStatuses)
	}
	if len(cfg.RetryErrors) != 2 || !cfg.RetryErrors[1].Statuses[404] {
		t.Errorf("RetryErrors = %+v, want two phrases with the second scoped to 404", cfg.RetryErrors)
	}
}
