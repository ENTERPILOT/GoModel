package config

import (
	"os"
	"path/filepath"
	"testing"
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

	if err := applyTaggingEnv(cfg); err != nil {
		t.Fatalf("applyTaggingEnv() error = %v", err)
	}
	headers := cfg.Tagging.Headers
	if len(headers) != 3 {
		t.Fatalf("merged len = %d, want 3: %#v", len(headers), headers)
	}
	// "X-Team" is overridden in place (env wins) and keeps its position.
	team := headers[0]
	if team.Header != "X-Team" || !team.DoNotPass || team.Prefix != "" {
		t.Fatalf("env did not override X-Team: %#v", team)
	}
	// "X-Keep" is untouched; "X-Cost-Center" is appended.
	if headers[1].Header != "X-Keep" {
		t.Fatalf("merge order wrong: %#v", headers)
	}
	cc := headers[2]
	if cc.Header != "X-Cost-Center" || cc.Prefix != "cc-" || cc.Delimiter != ";" || cc.DoNotPass {
		t.Fatalf("env entry wrong: %#v", cc)
	}
}

func TestApplyTaggingEnv_SortsByIndexAndSkipsGaps(t *testing.T) {
	cfg := &Config{}
	t.Setenv("TAGGING_HEADER_10", "X-Ten")
	t.Setenv("TAGGING_HEADER_2", "X-Two")

	if err := applyTaggingEnv(cfg); err != nil {
		t.Fatalf("applyTaggingEnv() error = %v", err)
	}
	headers := cfg.Tagging.Headers
	if len(headers) != 2 || headers[0].Header != "X-Two" || headers[1].Header != "X-Ten" {
		t.Fatalf("index ordering wrong: %#v", headers)
	}
}

func TestApplyTaggingEnv_Unset(t *testing.T) {
	cfg := &Config{Tagging: TaggingConfig{Headers: []TaggingHeaderConfig{{Header: "X-Team"}}}}
	if err := applyTaggingEnv(cfg); err != nil {
		t.Fatalf("applyTaggingEnv() error = %v", err)
	}
	if len(cfg.Tagging.Headers) != 1 {
		t.Fatalf("no env vars mutated config: %#v", cfg.Tagging.Headers)
	}
}

func TestNormalizeTaggingConfig(t *testing.T) {
	cfg := &TaggingConfig{Headers: []TaggingHeaderConfig{
		{Header: "x-team "},
		{Header: "X-Cost-Center", Delimiter: ";"},
	}}
	if err := normalizeTaggingConfig(cfg); err != nil {
		t.Fatalf("normalizeTaggingConfig() error = %v", err)
	}
	if cfg.Headers[0].Header != "X-Team" {
		t.Fatalf("header not canonicalized: %#v", cfg.Headers[0])
	}
	if cfg.Headers[0].Delimiter != DefaultTaggingDelimiter {
		t.Fatalf("default delimiter not applied: %#v", cfg.Headers[0])
	}
	if cfg.Headers[1].Delimiter != ";" {
		t.Fatalf("explicit delimiter overwritten: %#v", cfg.Headers[1])
	}
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
			if err := normalizeTaggingConfig(cfg); err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
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
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
			t.Fatalf("failed to write config.yaml: %v", err)
		}
		t.Setenv("TAGGING_HEADER_1", "X-Cost-Center")
		t.Setenv("TAGGING_HEADER_1_PREFIX", "cc-")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		headers := result.Config.Tagging.Headers
		if len(headers) != 3 {
			t.Fatalf("headers len = %d, want 3: %#v", len(headers), headers)
		}
		if headers[0].Header != "X-Team" || headers[0].Prefix != "team-" || !headers[0].DoNotPass {
			t.Fatalf("yaml entry wrong: %#v", headers[0])
		}
		if headers[0].Delimiter != DefaultTaggingDelimiter {
			t.Fatalf("default delimiter not applied: %#v", headers[0])
		}
		if headers[2].Header != "X-Cost-Center" || headers[2].Prefix != "cc-" {
			t.Fatalf("env entry wrong: %#v", headers[2])
		}
	})
}
