package googlecommon

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestServiceAccountJSONDecodesURLSafeBase64(t *testing.T) {
	want := []byte{0xfb, 0xff, 0xfe}
	encoded := base64.RawURLEncoding.EncodeToString(want)

	got, err := serviceAccountJSON(Config{ServiceAccountJSONBase64: encoded})
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestServiceAccountJSONDecodesPaddedURLSafeBase64(t *testing.T) {
	want := []byte{0xfb, 0xff, 0xfe}
	encoded := base64.URLEncoding.EncodeToString(want)

	got, err := serviceAccountJSON(Config{ServiceAccountJSONBase64: encoded})
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestServiceAccountJSONReportsOriginalBase64DecodeError(t *testing.T) {
	_, err := serviceAccountJSON(Config{ServiceAccountJSONBase64: "not valid base64!"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "standard base64 decode failed")
}

func TestFindCredentialsAndHTTPClientAuthSelection(t *testing.T) {
	tests := []struct {
		name        string
		cfg         func(t *testing.T, tokenURL string) Config
		wantToken   string
		wantScope   string
		wantADCFile bool
	}{
		{
			name: "service account base64 uses service account path and default scope",
			cfg: func(t *testing.T, tokenURL string) Config {
				credentials := serviceAccountCredentials(t, tokenURL)
				encoded := base64.StdEncoding.EncodeToString([]byte(credentials))
				_, err := serviceAccountJSON(Config{ServiceAccountJSONBase64: encoded})
				require.NoError(t, err)

				return Config{ServiceAccountJSONBase64: encoded}
			},
			wantToken: "service-account-token",
			wantScope: DefaultScope,
		},
		{
			name: "empty service account config uses ADC path",
			cfg: func(t *testing.T, tokenURL string) Config {
				t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adcCredentialsFile(t, tokenURL))
				return Config{Scope: "https://www.googleapis.com/auth/custom"}
			},
			wantToken:   "adc-token",
			wantADCFile: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotForm url.Values
			tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				err := r.ParseForm()
				require.NoError(t, err)

				gotForm = r.PostForm
				token := tt.wantToken
				if gotForm.Get("grant_type") == "urn:ietf:params:oauth:grant-type:jwt-bearer" {
					token = "service-account-token"
				} else if gotForm.Get("grant_type") == "refresh_token" {
					token = "adc-token"
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": token,
					"token_type":   "Bearer",
					"expires_in":   3600,
				})
			}))
			defer tokenServer.Close()

			creds, err := FindCredentials(context.Background(), tt.cfg(t, tokenServer.URL))
			require.NoError(t, err)

			upstream, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})

			client := HTTPClient(upstream.Client(), creds.TokenSource, "")
			resp, err := client.Get(upstream.URL)
			require.NoError(t, err)
			_ = resp.Body.Close()

			assert.Equal(t, "Bearer "+tt.wantToken, capture.Last(t).Header.Get("Authorization"))
			if tt.wantScope != "" {
				assert.Equal(t, tt.wantScope, tokenRequestScope(t, gotForm))
			}
			if tt.wantADCFile {
				assert.Equal(t, "adc-refresh-token", gotForm.Get("refresh_token"))
			}
		})
	}
}

func adcCredentialsFile(t *testing.T, tokenURL string) string {
	t.Helper()
	return adcCredentialsFileWithQuotaProject(t, tokenURL, "")
}

func adcCredentialsFileWithQuotaProject(t *testing.T, tokenURL, quotaProject string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adc.json")
	contents := map[string]string{
		"type":          "authorized_user",
		"client_id":     "adc-client-id",
		"client_secret": "adc-client-secret",
		"refresh_token": "adc-refresh-token",
		"token_uri":     tokenURL,
	}
	if quotaProject != "" {
		contents["quota_project_id"] = quotaProject
	}
	encoded, err := json.Marshal(contents)
	require.NoError(t, err)
	err = os.WriteFile(path, encoded, 0o600)
	require.NoError(t, err)

	return path
}

// staticTokenServer answers every token request with accessToken.
func staticTokenServer(t *testing.T, accessToken string) *httptest.Server {
	t.Helper()
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"access_token":"`+accessToken+`","token_type":"Bearer","expires_in":3600}`)
	return server
}

func TestFindCredentialsReadsADCQuotaProject(t *testing.T) {
	tokenServer := staticTokenServer(t, "adc-token")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adcCredentialsFileWithQuotaProject(t, tokenServer.URL, "billing-target"))

	creds, err := FindCredentials(context.Background(), Config{})
	require.NoError(t, err)
	assert.Equal(t, "billing-target", creds.QuotaProjectID)
}

func TestFindCredentialsReadsServiceAccountProject(t *testing.T) {
	tokenServer := staticTokenServer(t, "sa-token")

	saJSON := serviceAccountCredentialsWithProject(t, tokenServer.URL, "sa-home-project")
	creds, err := FindCredentials(context.Background(), Config{ServiceAccountJSON: saJSON})
	require.NoError(t, err)
	assert.Equal(t, "sa-home-project", creds.QuotaProjectID)
}

func TestHTTPClientQuotaProjectHeader(t *testing.T) {
	tests := []struct {
		name         string
		quotaProject string
	}{
		{name: "set when configured", quotaProject: "billing-target"},
		{name: "omitted when empty", quotaProject: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})

			source := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token", TokenType: "Bearer"})
			client := HTTPClient(upstream.Client(), source, tt.quotaProject)
			resp, err := client.Get(upstream.URL)
			require.NoError(t, err)
			_ = resp.Body.Close()

			header := capture.Last(t).Header
			if tt.quotaProject == "" {
				assert.NotContains(t, header, QuotaProjectHeader)
				return
			}
			assert.Equal(t, tt.quotaProject, header.Get(QuotaProjectHeader))
		})
	}
}

func serviceAccountCredentialsWithProject(t *testing.T, tokenURL, projectID string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyBytes,
	})
	contents := map[string]string{
		"type":           "service_account",
		"client_email":   "service@example.com",
		"private_key_id": "test-key-id",
		"private_key":    string(keyPEM),
		"token_uri":      tokenURL,
	}
	if projectID != "" {
		contents["project_id"] = projectID
	}
	encoded, err := json.Marshal(contents)
	require.NoError(t, err)

	return string(encoded)
}

func serviceAccountCredentials(t *testing.T, tokenURL string) string {
	t.Helper()
	return serviceAccountCredentialsWithProject(t, tokenURL, "")
}

func tokenRequestScope(t *testing.T, form url.Values) string {
	t.Helper()
	if scope := form.Get("scope"); scope != "" {
		return scope
	}
	assertion := form.Get("assertion")
	parts := strings.Split(assertion, ".")
	require.GreaterOrEqual(t, len(parts), 2, "JWT assertion = %q, want header.payload.signature", assertion)

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)

	var claims struct {
		Scope string `json:"scope"`
	}
	err = json.Unmarshal(payload, &claims)
	require.NoError(t, err)

	return claims.Scope
}
