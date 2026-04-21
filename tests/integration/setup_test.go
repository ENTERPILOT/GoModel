//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"gomodel/config"
	"gomodel/internal/app"
	"gomodel/internal/core"
	"gomodel/internal/providers"
)

// TestServerConfig configures how the test server is set up.
type TestServerConfig struct {
	// DBType is either "postgresql" or "mongodb"
	DBType string

	// AuditLogEnabled enables audit logging
	AuditLogEnabled bool

	// UsageEnabled enables usage tracking
	UsageEnabled bool

	// LogBodies enables body logging in audit logs
	LogBodies bool

	// LogHeaders enables header logging in audit logs
	LogHeaders bool

	// OnlyModelInteractions limits logging to model endpoints only
	OnlyModelInteractions bool

	// AdminEndpointsEnabled enables admin API endpoints
	AdminEndpointsEnabled bool

	// AdminUIEnabled enables admin dashboard UI
	AdminUIEnabled bool

	// GuardrailsEnabled enables reusable guardrail definitions and workflow execution.
	GuardrailsEnabled bool

	// MasterKey sets the authentication master key (empty = unsafe mode)
	MasterKey string
}

// TestServerFixture holds test server resources.
type TestServerFixture struct {
	// ServerURL is the base URL of the test server
	ServerURL string

	// App is the running application
	App *app.App

	// MockLLM is the mock LLM server
	MockLLM *MockLLMServer

	// PgPool is the PostgreSQL connection pool (for DB assertions)
	PgPool *pgxpool.Pool

	// MongoDb is the MongoDB database (for DB assertions)
	MongoDb *mongo.Database

	// DBType is the configured database type
	DBType string

	cancelFunc context.CancelFunc
}

// SetupTestServer creates a test server with the specified configuration.
func SetupTestServer(t *testing.T, cfg TestServerConfig) *TestServerFixture {
	t.Helper()

	ctx, cancel := context.WithCancel(GetTestContext())

	resetIntegrationStorage(t, cfg.DBType)

	// Create mock LLM server
	mockLLM := NewMockLLMServer()

	// Reserve a loopback listener up front so the port cannot be stolen before
	// the application starts accepting connections.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "failed to find available port")

	// Build app config
	port := listener.Addr().(*net.TCPAddr).Port
	appCfg := buildAppConfig(t, cfg, mockLLM.URL(), port)

	// Create provider factory
	factory := providers.NewProviderFactory()
	testProvider := NewTestProvider(mockLLM.URL(), "sk-test-key")
	factory.Add(providers.Registration{
		Type: "test",
		New:  func(_ providers.ProviderConfig, _ providers.ProviderOptions) core.Provider { return testProvider },
	})

	// Create app
	application, err := app.New(ctx, app.Config{
		AppConfig: appCfg,
		Factory:   factory,
	})
	require.NoError(t, err, "failed to create app")

	// Start server in background
	serverURL := "http://" + listener.Addr().String()
	go func() {
		_ = application.StartWithListener(context.Background(), listener)
	}()

	// Wait for server to be healthy
	err = waitForServer(serverURL + "/health")
	require.NoError(t, err, "server failed to become healthy")

	fixture := &TestServerFixture{
		ServerURL:  serverURL,
		App:        application,
		MockLLM:    mockLLM,
		DBType:     cfg.DBType,
		cancelFunc: cancel,
	}

	// Set database references for assertions
	switch cfg.DBType {
	case "postgresql":
		fixture.PgPool = GetPostgreSQLPool()
	case "mongodb":
		fixture.MongoDb = GetMongoDatabase()
	}

	return fixture
}

func resetIntegrationStorage(t *testing.T, dbType string) {
	t.Helper()

	switch dbType {
	case "postgresql":
		resetPostgreSQLStorage(t)
	case "mongodb":
		resetMongoDBStorage(t)
	default:
		t.Fatalf("unknown db type: %s", dbType)
	}
}

func resetPostgreSQLStorage(t *testing.T) {
	t.Helper()

	pool := GetPostgreSQLPool()
	require.NotNil(t, pool, "postgresql pool must be initialized")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tables := []string{
		"audit_logs",
		"usage",
		"workflow_versions",
		"guardrail_definitions",
		"auth_keys",
		"model_overrides",
		"aliases",
		"batches",
	}
	for _, table := range tables {
		_, err := pool.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
		require.NoError(t, err, "failed to reset table %s", table)
	}
}

func resetMongoDBStorage(t *testing.T) {
	t.Helper()

	db := GetMongoDatabase()
	require.NotNil(t, db, "mongodb database must be initialized")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	collections := []string{
		"audit_logs",
		"usage",
		"workflow_versions",
		"guardrail_definitions",
		"auth_keys",
		"model_overrides",
		"aliases",
		"batches",
	}
	for _, collection := range collections {
		err := db.Collection(collection).Drop(ctx)
		if err == nil {
			continue
		}
		var cmdErr mongo.CommandError
		if errors.As(err, &cmdErr) && cmdErr.Code == 26 {
			continue
		}
		require.NoError(t, err, "failed to reset collection %s", collection)
	}
}

// FlushAndClose flushes all pending log entries and closes loggers.
// CRITICAL: Call this before making any DB assertions.
func (f *TestServerFixture) FlushAndClose(t *testing.T) {
	t.Helper()

	// Close the app which flushes all loggers
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if f.App != nil {
		err := f.App.Shutdown(ctx)
		require.NoError(t, err, "failed to shutdown app")
	}
}

// Shutdown gracefully shuts down the test server.
func (f *TestServerFixture) Shutdown(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if f.App != nil {
		_ = f.App.Shutdown(ctx)
	}

	if f.MockLLM != nil {
		f.MockLLM.Close()
	}

	if f.cancelFunc != nil {
		f.cancelFunc()
	}
}

// buildAppConfig creates an application config for testing.
func buildAppConfig(t *testing.T, cfg TestServerConfig, mockLLMURL string, port int) *config.LoadResult {
	t.Helper()

	appCfg := &config.Config{
		Server: config.ServerConfig{
			Port:      fmt.Sprintf("%d", port),
			MasterKey: cfg.MasterKey,
		},
		Models: config.ModelsConfig{
			EnabledByDefault: true,
			OverridesEnabled: true,
		},
		Admin: config.AdminConfig{
			EndpointsEnabled: cfg.AdminEndpointsEnabled,
			UIEnabled:        cfg.AdminUIEnabled,
		},
		Guardrails: config.GuardrailsConfig{
			Enabled: cfg.GuardrailsEnabled,
		},
		Cache: config.CacheConfig{
			Model: config.ModelCacheConfig{
				Local: &config.LocalCacheConfig{CacheDir: ".cache"},
			},
		},
		Logging: config.LogConfig{
			Enabled:               cfg.AuditLogEnabled,
			LogBodies:             cfg.LogBodies,
			LogHeaders:            cfg.LogHeaders,
			BufferSize:            100,
			FlushInterval:         1,
			RetentionDays:         0,
			OnlyModelInteractions: cfg.OnlyModelInteractions,
		},
		Usage: config.UsageConfig{
			Enabled:                   cfg.UsageEnabled,
			EnforceReturningUsageData: true,
			BufferSize:                100,
			FlushInterval:             1,
			RetentionDays:             0,
		},
		Metrics: config.MetricsConfig{
			Enabled: false,
		},
	}

	// Configure storage based on DBType
	switch cfg.DBType {
	case "postgresql":
		appCfg.Storage = config.StorageConfig{
			Type: "postgresql",
			PostgreSQL: config.PostgreSQLStorageConfig{
				URL:      GetPostgreSQLURL(),
				MaxConns: 5,
			},
		}
	case "mongodb":
		appCfg.Storage = config.StorageConfig{
			Type: "mongodb",
			MongoDB: config.MongoDBStorageConfig{
				URL:      GetMongoURL(),
				Database: "gomodel_test",
			},
		}
	default:
		t.Fatalf("unsupported DB type: %s", cfg.DBType)
	}

	return &config.LoadResult{
		Config: appCfg,
		RawProviders: map[string]config.RawProviderConfig{
			"test": {
				Type:    "test",
				APIKey:  "sk-test-key",
				BaseURL: mockLLMURL,
			},
		},
	}
}

// waitForServer waits for the server to become healthy.
func waitForServer(healthURL string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 50; i++ {
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

// MockLLMServer is a mock LLM server for testing.
type MockLLMServer struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []RecordedRequest
}

// RecordedRequest stores one upstream request observed by the integration mock.
type RecordedRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

// NewMockLLMServer creates a new mock LLM server.
func NewMockLLMServer() *MockLLMServer {
	mock := &MockLLMServer{
		requests: make([]RecordedRequest, 0),
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read body for stream detection
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))

		mock.mu.Lock()
		mock.requests = append(mock.requests, RecordedRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Headers: r.Header.Clone(),
			Body:    append([]byte(nil), body...),
		})
		mock.mu.Unlock()

		switch r.URL.Path {
		case "/v1/chat/completions":
			handleChatCompletion(w, r, body)
		case "/v1/responses":
			handleResponses(w, r, body)
		case "/v1/models":
			handleModels(w)
		default:
			http.NotFound(w, r)
		}
	})

	server := httptest.NewServer(handler)
	mock.server = server
	return mock
}

// URL returns the server URL.
func (m *MockLLMServer) URL() string {
	return m.server.URL
}

// Close shuts down the server.
func (m *MockLLMServer) Close() {
	m.server.Close()
}

// Requests returns a safe snapshot of requests recorded by the mock server.
func (m *MockLLMServer) Requests() []RecordedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]RecordedRequest, len(m.requests))
	for i, req := range m.requests {
		out[i] = RecordedRequest{
			Method:  req.Method,
			Path:    req.Path,
			Headers: req.Headers.Clone(),
			Body:    append([]byte(nil), req.Body...),
		}
	}
	return out
}

// ResetRequests clears the recorded request history.
func (m *MockLLMServer) ResetRequests() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = m.requests[:0]
}

func handleChatCompletion(w http.ResponseWriter, _ *http.Request, body []byte) {
	// Check if streaming is requested
	var req struct {
		Stream bool   `json:"stream"`
		Model  string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err == nil && req.Stream {
		handleChatCompletionStream(w, req.Model)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	response := `{
		"id": "chatcmpl-test123",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "gpt-4",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "Hello! How can I help you today?"
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 8,
			"total_tokens": 18
		}
	}`
	_, _ = w.Write([]byte(response))
}

func handleChatCompletionStream(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	if model == "" {
		model = "gpt-4"
	}

	// Send content chunks
	chunks := []string{"Hello", "!", " How", " can", " I", " help", " you", " today", "?"}
	for i, chunk := range chunks {
		delta := map[string]interface{}{
			"id":      "chatcmpl-test-stream",
			"object":  "chat.completion.chunk",
			"model":   model,
			"created": 1700000000,
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"delta": map[string]interface{}{
						"content": chunk,
					},
					"finish_reason": nil,
				},
			},
		}

		// Last chunk has finish_reason and usage
		if i == len(chunks)-1 {
			delta["choices"].([]map[string]interface{})[0]["finish_reason"] = "stop"
			delta["usage"] = map[string]interface{}{
				"prompt_tokens":     10,
				"completion_tokens": 8,
				"total_tokens":      18,
			}
		}

		data, _ := json.Marshal(delta)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// Send done marker
	_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func handleResponses(w http.ResponseWriter, _ *http.Request, body []byte) {
	// Check if streaming is requested
	var req struct {
		Stream bool   `json:"stream"`
		Model  string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err == nil && req.Stream {
		handleResponsesStream(w, req.Model)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	response := `{
		"id": "resp-test123",
		"object": "response",
		"created_at": 1700000000,
		"model": "gpt-4",
		"output": [{
			"type": "message",
			"id": "msg-test123",
			"role": "assistant",
			"content": [{
				"type": "output_text",
				"text": "Hello! How can I help you today?"
			}]
		}],
		"usage": {
			"input_tokens": 10,
			"output_tokens": 8,
			"total_tokens": 18
		}
	}`
	_, _ = w.Write([]byte(response))
}

func handleResponsesStream(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	if model == "" {
		model = "gpt-4"
	}

	// Send response.created event
	createdEvent := map[string]interface{}{
		"type": "response.created",
		"response": map[string]interface{}{
			"id":         "resp-test-stream",
			"object":     "response",
			"created_at": 1700000000,
			"model":      model,
			"status":     "in_progress",
		},
	}
	data, _ := json.Marshal(createdEvent)
	_, _ = fmt.Fprintf(w, "event: response.created\ndata: %s\n\n", data)
	flusher.Flush()

	// Send content delta events
	chunks := []string{"Hello", "!", " How", " can", " I", " help", " you", "?"}
	for _, chunk := range chunks {
		deltaEvent := map[string]interface{}{
			"type":  "response.output_text.delta",
			"delta": chunk,
		}
		data, _ = json.Marshal(deltaEvent)
		_, _ = fmt.Fprintf(w, "event: response.output_text.delta\ndata: %s\n\n", data)
		flusher.Flush()
	}

	// Send response.completed event with usage
	doneEvent := map[string]interface{}{
		"type": "response.completed",
		"response": map[string]interface{}{
			"id":         "resp-test-stream",
			"object":     "response",
			"created_at": 1700000000,
			"model":      model,
			"status":     "completed",
			"usage": map[string]interface{}{
				"input_tokens":  10,
				"output_tokens": 8,
				"total_tokens":  18,
			},
		},
	}
	data, _ = json.Marshal(doneEvent)
	_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", data)
	flusher.Flush()
}

func handleModels(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	response := `{
		"object": "list",
		"data": [
			{"id": "gpt-4", "object": "model", "owned_by": "openai"},
			{"id": "gpt-4.1", "object": "model", "owned_by": "openai"},
			{"id": "gpt-3.5-turbo", "object": "model", "owned_by": "openai"}
		]
	}`
	_, _ = w.Write([]byte(response))
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
	return forwardChatRequest(ctx, p.httpClient, p.baseURL, p.apiKey, req)
}

// StreamChatCompletion forwards the streaming request to the mock server.
func (p *TestProvider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return forwardStreamingChatRequest(ctx, p.httpClient, p.baseURL, p.apiKey, req)
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
	return forwardResponsesRequest(ctx, p.httpClient, p.baseURL, p.apiKey, req)
}

// StreamResponses forwards the streaming responses API request to the mock server.
func (p *TestProvider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return forwardStreamingResponsesRequest(ctx, p.httpClient, p.baseURL, p.apiKey, req)
}

// Embeddings returns an error (test provider does not support embeddings).
func (p *TestProvider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("test provider does not support embeddings", nil)
}
