package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// loadConfigFiles writes files, keyed by path relative to a fresh working
// directory, and loads the configuration from there.
func loadConfigFiles(t *testing.T, files map[string]string) (*LoadResult, error) {
	t.Helper()
	var (
		result *LoadResult
		err    error
	)
	withTempDir(t, func(string) {
		writeFiles(t, files)
		result, err = Load()
	})
	return result, err
}

func writeFiles(t *testing.T, files map[string]string) {
	t.Helper()
	for path, contents := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	}
}

func TestLoad_MergesIncludedFiles(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("GOMODEL_TEST_INCLUDE_DIR", "secrets")
	t.Setenv("GOMODEL_TEST_INCLUDE_KEY", "sk-from-env")

	result, err := loadConfigFiles(t, map[string]string{
		// Paths are relative to the config file, not the working directory.
		"config/config.yaml": `
include:
  - providers.yaml
  - "${GOMODEL_TEST_INCLUDE_DIR}/*.yaml"
server:
  port: "9999"
providers:
  local:
    type: vllm
    base_url: "http://local:8000/v1"
`,
		"config/providers.yaml": `
providers:
  openai:
    type: openai
    models: ["gpt-4o"]
failover:
  enabled: false
`,
		// A piece can hold one key of a provider another file declares.
		"config/secrets/10-openai.yaml": "providers:\n  openai:\n    api_key: \"${GOMODEL_TEST_INCLUDE_KEY}\"\n",
		"config/secrets/20-local.yaml":  "providers:\n  local:\n    api_key: local-key\n",
		"config/secrets/notes.txt":      "not: [yaml",
	})
	require.NoError(t, err)

	assert.Equal(t, "9999", result.Config.Server.Port)
	assert.False(t, result.Config.Failover.Enabled)
	// Settings no file names keep their defaults.
	assert.Equal(t, 600, result.Config.HTTP.Timeout)
	require.Len(t, result.RawProviders, 2)
	openai := result.RawProviders["openai"]
	assert.Equal(t, "openai", openai.Type)
	assert.Equal(t, "sk-from-env", openai.APIKey)
	require.Len(t, openai.Models, 1)
	assert.Equal(t, "gpt-4o", openai.Models[0].ID)
	local := result.RawProviders["local"]
	assert.Equal(t, "http://local:8000/v1", local.BaseURL)
	assert.Equal(t, "local-key", local.APIKey)
}

func TestLoad_IncludeEdgeCases(t *testing.T) {
	const provider = "providers:\n  a:\n    type: vllm\n    base_url: \"http://a:8000/v1\"\n"
	tests := []struct {
		name          string
		files         map[string]string
		wantProviders int
	}{
		{
			name:  "empty list",
			files: map[string]string{"config.yaml": "include: []\n"},
		},
		{
			name:  "pattern in a missing directory",
			files: map[string]string{"config.yaml": "include: [\"conf.d/*.yaml\"]\n"},
		},
		{
			name:  "pattern matching nothing",
			files: map[string]string{"config.yaml": "include: [\"conf.d/*.yaml\"]\n", "conf.d/readme.txt": "x"},
		},
		{
			name:          "pattern matching the config file itself",
			files:         map[string]string{"config.yaml": "include: [\"*.yaml\"]\n" + provider},
			wantProviders: 1,
		},
		{
			name:          "file named twice is read once",
			files:         map[string]string{"config.yaml": "include: [a.yaml, \"*.yaml\", ./a.yaml]\n", "a.yaml": provider},
			wantProviders: 1,
		},
		{
			name:          "file named by another path is read once",
			files:         map[string]string{"config.yaml": "include: [a.yaml, \"${GOMODEL_TEST_INCLUDE_CWD}/a.yaml\", \"${GOMODEL_TEST_INCLUDE_CWD}/config.yaml\"]\n", "a.yaml": provider},
			wantProviders: 1,
		},
		{
			name:          "entry is a path as written",
			files:         map[string]string{"config.yaml": "include: [\" a.yaml\"]\n", " a.yaml": provider},
			wantProviders: 1,
		},
		{
			name:          "pattern skips directories",
			files:         map[string]string{"config.yaml": "include: [\"conf.d/*\"]\n", "conf.d/a.yaml": provider, "conf.d/..data/a.yaml": provider},
			wantProviders: 1,
		},
		{
			name:          "empty and comment-only files",
			files:         map[string]string{"config.yaml": "include: [empty.yaml, comments.yaml, a.yaml]\n", "empty.yaml": "", "comments.yaml": "# later\n", "a.yaml": provider},
			wantProviders: 1,
		},
		{
			name:          "empty section yields to the file that fills it",
			files:         map[string]string{"config.yaml": "include: [a.yaml, b.yaml]\nproviders:\n", "a.yaml": provider, "b.yaml": "providers:\n"},
			wantProviders: 1,
		},
		{
			name:          "absolute path",
			files:         map[string]string{"config.yaml": "include: [\"${GOMODEL_TEST_INCLUDE_ABS}/a.yaml\"]\n", "elsewhere/a.yaml": provider},
			wantProviders: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			withTempDir(t, func(dir string) {
				t.Setenv("GOMODEL_TEST_INCLUDE_ABS", filepath.Join(dir, "elsewhere"))
				t.Setenv("GOMODEL_TEST_INCLUDE_CWD", dir)
				writeFiles(t, tt.files)

				result, err := Load()
				require.NoError(t, err)
				assert.Len(t, result.RawProviders, tt.wantProviders)
			})
		})
	}
}

func TestLoad_RejectsBrokenIncludes(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{
			name:    "named file is missing",
			files:   map[string]string{"config/config.yaml": "include: [providers.yaml]\n"},
			wantErr: "failed to read config/providers.yaml, included from config/config.yaml",
		},
		{
			name:    "malformed pattern",
			files:   map[string]string{"config.yaml": "include: [\"conf.d/[.yaml\"]\n"},
			wantErr: `failed to parse config.yaml: include "conf.d/[.yaml": syntax error in pattern`,
		},
		{
			name:    "empty entry",
			files:   map[string]string{"config.yaml": "include: [a.yaml, \" \"]\n", "a.yaml": ""},
			wantErr: "failed to parse config.yaml: include holds an empty entry",
		},
		{
			name:    "not a list",
			files:   map[string]string{"config.yaml": "include: providers.yaml\n"},
			wantErr: "failed to parse config.yaml: line 1: cannot unmarshal",
		},
		{
			name:    "syntax error in an included file",
			files:   map[string]string{"config.yaml": "include: [a.yaml]\n", "a.yaml": "providers: [unclosed\n"},
			wantErr: "failed to parse a.yaml: line",
		},
		{
			name:    "unknown key in an included file",
			files:   map[string]string{"config.yaml": "include: [a.yaml]\n", "a.yaml": "server:\n  prot: \"9999\"\n"},
			wantErr: "failed to parse a.yaml: line 2: field prot not found",
		},
		{
			name:    "second document in an included file",
			files:   map[string]string{"config.yaml": "include: [a.yaml]\n", "a.yaml": "server:\n  port: \"1\"\n---\nserver:\n  port: \"2\"\n"},
			wantErr: "failed to parse a.yaml: only one YAML document is supported",
		},
		{
			name:    "include in an included file",
			files:   map[string]string{"config.yaml": "include: [a.yaml]\n", "a.yaml": "include: [b.yaml]\n", "b.yaml": ""},
			wantErr: "failed to parse a.yaml: include is supported only in config.yaml",
		},
		{
			name:    "value set in two files",
			files:   map[string]string{"config.yaml": "include: [a.yaml]\nserver:\n  port: \"9999\"\n", "a.yaml": "\nserver:\n  port: \"9999\"\n"},
			wantErr: "failed to parse a.yaml: line 3: server.port is already set in config.yaml (line 3); a setting can be set in only one config file",
		},
		{
			name: "value set in two included files",
			files: map[string]string{
				"config.yaml":    "include: [\"conf.d/*.yaml\"]\nproviders:\n  a:\n    type: vllm\n",
				"conf.d/10.yaml": "providers:\n  a:\n    base_url: \"http://a:8000/v1\"\n",
				"conf.d/20.yaml": "providers:\n  a:\n    base_url: \"http://b:8000/v1\"\n",
			},
			wantErr: "failed to parse conf.d/20.yaml: line 3: providers.a.base_url is already set in conf.d/10.yaml (line 3)",
		},
		{
			name:    "list set in two files",
			files:   map[string]string{"config.yaml": "include: [a.yaml]\nvirtual_models:\n  - source: fast\n    target: a/x\n", "a.yaml": "virtual_models:\n  - source: smart\n    target: a/y\n"},
			wantErr: "failed to parse a.yaml: line 2: virtual_models is already set in config.yaml (line 3)",
		},
		{
			// The merge key supplies api_key, which a key added from a second
			// file would silently replace.
			name:    "section with a merge key set in two files",
			files:   map[string]string{"config.yaml": "include: [a.yaml]\nproviders:\n  a:\n    <<: {api_key: first}\n    type: vllm\n", "a.yaml": "providers:\n  a:\n    api_key: second\n"},
			wantErr: "failed to parse a.yaml: line 3: providers.a is also set in config.yaml (line 4); a section with a YAML anchor or merge key (<<) can be set in only one config file",
		},
		{
			// A key added to an anchored section would reach every alias of it.
			name:    "anchored section set in two files",
			files:   map[string]string{"config.yaml": "include: [a.yaml]\nextensions:\n  base: &base\n    a: 1\n  copy: *base\n", "a.yaml": "extensions:\n  base:\n    b: 2\n"},
			wantErr: "failed to parse a.yaml: line 3: extensions.base is also set in config.yaml (line 3)",
		},
		{
			name:    "alias set in two files",
			files:   map[string]string{"config.yaml": "include: [a.yaml]\nextensions:\n  base: &base\n    a: 1\n  copy: *base\n", "a.yaml": "extensions:\n  copy:\n    b: 2\n"},
			wantErr: "failed to parse a.yaml: line 3: extensions.copy is already set in config.yaml (line 5)",
		},
		{
			name:    "section in one file and value in another",
			files:   map[string]string{"config.yaml": "include: [a.yaml]\nextensions:\n  demo:\n    a: 1\n", "a.yaml": "extensions:\n  demo: off\n"},
			wantErr: "failed to parse a.yaml: line 2: extensions.demo is already set in config.yaml (line 4)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)

			_, err := loadConfigFiles(t, tt.files)
			require.Error(t, err)
			assert.Contains(t, filepath.ToSlash(err.Error()), tt.wantErr)
		})
	}
}

// CONFIG_STRICT=false downgrades an unknown key to a warning in every file;
// two files setting one value stays fatal, as nothing says which one applies.
func TestLoad_ConfigStrictFalseAppliesToIncludedFiles(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv(envConfigStrict, "false")

	result, err := loadConfigFiles(t, map[string]string{
		"config.yaml": "include: [a.yaml]\nbogus: 1\n",
		"a.yaml":      "server:\n  port: \"9999\"\n  prot: \"1\"\n",
	})
	require.NoError(t, err)
	assert.Equal(t, "9999", result.Config.Server.Port)

	_, err = loadConfigFiles(t, map[string]string{
		"config.yaml": "include: [a.yaml]\nserver:\n  port: \"1\"\n",
		"a.yaml":      "server:\n  port: \"2\"\n",
	})
	require.ErrorContains(t, err, "server.port is already set in config.yaml")
}

func TestLoad_RejectsMasterKeyFromUnsetVariableInIncludedFile(t *testing.T) {
	clearAllConfigEnvVars(t)

	_, err := loadConfigFiles(t, map[string]string{
		"config.yaml": "include: [a.yaml]\nserver:\n  port: \"9999\"\n",
		"a.yaml":      "server:\n  master_key: \"${GOMODEL_TEST_INCLUDE_UNSET}\"\n",
	})
	require.ErrorContains(t, err, "server.master_key reads an environment variable that is not set")
}

func TestIncludedFilesKeepsListedOrderAndSortsMatches(t *testing.T) {
	withTempDir(t, func(string) {
		writeFiles(t, map[string]string{
			"config/last.yaml":      "",
			"config/conf.d/20.yaml": "",
			"config/conf.d/10.yaml": "",
			"config/conf.d/30.yaml": "",
		})

		files, err := includedFiles("config/config.yaml", []string{"last.yaml", "conf.d/30.yaml", "conf.d/*.yaml", "first.yaml"})
		require.NoError(t, err)
		for i := range files {
			files[i] = filepath.ToSlash(files[i])
		}
		assert.Equal(t, []string{"config/last.yaml", "config/conf.d/30.yaml", "config/conf.d/10.yaml", "config/conf.d/20.yaml", "config/first.yaml"}, files)
	})
}

// The export is one file: what the config file and the files it includes
// declare together, with no include key, and each ${NAME} as written.
func TestExportYAMLMergesIncludedFiles(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("GOMODEL_TEST_INCLUDE_URL", "http://a:8000/v1")

	loaded, err := loadConfigFiles(t, map[string]string{
		"config.yaml": "include: [a.yaml]\nserver:\n  port: \"9999\"\nproviders:\n  a:\n    type: vllm\n",
		"a.yaml":      "providers:\n  a:\n    base_url: \"${GOMODEL_TEST_INCLUDE_URL}\"\n",
	})
	require.NoError(t, err)
	require.NoError(t, loaded.ResolveSecrets(t.Context()))

	out, err := loaded.ExportYAML(ExportDocument{Config: loaded.Config, Providers: loaded.RawProviders}, os.Environ())
	require.NoError(t, err)

	var exported map[string]any
	require.NoError(t, yaml.Unmarshal(out, &exported))
	assert.NotContains(t, exported, "include")
	assert.Equal(t, "9999", exportedValue(t, string(out), "server", "port"))
	assert.Equal(t, "vllm", exportedValue(t, string(out), "providers", "a", "type"))
	assert.Contains(t, string(out), `base_url: "${GOMODEL_TEST_INCLUDE_URL}"`)
}

func TestIsConfigFileCoversIncludedFiles(t *testing.T) {
	withTempDir(t, func(string) {
		writeFiles(t, map[string]string{
			"config.yaml":   "include: [\"conf.d/*.yaml\"]\n",
			"conf.d/a.yaml": "",
			"other.yaml":    "",
		})

		for path, want := range map[string]bool{"config.yaml": true, "conf.d/a.yaml": true, "other.yaml": false} {
			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, want, IsConfigFile(info), path)
		}
	})
}
