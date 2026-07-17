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

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

func TestResponsesUsesOpenAIPathForGPT56(t *testing.T) {
	var path, authorization string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		authorization = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","model":"openai.gpt-5.6-sol","status":"completed","output":[]}`)
	}))
	defer server.Close()

	p := testProvider(t, server, modeAuto, providers.NewKeyring("secret", "second-secret"), nil)
	var req core.ResponsesRequest
	if err := json.Unmarshal([]byte(`{
		"model":"openai.gpt-5.6-sol",
		"input":"hello",
		"previous_response_id":"resp_previous",
		"custom_bedrock_option":true
	}`), &req); err != nil {
		t.Fatal(err)
	}

	resp, err := p.Responses(context.Background(), &req)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}
	if resp.ID != "resp_1" {
		t.Errorf("response ID = %q, want resp_1", resp.ID)
	}
	if path != "/openai/v1/responses" {
		t.Errorf("path = %q, want /openai/v1/responses", path)
	}
	if authorization != "Bearer secret" {
		t.Errorf("Authorization = %q, want first bearer token", authorization)
	}
	if body["previous_response_id"] != "resp_previous" || body["custom_bedrock_option"] != true {
		t.Errorf("request body did not preserve Responses fields: %#v", body)
	}
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
			var path string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(path, "/chat/completions"):
					_, _ = io.WriteString(w, `{"id":"chat_1","object":"chat.completion","choices":[]}`)
				default:
					_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","output":[]}`)
				}
			}))
			defer server.Close()

			p := testProvider(t, server, tt.mode, providers.NewKeyring("secret"), nil)
			if err := tt.call(context.Background(), p); err != nil {
				t.Fatalf("request error = %v", err)
			}
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
		})
	}
}

func TestListModelsAlwaysUsesCatalogPath(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = io.WriteString(w, `{"data":[{"id":"openai.gpt-5.6-luna"}]}`)
	}))
	defer server.Close()

	p := testProvider(t, server, modeOpenAI, providers.NewKeyring("secret"), nil)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if path != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", path)
	}
	if models.Object != "list" || len(models.Data) != 1 || models.Data[0].Object != "model" {
		t.Errorf("models were not normalized: %#v", models)
	}
}

func TestStreamResponsesUsesOpenAIPath(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer server.Close()

	p := testProvider(t, server, modeAuto, providers.NewKeyring("secret"), nil)
	stream, err := p.StreamResponses(context.Background(), &core.ResponsesRequest{Model: "openai.gpt-5.6-luna", Input: "hello"})
	if err != nil {
		t.Fatalf("StreamResponses() error = %v", err)
	}
	defer func() { _ = stream.Close() }()
	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/openai/v1/responses" {
		t.Errorf("path = %q, want /openai/v1/responses", path)
	}
	if !strings.Contains(string(body), "[DONE]") {
		t.Errorf("stream = %q, want terminal [DONE]", body)
	}
}

func TestSigV4AuthenticationUsesBedrockService(t *testing.T) {
	var authorization, securityToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		securityToken = r.Header.Get("X-Amz-Security-Token")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()

	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{
			AccessKeyID:     "AKID",
			SecretAccessKey: "secret",
			SessionToken:    "session-token",
		}, nil
	})
	p := testProvider(t, server, modeAuto, nil, provider)
	if _, err := p.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("Authorization = %q, want SigV4", authorization)
	}
	if !strings.Contains(authorization, "Credential=AKID/") || !strings.Contains(authorization, "/us-east-1/bedrock/aws4_request") {
		t.Errorf("Authorization has wrong credential scope: %q", authorization)
	}
	if securityToken != "session-token" {
		t.Errorf("X-Amz-Security-Token = %q", securityToken)
	}
}

func TestBearerAuthenticationRotatesKeys(t *testing.T) {
	var authorizations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()

	p := testProvider(t, server, modeAuto, providers.NewKeyring("first", "second"), nil)
	for range 2 {
		if _, err := p.ListModels(context.Background()); err != nil {
			t.Fatalf("ListModels() error = %v", err)
		}
	}
	if len(authorizations) != 2 || authorizations[0] != "Bearer first" || authorizations[1] != "Bearer second" {
		t.Errorf("Authorization headers = %v", authorizations)
	}
}

func TestProviderDoesNotAdvertiseUnsupportedOpenAISurfaces(t *testing.T) {
	p := &Provider{}
	if _, ok := any(p).(core.NativeResponseLifecycleProvider); ok {
		t.Error("Bedrock Mantle unexpectedly implements response lifecycle APIs")
	}
	if _, ok := any(p).(core.NativeBatchProvider); ok {
		t.Error("Bedrock Mantle unexpectedly implements batch APIs")
	}
	if _, ok := any(p).(core.NativeFileProvider); ok {
		t.Error("Bedrock Mantle unexpectedly implements file APIs")
	}
}

func testProvider(t *testing.T, server *httptest.Server, mode string, keys *providers.Keyring, credentialsProvider aws.CredentialsProvider) *Provider {
	t.Helper()
	client := authenticatedClient(server.Client(), keys, credentialsProvider, defaultRegion)
	return newProvider(endpointConfig{baseURL: server.URL, region: defaultRegion, mode: mode}, providers.ProviderConfig{}, providers.ProviderOptions{}, client)
}
