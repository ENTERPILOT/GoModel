package run

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const migrateTestConfig = `
model_list:
  - model_name: gpt-4o
    litellm_params:
      model: openai/gpt-4o
      api_key: sk-inline-secret
general_settings:
  master_key: sk-1234
`

func writeLiteLLMConfig(t *testing.T) string {
	t.Helper()
	// A developer's DATABASE_URL must not make the tests read a database.
	t.Setenv("DATABASE_URL", "")
	path := filepath.Join(t.TempDir(), "litellm.yaml")
	require.NoError(t, os.WriteFile(path, []byte(migrateTestConfig), 0o600))
	return path
}

func TestParseCLI_MigrateSubcommand(t *testing.T) {
	opts, err := parseCLI("gomodel", []string{"migrate", "litellm", "x.yaml"}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, []string{"litellm", "x.yaml"}, opts.MigrateArgs)
	assert.Nil(t, opts.PluginArgs)
}

func TestRunMigrateCommand_Usage(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{name: "missing source", args: nil, wantCode: 2, wantErr: "missing source"},
		{name: "unknown source", args: []string{"portkey"}, wantCode: 2, wantErr: `unknown source "portkey"`},
		{name: "missing file", args: []string{"litellm"}, wantCode: 2, wantErr: "missing LiteLLM config file argument"},
		{name: "extra args", args: []string{"litellm", "a.yaml", "b.yaml"}, wantCode: 2, wantErr: "unexpected arguments"},
		{name: "skip and import", args: []string{"litellm", "--skip-database", "--gomodel-url", "http://gomodel", "a.yaml"}, wantCode: 2, wantErr: "--skip-database cannot be combined"},
		{name: "help", args: []string{"help"}, wantCode: 0},
		{name: "litellm help", args: []string{"litellm", "-h"}, wantCode: 0},
		{name: "file not found", args: []string{"litellm", filepath.Join(t.TempDir(), "nope.yaml")}, wantCode: 1, wantErr: "nope.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runMigrateCommand("gomodel", tt.args, &stdout, &stderr)
			assert.Equal(t, tt.wantCode, ExitCode(err))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Contains(t, stderr.String(), tt.wantErr, "errors are printed, not only returned")
			}
		})
	}
}

func TestRunMigrateCommand_DryRunWritesNothing(t *testing.T) {
	path := writeLiteLLMConfig(t)
	var stdout bytes.Buffer
	require.NoError(t, runMigrateCommand("gomodel", []string{"litellm", path}, &stdout, io.Discard))

	out := stdout.String()
	assert.Contains(t, out, "# LiteLLM to GoModel migration report")
	assert.Contains(t, out, "virtual_models:")
	assert.Contains(t, out, "No files were written.")
	assert.Contains(t, out, "`database`: not read: keys, teams, users, and budgets were not migrated. Pass --database-url to import them")
	assert.NotContains(t, out, "Nothing was imported into GoModel", "no import was planned")
	assert.NotContains(t, out, "sk-inline-secret", "the dry run never prints secrets")
	assert.NotContains(t, out, "sk-1234")
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestRunMigrateCommand_WritesFiles(t *testing.T) {
	path := writeLiteLLMConfig(t)
	out := filepath.Join(t.TempDir(), "gomodel")
	args := []string{"litellm", "--out", out, path}
	require.NoError(t, runMigrateCommand("gomodel", args, io.Discard, io.Discard))

	config, err := os.ReadFile(filepath.Join(out, "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(config), "api_key: ${OPENAI_API_KEY}")
	assert.NotContains(t, string(config), "sk-inline-secret")

	env, err := os.ReadFile(filepath.Join(out, ".env"))
	require.NoError(t, err)
	assert.Contains(t, string(env), "OPENAI_API_KEY=sk-inline-secret")
	assert.Contains(t, string(env), "GOMODEL_MASTER_KEY=sk-1234")
	info, err := os.Stat(filepath.Join(out, ".env"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.FileExists(t, filepath.Join(out, "MIGRATION_REPORT.md"))

	err = runMigrateCommand("gomodel", args, io.Discard, io.Discard)
	require.ErrorContains(t, err, "already exists; pass --force to overwrite")

	require.NoError(t, os.Chmod(filepath.Join(out, ".env"), 0o644))
	require.NoError(t, runMigrateCommand("gomodel", append([]string{"litellm", "--force"}, args[1:]...), io.Discard, io.Discard))
	info, err = os.Stat(filepath.Join(out, ".env"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "--force restores the secret file's mode")
}

func TestRunMigrateCommand_DoesNotFollowSymlinks(t *testing.T) {
	path := writeLiteLLMConfig(t)
	out := t.TempDir()
	outside := filepath.Join(t.TempDir(), "stolen.env")
	require.NoError(t, os.Symlink(outside, filepath.Join(out, ".env")))
	args := []string{"litellm", "--out", out, path}

	err := runMigrateCommand("gomodel", args, io.Discard, io.Discard)
	require.ErrorContains(t, err, "already exists", "a dangling symlink counts as an existing file")

	require.NoError(t, runMigrateCommand("gomodel", append([]string{"litellm", "--force"}, args[1:]...), io.Discard, io.Discard))
	assert.NoFileExists(t, outside, "the secret file is not written through the link")
	info, err := os.Lstat(filepath.Join(out, ".env"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestRunMigrateCommand_DatabaseErrors(t *testing.T) {
	path := writeLiteLLMConfig(t)
	tests := []struct {
		name string
		args []string
		env  string
		want string
	}{
		{name: "import without a database", args: []string{"--gomodel-url", "http://127.0.0.1:1"}, want: "--gomodel-url imports from the LiteLLM database; pass --database-url"},
		{name: "unreachable database flag", args: []string{"--database-url", "postgres://user@127.0.0.1:1/litellm?connect_timeout=1"}, want: "pass --skip-database to convert the config only"},
		{name: "unreachable DATABASE_URL", env: "postgres://user@127.0.0.1:1/litellm?connect_timeout=1", want: "connect to the LiteLLM database"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", tt.env)
			args := append(append([]string{"litellm"}, tt.args...), path)
			err := runMigrateCommand("gomodel", args, io.Discard, io.Discard)
			require.ErrorContains(t, err, tt.want)
			assert.Equal(t, 1, ExitCode(err))
		})
	}

	t.Setenv("DATABASE_URL", "postgres://user@127.0.0.1:1/litellm?connect_timeout=1")
	require.NoError(t, runMigrateCommand("gomodel", []string{"litellm", "--skip-database", path}, io.Discard, io.Discard),
		"--skip-database ignores DATABASE_URL")
}

func TestCheckGoModelURL(t *testing.T) {
	tests := []struct {
		url       string
		allowHTTP bool
		wantErr   string
	}{
		{url: "https://gomodel.example.com"},
		{url: "http://localhost:8080"},
		{url: "http://127.0.0.1:8080"},
		{url: "http://[::1]:8080"},
		{url: "http://gomodel:8080", wantErr: "would send the admin key unencrypted"},
		{url: "http://gomodel:8080", allowHTTP: true},
		{url: "ftp://gomodel", wantErr: "must use http or https"},
		{url: "localhost:8080", wantErr: "is not a URL"},
	}
	for _, tt := range tests {
		err := checkGoModelURL(tt.url, tt.allowHTTP)
		if tt.wantErr == "" {
			require.NoError(t, err, tt.url)
			continue
		}
		require.ErrorContains(t, err, tt.wantErr, tt.url)
	}

	err := runMigrateCommand("gomodel", []string{"litellm", "--gomodel-url", "http://gomodel:8080", "a.yaml"}, io.Discard, io.Discard)
	assert.Equal(t, 2, ExitCode(err), "a remote http URL is a usage error")
}
