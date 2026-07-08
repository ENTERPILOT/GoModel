package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
				if err == nil {
					t.Fatal("Load() succeeded, want unknown-field error")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %q, want it to contain %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), "in type") {
					t.Fatalf("Load() error leaks an internal Go type name: %q", err)
				}
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
				writeConfigYAML(t, dir, tt.yaml)

				result, err := Load()
				if err != nil {
					t.Fatalf("Load() failed: %v", err)
				}
				if got := len(result.RawProviders); got != tt.wantProviders {
					t.Fatalf("len(RawProviders) = %d, want %d", got, tt.wantProviders)
				}
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
				if err == nil {
					t.Fatal("Load() succeeded, want a multi-document error")
				}
				if !strings.Contains(err.Error(), "only one YAML document is supported") {
					t.Fatalf("Load() error = %q, want a multi-document error", err)
				}
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
		if err != nil {
			t.Fatalf("Load() failed under CONFIG_STRICT=false: %v", err)
		}
		if len(result.RawProviders) != 0 {
			t.Fatalf("len(RawProviders) = %d, want 0 (the keys are not providers)", len(result.RawProviders))
		}
		if result.Config.Server.Port != "9999" {
			t.Fatalf("Server.Port = %q, want the known keys to still apply", result.Config.Server.Port)
		}
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
		if err == nil {
			t.Fatal("Load() succeeded, want a type error even under CONFIG_STRICT=false")
		}
		if !strings.Contains(err.Error(), "cannot unmarshal") {
			t.Fatalf("Load() error = %q, want a type error", err)
		}
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
		if err == nil {
			t.Fatal("Load() succeeded, want the type error to remain fatal")
		}
		if strings.Contains(err.Error(), "bogus_section") {
			t.Fatalf("Load() error = %q, want only the fatal type error reported", err)
		}
	})
}

func TestLoad_ConfigStrictRejectsNonBoolean(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv(envConfigStrict, "yes-please")

	withTempDir(t, func(string) {
		_, err := Load()
		if err == nil {
			t.Fatal("Load() succeeded, want an invalid CONFIG_STRICT error")
		}
		if !strings.Contains(err.Error(), "invalid CONFIG_STRICT") {
			t.Fatalf("Load() error = %q, want it to name CONFIG_STRICT", err)
		}
	})
}

// TestApplyYAML_ExampleConfigParses keeps the shipped example honest: every key it
// documents must exist on Config, or operators who copy it cannot boot under strict
// parsing. It exercises the decode only — Load additionally resolves paths the
// example points at (failover rules), which do not exist relative to a temp dir.
func TestApplyYAML_ExampleConfigParses(t *testing.T) {
	clearAllConfigEnvVars(t)

	example, err := os.ReadFile("config.example.yaml")
	if err != nil {
		t.Fatalf("Failed to read config.example.yaml: %v", err)
	}

	withTempDir(t, func(dir string) {
		writeConfigYAML(t, dir, string(example))

		if _, err := applyYAML(buildDefaultConfig(), true); err != nil {
			t.Fatalf("config.example.yaml does not parse: %v", err)
		}
	})
}

// A path that exists but cannot be read — most often a directory bind-mounted where
// a file was expected — must not be mistaken for a missing config file.
func TestApplyYAML_UnreadableConfigFileIsAnError(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		if err := os.Mkdir(filepath.Join(dir, "config.yaml"), 0755); err != nil {
			t.Fatalf("Failed to create directory: %v", err)
		}

		_, err := applyYAML(buildDefaultConfig(), true)
		if err == nil {
			t.Fatal("applyYAML() succeeded, want read error")
		}
		if !strings.Contains(err.Error(), "failed to read config.yaml") {
			t.Fatalf("applyYAML() error = %q, want a read error naming the file", err)
		}
	})
}

func writeConfigYAML(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(contents), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}
}
