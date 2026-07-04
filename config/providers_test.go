package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestRawProviderConfig_HeaderYAMLTags verifies that the 4 new fields
// (custom_upstream_headers, passthrough_user_headers, passthrough_user_headers_skip,
// passthrough_user_headers_skip_mode) are correctly populated when a YAML provider
// block containing them is unmarshalled into RawProviderConfig.
func TestRawProviderConfig_HeaderYAMLTags(t *testing.T) {
	yamlData := `
provider:
  type: openai
  api_key: sk-test
  custom_upstream_headers:
    X-Custom-Header: custom-value
    X-Another-Header: another-value
  passthrough_user_headers: true
  passthrough_user_headers_skip:
    - Authorization
    - Cookie
  passthrough_user_headers_skip_mode: only
`

	var config struct {
		Provider RawProviderConfig `yaml:"provider"`
	}

	if err := yaml.Unmarshal([]byte(yamlData), &config); err != nil {
		t.Fatalf("yaml.Unmarshal failed: %v", err)
	}

	// Verify CustomUpstreamHeaders
	if config.Provider.CustomUpstreamHeaders == nil {
		t.Error("CustomUpstreamHeaders should not be nil")
	} else {
		if got, want := len(config.Provider.CustomUpstreamHeaders), 2; got != want {
			t.Errorf("CustomUpstreamHeaders length: got %d, want %d", got, want)
		}
		if got, want := config.Provider.CustomUpstreamHeaders["X-Custom-Header"], "custom-value"; got != want {
			t.Errorf("CustomUpstreamHeaders[X-Custom-Header]: got %q, want %q", got, want)
		}
		if got, want := config.Provider.CustomUpstreamHeaders["X-Another-Header"], "another-value"; got != want {
			t.Errorf("CustomUpstreamHeaders[X-Another-Header]: got %q, want %q", got, want)
		}
	}

	// Verify PassthroughUserHeaders
	if got, want := config.Provider.PassthroughUserHeaders, true; got != want {
		t.Errorf("PassthroughUserHeaders: got %v, want %v", got, want)
	}

	// Verify PassthroughUserHeadersSkip
	if got, want := len(config.Provider.PassthroughUserHeadersSkip), 2; got != want {
		t.Errorf("PassthroughUserHeadersSkip length: got %d, want %d", got, want)
	}
	if got, want := config.Provider.PassthroughUserHeadersSkip[0], "Authorization"; got != want {
		t.Errorf("PassthroughUserHeadersSkip[0]: got %q, want %q", got, want)
	}
	if got, want := config.Provider.PassthroughUserHeadersSkip[1], "Cookie"; got != want {
		t.Errorf("PassthroughUserHeadersSkip[1]: got %q, want %q", got, want)
	}

	// Verify PassthroughUserHeadersSkipMode
	if got, want := config.Provider.PassthroughUserHeadersSkipMode, "only"; got != want {
		t.Errorf("PassthroughUserHeadersSkipMode: got %q, want %q", got, want)
	}
}

// TestRawProviderConfig_HeaderYAMLTags_Empty verifies that header fields
// default to empty/nil when not present in YAML.
func TestRawProviderConfig_HeaderYAMLTags_Empty(t *testing.T) {
	yamlData := `
provider:
  type: openai
  api_key: sk-test
`

	var config struct {
		Provider RawProviderConfig `yaml:"provider"`
	}

	if err := yaml.Unmarshal([]byte(yamlData), &config); err != nil {
		t.Fatalf("yaml.Unmarshal failed: %v", err)
	}

	// Verify CustomUpstreamHeaders is nil
	if config.Provider.CustomUpstreamHeaders != nil {
		t.Errorf("CustomUpstreamHeaders should be nil when not specified, got %v", config.Provider.CustomUpstreamHeaders)
	}

	// Verify PassthroughUserHeaders defaults to false
	if got, want := config.Provider.PassthroughUserHeaders, false; got != want {
		t.Errorf("PassthroughUserHeaders: got %v, want %v", got, want)
	}

	// Verify PassthroughUserHeadersSkip is nil
	if config.Provider.PassthroughUserHeadersSkip != nil {
		t.Errorf("PassthroughUserHeadersSkip should be nil when not specified, got %v", config.Provider.PassthroughUserHeadersSkip)
	}

	// Verify PassthroughUserHeadersSkipMode is empty string
	if got, want := config.Provider.PassthroughUserHeadersSkipMode, ""; got != want {
		t.Errorf("PassthroughUserHeadersSkipMode: got %q, want %q", got, want)
	}
}