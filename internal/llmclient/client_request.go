package llmclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// maxErrorBodyBytes caps how much of an upstream error body is read into
// memory. It matches core's audit capture cap; a misbehaving upstream that
// answers an error status with an endless body must not be buffered whole.
const maxErrorBodyBytes = 64 * 1024

// extractModel attempts to extract the model name from a request body
// UnknownModel is the model label reported for requests whose model cannot be
// recovered from the request body (body-less discovery GETs, availability
// probes, multipart uploads). Hook consumers that attribute traffic per model
// should treat it as "not model-attributed".
const UnknownModel = "unknown"

func extractModel(body any) string {
	if body == nil {
		return UnknownModel
	}

	// Try ChatRequest
	if chatReq, ok := body.(*core.ChatRequest); ok && chatReq != nil {
		return chatReq.Model
	}

	// Try ResponsesRequest
	if respReq, ok := body.(*core.ResponsesRequest); ok && respReq != nil {
		return respReq.Model
	}

	// Try AudioSpeechRequest. Transcription has no JSON body (multipart upload),
	// so its model cannot be recovered here and stays "unknown".
	if speechReq, ok := body.(*core.AudioSpeechRequest); ok && speechReq != nil {
		return speechReq.Model
	}

	// Unknown request type
	return UnknownModel
}

// extractStatusCode tries to extract HTTP status code from an error
func extractStatusCode(err error) int {
	if gwErr, ok := errors.AsType[*core.GatewayError](err); ok {
		return gwErr.StatusCode
	}

	// Network or unknown error
	return 0
}

// doHTTPRequest executes a single HTTP request without retries and returns the
// live upstream response. Metrics hooks are called at the logical request
// level, not here, to avoid counting each attempt separately.
func (c *Client) doHTTPRequest(ctx context.Context, req Request) (*http.Response, error) {
	httpReq, err := c.buildRequest(ctx, req)
	if err != nil {
		// The transport owns closing the body once the request reaches it;
		// a request that never gets there must release the reader here.
		closeRawBodyReader(req)
		return nil, err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, core.NewProviderError(c.config.ProviderName, providerErrorStatusCode(err), "failed to send request: "+err.Error(), err)
	}
	return resp, nil
}

// closeRawBodyReader releases a caller-supplied streaming body when the
// request fails before reaching the HTTP transport, which otherwise closes it
// on every path. Pipe-backed uploads (files, audio transcription) rely on
// this to unblock their producer goroutines instead of leaking them — and the
// upload buffers they pin — for the process lifetime.
func closeRawBodyReader(req Request) {
	if closer, ok := req.RawBodyReader.(io.Closer); ok {
		_ = closer.Close()
	}
}

// doRequest executes a single HTTP request without retries.
// Note: Metrics hooks are called at the DoRaw level, not here, to avoid
// counting each retry attempt as a separate request.
func (c *Client) doRequest(ctx context.Context, req Request) (*Response, error) {
	resp, err := c.doHTTPRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	// Successful responses are read whole (large results are legitimate);
	// error bodies are bounded — they only feed error parsing and audit
	// capture, both of which cap at the same size.
	reader := io.Reader(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		reader = io.LimitReader(resp.Body, maxErrorBodyBytes)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, core.NewProviderError(c.config.ProviderName, providerErrorStatusCode(err), "failed to read response: "+err.Error(), err)
	}

	return &Response{
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Header:      resp.Header,
		Body:        body,
	}, nil
}

// buildRequest creates an HTTP request from a Request
func (c *Client) buildRequest(ctx context.Context, req Request) (*http.Request, error) {
	// Validate request
	if req.Method == "" {
		return nil, core.NewInvalidRequestError("HTTP method is required", nil)
	}
	if req.Endpoint == "" {
		return nil, core.NewInvalidRequestError("endpoint is required", nil)
	}

	// Validate HTTP method
	switch req.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
		// Valid methods
	default:
		return nil, core.NewInvalidRequestError(fmt.Sprintf("invalid HTTP method: %s", req.Method), nil)
	}

	url := c.BaseURL() + req.Endpoint

	var bodyReader io.Reader
	bodySources := 0
	if req.Body != nil {
		bodySources++
	}
	if req.RawBody != nil {
		bodySources++
	}
	if req.RawBodyReader != nil {
		bodySources++
	}
	if bodySources > 1 {
		return nil, core.NewInvalidRequestError("Body, RawBody, and RawBodyReader are mutually exclusive", nil)
	}
	if req.RawBodyReader != nil {
		bodyReader = req.RawBodyReader
	} else if req.RawBody != nil {
		bodyReader = bytes.NewReader(req.RawBody)
	} else if req.Body != nil {
		bodyBytes, err := json.Marshal(req.Body)
		if err != nil {
			return nil, core.NewInvalidRequestError("failed to marshal request", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, url, bodyReader)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to create request", err)
	}

	// Set default content type for requests with body
	if req.Body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	// Apply provider-specific headers
	if c.headerSetter != nil {
		c.headerSetter(httpReq)
	}

	// Apply request-specific headers
	for key, values := range req.Headers {
		httpReq.Header.Del(key)
		for _, value := range values {
			httpReq.Header.Add(key, value)
		}
	}

	return httpReq, nil
}

func providerErrorStatusCode(err error) int {
	if isTimeoutError(err) {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "client.timeout exceeded") ||
		strings.Contains(message, "timeout awaiting response headers")
}

// isLocalRequestBuildError reports whether err originated from buildRequest
// (e.g. an empty endpoint, an invalid HTTP method, mutually-exclusive bodies,
// or a marshal failure). Such errors are caller-side: they will repeat
// deterministically on retry and must not be charged to the circuit breaker
// because the upstream provider was never contacted.
//
// buildRequest is the only producer of *core.GatewayError with type
// ErrorTypeInvalidRequest along the doRequest/doHTTPRequest path — the other
// transport-layer wrappers all use NewProviderError. ParseProviderError runs
// only on a returned response body, so an InvalidRequest seen in the
// transport-error branch can only have come from buildRequest.
func isLocalRequestBuildError(err error) bool {
	if err == nil {
		return false
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr == nil {
		return false
	}
	return gatewayErr.Type == core.ErrorTypeInvalidRequest
}

func isClientTimeoutGatewayError(err error) bool {
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr == nil {
		return isTimeoutError(err)
	}
	if gatewayErr.StatusCode != http.StatusGatewayTimeout {
		return false
	}
	if isTimeoutError(gatewayErr.Err) {
		return true
	}
	return isTimeoutError(gatewayErr)
}
