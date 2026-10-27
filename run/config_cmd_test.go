package run

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/enterpilot/gomodel/config"
)

func TestParseCLI_ConfigSubcommand(t *testing.T) {
	opts, err := parseCLI("gomodel", []string{"config", "export"}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, []string{"export"}, opts.ConfigArgs)
	assert.Nil(t, opts.SecretsArgs)
}

func TestParseConfigArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    bool
		wantErr string
	}{
		{name: "export", args: []string{"export"}, want: true},
		{name: "help", args: []string{"help"}},
		{name: "export help", args: []string{"export", "-h"}},
		{name: "missing action", args: nil, wantErr: "missing action"},
		{name: "unknown action", args: []string{"import"}, wantErr: `unknown action "import"`},
		{name: "extra args", args: []string{"export", "now"}, wantErr: "unexpected arguments"},
		{name: "unknown flag", args: []string{"export", "--force"}, wantErr: "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseConfigArgs("gomodel", tt.args, io.Discard, io.Discard)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRunConfigExport(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "export.db"))
	t.Setenv("GOMODEL_MASTER_KEY", "cli-master-do-not-leak")

	var stdout, stderr bytes.Buffer
	setupConfigCalls := 0
	err := Run(context.Background(), Options{
		Args:   []string{"config", "export"},
		Stdout: &stdout,
		Stderr: &stderr,
		SetupConfig: func(context.Context, *config.LoadResult) error {
			setupConfigCalls++
			return nil
		},
	})
	require.NoError(t, err, stderr.String())
	assert.Equal(t, 1, setupConfigCalls, "the distribution can register its secret schemes for the command")

	var doc struct {
		Server struct {
			MasterKey string `yaml:"master_key"`
		} `yaml:"server"`
	}
	require.NoError(t, yaml.Unmarshal(stdout.Bytes(), &doc), "stdout holds the document alone")
	assert.Equal(t, "${env:GOMODEL_MASTER_KEY}", doc.Server.MasterKey)
	assert.NotContains(t, stdout.String()+stderr.String(), "cli-master-do-not-leak")
}
