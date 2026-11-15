package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostAuthRecorder answers every Gemini request with a generateContent reply
// and records where it went and how it was authenticated, so a test can name
// a Google host without reaching it. Token exchanges with tokenHost go
// through.
type hostAuthRecorder struct {
	tokenHost string
	hosts     []string
	auth      []string
}

func (r *hostAuthRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == r.tokenHost {
		return http.DefaultTransport.RoundTrip(req)
	}
	r.hosts = append(r.hosts, req.URL.Host)
	r.auth = append(r.auth, req.Header.Get("Authorization")+req.Header.Get("x-goog-api-key"))
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)),
		Request: req,
	}, nil
}

// TestNew_HostCredentialDestination pins where the host's Google credentials
// may go when a Gemini row infers its Vertex backend from vertex_project. A
// provider declared in config.yaml or the environment keeps sending them
// wherever the operator points it; one saved through the admin API sends them
// to Google's own Vertex AI endpoints only.
func TestNew_HostCredentialDestination(t *testing.T) {
	const nativePath = "/v1/projects/p/locations/us-central1/publishers/google"
	const custom = "https://vertex-proxy.example.com" + nativePath
	tests := []struct {
		name     string
		managed  bool
		baseURL  string
		project  string
		wantHost string // empty: the provider refuses to build
		want     string
	}{
		{name: "declared, inferred Vertex, custom endpoint", baseURL: custom, project: "p", wantHost: "vertex-proxy.example.com", want: "Bearer adc-token"},
		{name: "managed, inferred Vertex, custom endpoint", managed: true, baseURL: custom, project: "p"},
		{name: "managed, inferred Vertex, Google lookalike host", managed: true, baseURL: "https://aiplatform.googleapis.com.example.com" + nativePath, project: "p"},
		{name: "managed, inferred Vertex, default endpoint", managed: true, project: "p", wantHost: "aiplatform.googleapis.com", want: "Bearer adc-token"},
		{name: "managed, inferred Vertex, Vertex endpoint", managed: true, baseURL: "https://us-central1-aiplatform.googleapis.com" + nativePath, project: "p", wantHost: "us-central1-aiplatform.googleapis.com", want: "Bearer adc-token"},
		{name: "managed, AI Studio key, custom endpoint", managed: true, baseURL: "https://gemini-proxy.example.com/v1beta", wantHost: "gemini-proxy.example.com", want: "AIza-own"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("USE_GOOGLE_GEMINI_NATIVE_API", "")
			tokenServer, tokenCapture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "adc-token", "token_type": "Bearer", "expires_in": 3600})
			})
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adcFile(t, tokenServer.URL))

			parsed, err := url.Parse(tokenServer.URL)
			require.NoError(t, err)
			recorder := &hostAuthRecorder{tokenHost: parsed.Host}
			cfg := providers.ProviderConfig{
				Type:          "gemini",
				Managed:       tt.managed,
				APIKey:        "AIza-own",
				APIMode:       "native",
				BaseURL:       tt.baseURL,
				VertexProject: tt.project,
			}
			if tt.project != "" {
				cfg.VertexLocation = "us-central1"
			}
			opts := providers.ProviderOptions{HTTPClient: &http.Client{Transport: recorder}, Keys: providers.NewKeyring("AIza-own")}
			_, err = New(cfg, opts).ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "gemini-2.5-flash",
				Messages: []core.Message{{Role: "user", Content: "hi"}},
			})

			if tt.wantHost == "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "only sent to Google's Vertex AI endpoints")
				assert.Empty(t, recorder.hosts, "a refused provider must not send requests")
				assert.Zero(t, tokenCapture.Count(), "a refused provider must not exchange the host's credentials")
				return
			}
			require.NoError(t, err)
			require.Len(t, recorder.auth, 1)
			assert.Equal(t, tt.wantHost, recorder.hosts[0])
			assert.Equal(t, tt.want, recorder.auth[0])
		})
	}
}

func adcFile(t *testing.T, tokenURL string) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]string{
		"type":          "authorized_user",
		"client_id":     "adc-client-id",
		"client_secret": "adc-client-secret",
		"refresh_token": "adc-refresh-token",
		"token_uri":     tokenURL,
	})
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "adc.json")
	require.NoError(t, os.WriteFile(path, encoded, 0o600))
	return path
}
