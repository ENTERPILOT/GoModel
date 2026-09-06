// Package llmclient provides a base HTTP client for LLM providers with:
// - Request marshaling/unmarshaling
// - Retries with exponential backoff and jitter
// - Standardized error parsing, including errors embedded in 200-status bodies
// - Circuit breaking with half-open state protection
package llmclient

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/httpclient"
)

// RequestInfo contains metadata about a request for observability hooks
type RequestInfo struct {
	Provider     string // Configured provider name
	ProviderType string // Provider implementation type (e.g., "openai", "anthropic")
	Model        string // Model name (e.g., "gpt-4", "claude-3-opus")
	Operation    string // Semantic GenAI operation; empty for non-inference calls
	Endpoint     string // API endpoint (e.g., "/chat/completions", "/models")
	Method       string // HTTP method (e.g., "POST", "GET")
	Stream       bool   // Whether this is a streaming request
	// StreamUncertain means a bounded opaque-body inspection could not
	// determine intent before the upstream call began.
	StreamUncertain bool
}

// ResponseInfo contains metadata about a response for observability hooks
type ResponseInfo struct {
	Provider        string        // Configured provider name
	ProviderType    string        // Provider implementation type
	Model           string        // Model name
	Operation       string        // Semantic GenAI operation
	Endpoint        string        // API endpoint
	Method          string        // HTTP method
	StatusCode      int           // HTTP status code (0 if network error)
	Duration        time.Duration // Request duration
	Stream          bool          // Whether this was a streaming request
	StreamUncertain bool          // Whether request stream intent was unknown at dispatch
	Error           error         // Error if request failed (nil on success)
	// CircuitState is the selected provider or model breaker state after this request
	// completed ("closed", "half-open", "open"); empty when the breaker is
	// disabled. It reflects the moment of completion, so metrics built from it
	// update as traffic flows.
	CircuitState string
}

// Hooks defines observability callbacks for request lifecycle events.
// These hooks enable instrumentation without polluting business logic.
type Hooks struct {
	// OnRequestStart is called before a request is sent.
	// The returned context can be used to propagate trace spans or request IDs.
	OnRequestStart func(ctx context.Context, info RequestInfo) context.Context

	// OnRequestEnd is called after a request completes (success or failure).
	// For streaming requests, this is called when the stream starts, not when it closes.
	OnRequestEnd func(ctx context.Context, info ResponseInfo)

	// OnStreamFirstChunk is called once when a successful streaming response
	// body first returns bytes. It is not called for empty or unread streams.
	OnStreamFirstChunk func(ctx context.Context, info ResponseInfo)

	// OnStreamEmpty is called once when a successful streaming response body
	// ends (EOF or read error) before returning any bytes: the stream was
	// established but never delivered. OnStreamFirstChunk is not called for
	// it. Error carries the read error, io.EOF for a clean empty stream.
	OnStreamEmpty func(ctx context.Context, info ResponseInfo)
}

// Config holds configuration for the LLM client
type Config struct {
	// ProviderName is the identifier used in logs and metrics (e.g., "openai", "anthropic").
	ProviderName string
	// BaseURL is the base URL for the provider's API (e.g., "https://api.openai.com/v1").
	BaseURL string
	// Retry specifies retry behaviour for failed requests, including backoff and jitter settings.
	Retry config.RetryConfig
	// CircuitBreaker configures the circuit breaker that prevents cascading failures by
	// stopping requests to an unhealthy provider until it recovers.
	CircuitBreaker config.CircuitBreakerConfig
	// Hooks provides optional observability callbacks invoked on request start and end.
	Hooks Hooks
}

// DefaultConfig returns default client configuration
func DefaultConfig(providerName, baseURL string) Config {
	return Config{
		ProviderName:   providerName,
		BaseURL:        baseURL,
		Retry:          config.DefaultRetryConfig(),
		CircuitBreaker: config.DefaultCircuitBreakerConfig(),
	}
}

// HeaderSetter is a function that sets headers on an HTTP request
type HeaderSetter func(req *http.Request)

// Client is a base HTTP client for LLM providers
type Client struct {
	mu              sync.RWMutex
	httpClient      *http.Client
	config          Config
	headerSetter    HeaderSetter
	circuitBreaker  *circuitBreaker
	modelBreakers   sync.Map // model name -> *circuitBreaker
	retryStatuses   map[int]bool
	failureStatuses map[int]bool
}

// New creates a new LLM client with the given configuration
func New(cfg Config, headerSetter HeaderSetter) *Client {
	c := &Client{
		httpClient:   httpclient.NewDefaultHTTPClient(),
		config:       cfg,
		headerSetter: headerSetter,
	}

	// Config loading validates these lists. Programmatic callers receive the
	// defaults for nil lists, and no matches for invalid lists.
	c.retryStatuses, _ = config.ParseResilienceStatuses(cfg.Retry.RetryOnStatuses, config.DefaultRetryConfig().RetryOnStatuses)
	c.failureStatuses, _ = config.ParseResilienceStatuses(cfg.CircuitBreaker.FailureOnStatuses, config.DefaultCircuitBreakerConfig().FailureOnStatuses)

	// The breaker is off when explicitly disabled or when it can never trip.
	if cfg.CircuitBreaker.Enabled && cfg.CircuitBreaker.FailureThreshold > 0 {
		c.circuitBreaker = newCircuitBreaker(
			cfg.CircuitBreaker.FailureThreshold,
			cfg.CircuitBreaker.SuccessThreshold,
			cfg.CircuitBreaker.Timeout,
		)
	}

	return c
}

// NewWithHTTPClient creates a new LLM client with a custom HTTP client
func NewWithHTTPClient(httpClient *http.Client, cfg Config, headerSetter HeaderSetter) *Client {
	c := New(cfg, headerSetter)
	c.httpClient = httpClient
	return c
}

// SetBaseURL updates the base URL (thread-safe)
func (c *Client) SetBaseURL(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.config.BaseURL = url
}

// BaseURL returns the current base URL (thread-safe)
func (c *Client) BaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config.BaseURL
}

// Request represents an HTTP request to be made
type Request struct {
	Method   string
	Endpoint string
	Model    string
	// Operation explicitly identifies model inference semantics for
	// observability. Leave empty for control-plane and other non-inference calls.
	Operation       string
	Stream          bool   // explicit stream intent; Accept: text/event-stream remains a fallback
	StreamUncertain bool   // bounded opaque-body inspection could not determine stream intent
	Body            any    // Will be JSON marshaled if not nil
	RawBody         []byte // Used as-is (e.g., multipart form bodies). Mutually exclusive with Body and RawBodyReader.
	// RawBodyReader streams the request body without buffering it in memory.
	// It is intended for one-shot passthrough requests and is not replayable for retries.
	RawBodyReader io.Reader
	Headers       http.Header
}

// Response represents an HTTP response
type Response struct {
	StatusCode int
	// ContentType is the upstream response Content-Type header, preserved so
	// callers can describe the bytes actually returned (e.g. audio formats).
	ContentType string
	// Header carries the upstream response headers. It is used to audit failed
	// provider attempts; it is not relayed to API clients.
	Header http.Header
	Body   []byte
}
