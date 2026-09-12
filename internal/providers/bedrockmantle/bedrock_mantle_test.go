package bedrockmantle

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
