package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDashboardLogin(t *testing.T) {
	tests := []struct {
		name         string
		yaml         string
		env          map[string]string
		wantUsername string
		wantPassword string
		wantEnabled  bool
	}{
		{name: "off by default", wantUsername: "admin"},
		{name: "a password alone turns it on", env: map[string]string{"DASHBOARD_PASSWORD": "s3cret"}, wantUsername: "admin", wantPassword: "s3cret", wantEnabled: true},
		{name: "a username alone does not", env: map[string]string{"DASHBOARD_USERNAME": "operator"}, wantUsername: "operator"},
		{name: "from yaml", yaml: "admin:\n  username: operator\n  password: from-yaml\n", wantUsername: "operator", wantPassword: "from-yaml", wantEnabled: true},
		{
			name: "environment overrides yaml", yaml: "admin:\n  username: operator\n  password: from-yaml\n",
			env:          map[string]string{"DASHBOARD_USERNAME": "root", "DASHBOARD_PASSWORD": "from-env"},
			wantUsername: "root", wantPassword: "from-env", wantEnabled: true,
		},
		{name: "a blank username falls back to the default", yaml: "admin:\n  username: \"  \"\n  password: s3cret\n", wantUsername: "admin", wantPassword: "s3cret", wantEnabled: true},
		{name: "the username is trimmed, the password is not", yaml: "admin:\n  username: \" operator \"\n  password: \" s3cret \"\n", wantUsername: "operator", wantPassword: " s3cret ", wantEnabled: true},
		{name: "needs the admin API", env: map[string]string{"DASHBOARD_PASSWORD": "s3cret", "ADMIN_ENDPOINTS_ENABLED": "false"}, wantUsername: "admin", wantPassword: "s3cret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			withTempDir(t, func(dir string) {
				if tt.yaml != "" {
					writeConfigYAML(t, dir, tt.yaml)
				}
				result, err := Load()
				require.NoError(t, err)
				admin := result.Config.Admin
				assert.Equal(t, tt.wantUsername, admin.Username)
				assert.Equal(t, tt.wantPassword, admin.Password)
				assert.Equal(t, tt.wantEnabled, admin.LoginEnabled())
			})
		})
	}
}

// An unset ${NAME} stays in config.yaml verbatim; as the dashboard password it
// would be one anybody who reads the file knows.
func TestLoadRejectsDashboardPasswordFromUnsetVariable(t *testing.T) {
	const unset = "admin.password reads an environment variable that is not set"
	tests := []struct {
		name         string
		password     string
		env          map[string]string
		wantPassword string
		wantErr      string
	}{
		{name: "unset variable", password: "${GOMODEL_TEST_DASHBOARD_PASSWORD}", wantErr: unset},
		{name: "unset inside a value", password: "pw-${GOMODEL_TEST_DASHBOARD_PASSWORD}", wantErr: unset},
		{name: "set variable", password: "${GOMODEL_TEST_DASHBOARD_PASSWORD}", env: map[string]string{"GOMODEL_TEST_DASHBOARD_PASSWORD": "s3cret"}, wantPassword: "s3cret"},
		{name: "empty default", password: "${GOMODEL_TEST_DASHBOARD_PASSWORD:-}", wantPassword: ""},
		{name: "environment override", password: "${GOMODEL_TEST_DASHBOARD_PASSWORD}", env: map[string]string{"DASHBOARD_PASSWORD": "from-env"}, wantPassword: "from-env"},
		{name: "set variable holding ${", password: "${GOMODEL_TEST_DASHBOARD_PASSWORD}", env: map[string]string{"GOMODEL_TEST_DASHBOARD_PASSWORD": "pa${ss}"}, wantPassword: "pa${ss}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			t.Setenv("GOMODEL_TEST_DASHBOARD_PASSWORD", "")
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			withTempDir(t, func(dir string) {
				writeConfigYAML(t, dir, "admin:\n  password: \""+tt.password+"\"\n")
				result, err := Load()
				if tt.wantErr != "" {
					require.ErrorContains(t, err, tt.wantErr)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, tt.wantPassword, result.Config.Admin.Password)
			})
		})
	}
}
