package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLoad_RejectsUnknownYAMLFields locks in strict parsing: an unknown key is a
// startup error, never a silently dropped section. The misindented providers:
// block is the motivating case — it parses as a null section plus unknown
// top-level keys, so the gateway would otherwise boot with no providers at all.
func TestLoad_RejectsUnknownYAMLFields(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "misindented providers section",
			yaml: `
server:
  port: "9999"

providers:
uranium-geryon-9b:
  type: vllm
  base_url: "http://uranium-geryon-9b:8000/v1"
`,
			wantErr: `field uranium-geryon-9b not found`,
		},
		{
			name: "unknown top-level key",
			yaml: "bogus_section:\n  a: 1\n",
			// Reported against the file, without yaml.v3's internal Go type name.
			wantErr: "failed to parse config.yaml: line 1: field bogus_section not found",
		},
		{
			name:    "unknown nested key",
			yaml:    "server:\n  prot: \"9999\"\n",
			wantErr: "field prot not found",
		},
		{
			name:    "unknown provider key",
			yaml:    "providers:\n  local:\n    type: vllm\n    bass_url: \"http://x:8000/v1\"\n",
			wantErr: "field bass_url not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			withTempDir(t, func(dir string) {
				writeConfigYAML(t, dir, tt.yaml)

				_, err := Load()
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				require.False(t, strings.Contains(err.Error(), "in type"))
			})
		})
	}
}

// TestLoad_AcceptsValidYAMLShapes guards the edges strict parsing must not break:
// an empty or comments-only file is an empty overlay, a correctly indented
// providers: block still loads, and the removed failover.overrides key is still
// tolerated so an old config file keeps booting.
func TestLoad_AcceptsValidYAMLShapes(t *testing.T) {
	tests := []struct {
		name          string
		yaml          string
		wantProviders int
	}{
		{name: "empty file", yaml: ""},
		{name: "comments only", yaml: "# nothing to see here\n"},
		{name: "explicit null providers", yaml: "providers:\n"},
		{name: "document end marker", yaml: "server:\n  port: \"9999\"\n...\n"},
		{
			name:          "correctly indented providers",
			yaml:          "providers:\n  local:\n    type: vllm\n    base_url: \"http://x:8000/v1\"\n",
			wantProviders: 1,
		},
		{
			name: "legacy failover overrides are tolerated",
			yaml: "failover:\n  overrides:\n    \"gpt-4o\":\n      mode: \"off\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			withTempDir(t, func(dir string) {
				result := loadConfigYAML(t, dir, tt.yaml)
				got := len(result.RawProviders)
				require.Equal(t, tt.wantProviders, got)
			})
		})
	}
}

// A second YAML document is never applied, so accepting one silently drops every key
// it declares. Structural, therefore fatal regardless of CONFIG_STRICT.
func TestLoad_RejectsMultipleYAMLDocuments(t *testing.T) {
	for _, strict := range []string{"true", "false"} {
		t.Run("CONFIG_STRICT="+strict, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			t.Setenv(envConfigStrict, strict)

			withTempDir(t, func(dir string) {
				writeConfigYAML(t, dir, "providers:\n  a:\n    type: vllm\n    base_url: \"http://a:8000/v1\"\n---\nproviders:\n  b:\n    type: vllm\n    base_url: \"http://b:8000/v1\"\n")

				_, err := Load()
				require.Error(t, err)
				require.Contains(t, err.Error(), "only one YAML document is supported")
			})
		})
	}
}

// CONFIG_STRICT=false relaxes what the schema accepts, so an unknown key becomes a
// warning and the rest of the file still applies.
func TestLoad_ConfigStrictFalseDowngradesUnknownKeysToWarnings(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv(envConfigStrict, "false")

	withTempDir(t, func(dir string) {
		// The reporter's misindented file: four keys that should have been providers,
		// plus a server section that must still take effect.
		writeConfigYAML(t, dir, `
server:
  port: "9999"

providers:
uranium-geryon-9b:
  type: vllm
  base_url: "http://uranium-geryon-9b:8000/v1"
`)

		result, err := Load()
		require.NoError(t, err)
		require.Empty(t, result.RawProviders)
		require.Equal(t, "9999", result.Config.Server.Port)
	})
}

// CONFIG_STRICT relaxes which keys are accepted, never whether a value makes sense.
// A malformed value is a broken config in any mode.
func TestLoad_ConfigStrictFalseStillRejectsMalformedValues(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv(envConfigStrict, "false")

	withTempDir(t, func(dir string) {
		writeConfigYAML(t, dir, "server:\n  port: [9999, 8080]\n")

		_, err := Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), "cannot unmarshal")
	})
}

// A file that mixes an unknown key with a malformed value must still fail: the
// unknown key is downgraded, the type error is not.
func TestLoad_ConfigStrictFalseFailsWhenAnyErrorIsFatal(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv(envConfigStrict, "false")

	withTempDir(t, func(dir string) {
		writeConfigYAML(t, dir, "bogus_section:\n  a: 1\nserver:\n  port: [9999]\n")

		_, err := Load()
		require.Error(t, err)
		require.False(t, strings.Contains(err.Error(), "bogus_section"))
	})
}

func TestLoad_ConfigStrictRejectsNonBoolean(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv(envConfigStrict, "yes-please")

	withTempDir(t, func(string) {
		_, err := Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid CONFIG_STRICT")
	})
}

// TestApplyYAML_ExampleConfigParses keeps the shipped example honest: every key it
// documents must exist on Config, or operators who copy it cannot boot under strict
// parsing. It exercises the decode only.
func TestApplyYAML_ExampleConfigParses(t *testing.T) {
	clearAllConfigEnvVars(t)

	example, err := os.ReadFile("config.example.yaml")
	require.NoError(t, err)

	withTempDir(t, func(dir string) {
		writeConfigYAML(t, dir, string(example))
		_, err := applyYAML(buildDefaultConfig(), true)
		require.NoError(t, err)
	})
}

// A path that exists but cannot be read — most often a directory bind-mounted where
// a file was expected — must not be mistaken for a missing config file.
func TestApplyYAML_UnreadableConfigFileIsAnError(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		err := os.Mkdir(filepath.Join(dir, "config.yaml"), 0755)
		require.NoError(t, err)

		_, err = applyYAML(buildDefaultConfig(), true)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to read config.yaml")
	})
}

func writeConfigYAML(t *testing.T, dir, contents string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(contents), 0644)
	require.NoError(t, err)
}

// loadConfigYAML writes contents as dir/config.yaml and loads it, failing the
// test on any load error.
func loadConfigYAML(t *testing.T, dir, contents string) *LoadResult {
	t.Helper()
	writeConfigYAML(t, dir, contents)
	result, err := Load()
	require.NoError(t, err)
	return result
}
