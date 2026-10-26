package awscfg

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// refusingTransport stands in for a provider's proxy-aware transport and
// fails every request sent through it.
type refusingTransport struct{ calls atomic.Int32 }

func (rt *refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	rt.calls.Add(1)
	return nil, errors.New("request sent through the provider transport")
}

// isolateAWSEnv leaves IMDS as the only credential source, pointed at endpoint.
func isolateAWSEnv(t *testing.T, endpoint string) {
	t.Helper()
	dir := t.TempDir()
	for key, value := range map[string]string{
		"AWS_ACCESS_KEY_ID":                      "",
		"AWS_SECRET_ACCESS_KEY":                  "",
		"AWS_SESSION_TOKEN":                      "",
		"AWS_PROFILE":                            "",
		"AWS_DEFAULT_PROFILE":                    "",
		"AWS_WEB_IDENTITY_TOKEN_FILE":            "",
		"AWS_ROLE_ARN":                           "",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI":     "",
		"AWS_EC2_METADATA_DISABLED":              "",
		"AWS_CONFIG_FILE":                        filepath.Join(dir, "config"),
		"AWS_SHARED_CREDENTIALS_FILE":            filepath.Join(dir, "credentials"),
		"AWS_EC2_METADATA_SERVICE_ENDPOINT":      endpoint,
		"AWS_REGION":                             "us-east-1",
	} {
		t.Setenv(key, value)
	}
}

func TestLoadConfigKeepsSDKClientForIMDS(t *testing.T) {
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	imds, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/latest/api/token": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Aws-Ec2-Metadata-Token-Ttl-Seconds", "21600")
			_, _ = io.WriteString(w, "imds-token")
		},
		"/latest/meta-data/iam/security-credentials/": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "gateway-role")
		},
		"/latest/meta-data/iam/security-credentials/gateway-role": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"Code":"Success","Type":"AWS-HMAC","AccessKeyId":"AKIDIMDS","SecretAccessKey":"secret","Token":"session","Expiration":"`+expiry+`"}`)
		},
	})
	isolateAWSEnv(t, imds.URL)

	transport := &refusingTransport{}
	cfg, err := LoadConfig(context.Background(), &http.Client{Transport: transport})
	require.NoError(t, err)

	creds, err := cfg.Credentials.Retrieve(context.Background())
	require.NoError(t, err, "IMDS credentials should resolve through the SDK's own client")
	assert.Equal(t, "AKIDIMDS", creds.AccessKeyID)
	assert.Positive(t, capture.Count(), "the fake IMDS was not contacted")
	assert.Zero(t, transport.calls.Load(), "IMDS requests went through the provider transport")

	// AWS API calls still use the provider's client.
	apiClient, ok := cfg.HTTPClient.(*http.Client)
	require.True(t, ok, "HTTPClient is %T", cfg.HTTPClient)
	assert.Same(t, transport, apiClient.Transport)
}
