//go:build e2e

// Package e2e provides end-to-end tests for the LLM gateway.
package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"gomodel/internal/core"
	"gomodel/internal/providers"
	"gomodel/internal/server"
)

var (
	gatewayURL  string
	mockLLMURL  string
	testServer  *server.Server
	mockServer  *MockLLMServer
	testContext context.Context
	cancelFunc  context.CancelFunc
	serverDone  chan error
)

// TestMain sets up and tears down the test environment.
func TestMain(m *testing.M) {
	testContext, cancelFunc = context.WithCancel(context.Background())
	defer cancelFunc()

	// 1. Start mock LLM provider
	mockServer = NewMockLLMServer()
	mockLLMURL = mockServer.URL()

	// 2. Set up environment for test config
	_ = os.Setenv("TEST_MOCK_LLM_URL", mockLLMURL)
	_ = os.Setenv("MOCK_API_KEY", "sk-test-key-12345")

	// 3. Reserve a loopback listener for the gateway
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Printf("Failed to reserve listener: %v\n", err)
		os.Exit(1)
	}
	gatewayURL = "http://" + listener.Addr().String()

	// 4. Create a test provider and registry
	testProvider := NewTestProvider(mockLLMURL, "sk-test-key-12345")
	registry := providers.NewModelRegistry()
	registry.RegisterProvider(testProvider)

	// Initialize registry to discover models from test provider
	if err := registry.Initialize(testContext); err != nil {
		fmt.Printf("Failed to initialize model registry: %v\n", err)
		os.Exit(1)
	}

	router, err := providers.NewRouter(registry)
	if err != nil {
		fmt.Printf("Failed to create router: %v\n", err)
		os.Exit(1)
	}

	// 5. Start the gateway server (bind to loopback only)
	// Note: No master key for e2e tests (tests run in unsafe mode)
	testServer = server.New(router, &server.Config{})
	serverDone = make(chan error, 1)
	go func() {
		serverDone <- testServer.StartWithListener(testContext, listener)
	}()

	// 6. Wait for server to be healthy
	if err := waitForServer(gatewayURL + "/health"); err != nil {
		fmt.Printf("Server failed to start: %v\n", err)
		cleanup()
		os.Exit(1)
	}

	// 7. Run tests
	code := m.Run()

	// 8. Cleanup
	cleanup()
	os.Exit(code)
}

// cleanup shuts down all test resources.
func cleanup() {
	cancelFunc()
	if testServer != nil && serverDone != nil {
		select {
		case err := <-serverDone:
			if err != nil {
				fmt.Printf("Server shutdown error: %v\n", err)
			}
		case <-time.After(5 * time.Second):
			fmt.Printf("Server shutdown timed out\n")
		}
	}
	if mockServer != nil {
		mockServer.Close()
	}
}

// waitForServer waits for the server to become healthy.
func waitForServer(healthURL string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	for range 30 {
		resp, err := client.Get(healthURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("server did not become healthy within timeout")
}

// TestProvider is a test provider that forwards requests to the mock LLM server.
type TestProvider struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewTestProvider creates a new test provider.
func NewTestProvider(baseURL, apiKey string) *TestProvider {
	return &TestProvider{
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// ChatCompletion forwards the request to the mock server.
func (p *TestProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return forwardChatRequest(ctx, p.httpClient, p.baseURL, p.apiKey, req, false)
}

// StreamChatCompletion forwards the streaming request to the mock server.
func (p *TestProvider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return forwardStreamRequest(ctx, p.httpClient, p.baseURL, p.apiKey, req)
}

// ListModels returns a mock list of models.
func (p *TestProvider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return &core.ModelsResponse{
		Object: "list",
		Data: []core.Model{
			{ID: "gpt-4.1", Object: "model", OwnedBy: "openai"},
			{ID: "gpt-4", Object: "model", OwnedBy: "openai"},
			{ID: "gpt-3.5-turbo", Object: "model", OwnedBy: "openai"},
		},
	}, nil
}

// Responses forwards the responses API request to the mock server.
func (p *TestProvider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return forwardResponsesRequest(ctx, p.httpClient, p.baseURL, p.apiKey, req, false)
}

// StreamResponses forwards the streaming responses API request to the mock server.
func (p *TestProvider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return forwardResponsesStreamRequest(ctx, p.httpClient, p.baseURL, p.apiKey, req)
}

func (p *TestProvider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("test provider does not support embeddings", nil)
}
