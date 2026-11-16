package bedrockmantle

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestResponsesUsesOpenAIPathForGPT56(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"id":"resp_1","object":"response","model":"openai.gpt-5.6-sol","status":"completed","output":[]}`)

	p := testProvider(t, server, modeAuto, providers.NewKeyring("secret", "second-secret"), nil)
	var req core.ResponsesRequest
	err := json.Unmarshal([]byte(`{
		"model":"openai.gpt-5.6-sol",
		"input":"hello",
		"previous_response_id":"resp_previous",
		"custom_bedrock_option":true
	}`), &req)
	require.NoError(t, err)

	resp, err := p.Responses(context.Background(), &req)
	require.NoError(t, err)
	assert.Equal(t, "resp_1", resp.ID)

	sent := capture.Last(t)
	assert.Equal(t, "/openai/v1/responses", sent.Path)
	assert.Equal(t, "Bearer secret", sent.Header.Get("Authorization"))
	body := sent.JSON(t)
	assert.Equal(t, "resp_previous", body["previous_response_id"])
	custom, _ := body["custom_bedrock_option"].(bool)
	assert.True(t, custom, "request body did not preserve Responses fields: %#v", body)
}

func TestMantleEndpointRouting(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		call     func(context.Context, *Provider) error
		wantPath string
	}{
		{
			name: "standard responses model",
			mode: modeAuto,
			call: func(ctx context.Context, p *Provider) error {
				_, err := p.Responses(ctx, &core.ResponsesRequest{Model: "openai.gpt-oss-120b", Input: "hello"})
				return err
			},
			wantPath: "/v1/responses",
		},
		{
			name: "force standard",
			mode: modeStandard,
			call: func(ctx context.Context, p *Provider) error {
				_, err := p.Responses(ctx, &core.ResponsesRequest{Model: "openai.gpt-5.6-terra", Input: "hello"})
				return err
			},
			wantPath: "/v1/responses",
		},
		{
			name: "force openai chat",
			mode: modeOpenAI,
			call: func(ctx context.Context, p *Provider) error {
				_, err := p.ChatCompletion(ctx, &core.ChatRequest{Model: "custom.model"})
				return err
			},
			wantPath: "/openai/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/chat/completions") {
					_, _ = io.WriteString(w, `{"id":"chat_1","object":"chat.completion","choices":[]}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","output":[]}`)
			})

			p := testProvider(t, server, tt.mode, providers.NewKeyring("secret"), nil)
			require.NoError(t, tt.call(context.Background(), p))
			assert.Equal(t, tt.wantPath, capture.Last(t).Path)
		})
	}
}

func TestListModelsAlwaysUsesCatalogPath(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"data":[{"id":"openai.gpt-5.6-luna"}]}`)

	p := testProvider(t, server, modeOpenAI, providers.NewKeyring("secret"), nil)
	models, err := p.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/v1/models", capture.Last(t).Path)
	assert.Equal(t, "list", models.Object)
	require.Len(t, models.Data, 1)
	assert.Equal(t, "model", models.Data[0].Object, "models were not normalized")
}

func TestStreamResponsesUsesOpenAIPath(t *testing.T) {
	server, capture := providertest.SSEServer(t, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")

	p := testProvider(t, server, modeAuto, providers.NewKeyring("secret"), nil)
	stream, err := p.StreamResponses(context.Background(), &core.ResponsesRequest{Model: "openai.gpt-5.6-luna", Input: "hello"})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, "/openai/v1/responses", capture.Last(t).Path)
	assert.Contains(t, string(body), "[DONE]", "stream must end with the terminal marker")
}

func TestSigV4AuthenticationUsesBedrockService(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"data":[]}`)

	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{
			AccessKeyID:     "AKID",
			SecretAccessKey: "secret",
			SessionToken:    "session-token",
		}, nil
	})
	p := testProvider(t, server, modeAuto, nil, provider)
	_, err := p.ListModels(context.Background())
	require.NoError(t, err)

	sent := capture.Last(t)
	authorization := sent.Header.Get("Authorization")
	require.True(t, strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 "), "Authorization = %q, want SigV4", authorization)
	assert.Contains(t, authorization, "Credential=AKID/")
	assert.Contains(t, authorization, "/us-east-1/bedrock/aws4_request")
	assert.Equal(t, "session-token", sent.Header.Get("X-Amz-Security-Token"))
}

func TestBearerAuthenticationRotatesKeys(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"data":[]}`)

	p := testProvider(t, server, modeAuto, providers.NewKeyring("first", "second"), nil)
	for range 2 {
		_, err := p.ListModels(context.Background())
		require.NoError(t, err)
	}
	requests := capture.All()
	require.Len(t, requests, 2)
	assert.Equal(t, "Bearer first", requests[0].Header.Get("Authorization"))
	assert.Equal(t, "Bearer second", requests[1].Header.Get("Authorization"))
}

func TestProviderDoesNotAdvertiseUnsupportedOpenAISurfaces(t *testing.T) {
	p := &Provider{}
	_, ok := any(p).(core.NativeResponseLifecycleProvider)
	assert.False(t, ok)
	_, ok = any(p).(core.NativeBatchProvider)
	assert.False(t, ok)
	_, ok = any(p).(core.NativeFileProvider)
	assert.False(t, ok)
}

func testProvider(t *testing.T, server *httptest.Server, mode string, keys *providers.Keyring, credentialsProvider aws.CredentialsProvider) *Provider {
	t.Helper()
	client := authenticatedClient(server.Client(), keys, credentialsProvider, defaultRegion)
	return newProvider(endpointConfig{baseURL: server.URL, region: defaultRegion, mode: mode}, providers.ProviderConfig{}, providers.ProviderOptions{}, client)
}

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The factory hands a proxy-aware client down through ProviderOptions; the
// provider must wrap that client with its auth rather than build its own.
func TestNew_UsesTheFactoryTransport(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"data":[{"id":"openai.gpt-5.6-luna"}]}`)
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return server.Client().Transport.RoundTrip(r)
	})}

	p, ok := New(providers.ProviderConfig{APIKey: "secret", BaseURL: server.URL, APIMode: modeOpenAI}, providers.ProviderOptions{HTTPClient: client}).(*Provider)
	require.True(t, ok)
	require.NoError(t, p.ready())

	_, err := p.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load(), "request must travel through the factory-supplied client")
	assert.Equal(t, "Bearer secret", capture.Last(t).Header.Get("Authorization"))
}

// TestNew_HostCredentialDestination pins where the host's AWS credentials may
// go. A provider declared in config.yaml or the environment keeps sending
// them wherever the operator points it. One saved through the admin API sends
// them to AWS's own Bedrock endpoints only: against any other base URL it
// needs an API key of its own.
func TestNew_HostCredentialDestination(t *testing.T) {
	const custom = "https://mantle-proxy.example.com"
	const sigV4 = "AWS4-HMAC-SHA256"
	tests := []struct {
		name     string
		managed  bool
		baseURL  string
		ownKey   string
		envToken string
		wantHost string // empty: the provider refuses every request
		want     string
	}{
		{name: "declared, env token, custom endpoint", baseURL: custom, envToken: "env-token", wantHost: "mantle-proxy.example.com", want: "Bearer env-token"},
		{name: "declared, SigV4, custom endpoint", baseURL: custom, wantHost: "mantle-proxy.example.com", want: sigV4},
		{name: "declared, env token, http endpoint", baseURL: "http://mantle-proxy.internal:8080", envToken: "env-token", wantHost: "mantle-proxy.internal:8080", want: "Bearer env-token"},
		{name: "managed, env token, custom endpoint", managed: true, baseURL: custom, envToken: "env-token"},
		{name: "managed, SigV4, custom endpoint", managed: true, baseURL: custom},
		{name: "managed, own key, custom endpoint", managed: true, baseURL: custom, ownKey: "own-key", envToken: "env-token", wantHost: "mantle-proxy.example.com", want: "Bearer own-key"},
		{name: "managed, env token, AWS lookalike host", managed: true, baseURL: "https://bedrock-mantle.us-east-1.api.aws.example.com", envToken: "env-token"},
		{name: "managed, env token, AWS endpoint on another port", managed: true, baseURL: "https://bedrock-mantle.us-east-1.api.aws:8443", envToken: "env-token"},
		{name: "managed, SigV4, AWS endpoint over http", managed: true, baseURL: "http://bedrock-mantle.us-east-1.api.aws"},
		{name: "managed, env token, default endpoint", managed: true, envToken: "env-token", wantHost: "bedrock-mantle.us-east-1.api.aws", want: "Bearer env-token"},
		{name: "managed, SigV4, region", managed: true, baseURL: "eu-west-1", wantHost: "bedrock-mantle.eu-west-1.api.aws", want: sigV4},
		{name: "managed, env token, AWS endpoint", managed: true, baseURL: "https://bedrock-mantle.eu-west-1.api.aws/v1", envToken: "env-token", wantHost: "bedrock-mantle.eu-west-1.api.aws", want: "Bearer env-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AWS_BEARER_TOKEN_BEDROCK", tt.envToken)
			t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
			t.Setenv("AWS_SESSION_TOKEN", "")
			t.Setenv("AWS_PROFILE", "")
			t.Setenv("BEDROCK_MANTLE_REGION", "")
			t.Setenv("AWS_REGION", "us-east-1")
			t.Setenv("AWS_CONFIG_FILE", "/dev/null")
			t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")

			// The recorder answers in place of the endpoint, so a test can name
			// an AWS host without reaching it.
			var hosts, auth []string
			client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				hosts = append(hosts, r.URL.Host)
				auth = append(auth, r.Header.Get("Authorization"))
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
					Request:    r,
				}, nil
			})}
			p := New(providers.ProviderConfig{Managed: tt.managed, APIKey: tt.ownKey, BaseURL: tt.baseURL}, providers.ProviderOptions{HTTPClient: client})
			_, err := p.ListModels(context.Background())

			if tt.wantHost == "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "only sent to AWS's Bedrock endpoints")
				assert.Empty(t, hosts, "a refused provider must not send requests")
				return
			}
			require.NoError(t, err)
			require.Len(t, auth, 1)
			assert.Equal(t, tt.wantHost, hosts[0])
			assert.True(t, strings.HasPrefix(auth[0], tt.want), "Authorization = %q", auth[0])
		})
	}
}
