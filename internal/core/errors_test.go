package core

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *GatewayError
		expected string
	}{
		{
			name: "error with provider",
			err: &GatewayError{
				Type:     ErrorTypeProvider,
				Message:  "upstream error",
				Provider: "openai",
			},
			expected: "[openai] provider_error: upstream error",
		},
		{
			name: "error without provider",
			err: &GatewayError{
				Type:    ErrorTypeInvalidRequest,
				Message: "bad request",
			},
			expected: "invalid_request_error: bad request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestGatewayError_Unwrap(t *testing.T) {
	originalErr := errors.New("original error")
	gatewayErr := &GatewayError{
		Type:    ErrorTypeProvider,
		Message: "wrapped error",
		Err:     originalErr,
	}
	unwrapped := gatewayErr.Unwrap()
	assert.Equal(t, originalErr, unwrapped)
}

func TestGatewayError_HTTPStatusCode(t *testing.T) {
	tests := []struct {
		name     string
		err      *GatewayError
		expected int
	}{
		{
			name: "explicit status code",
			err: &GatewayError{
				Type:       ErrorTypeProvider,
				StatusCode: http.StatusServiceUnavailable,
			},
			expected: http.StatusServiceUnavailable,
		},
		{
			name: "rate limit default",
			err: &GatewayError{
				Type: ErrorTypeRateLimit,
			},
			expected: http.StatusTooManyRequests,
		},
		{
			name: "invalid request default",
			err: &GatewayError{
				Type: ErrorTypeInvalidRequest,
			},
			expected: http.StatusBadRequest,
		},
		{
			name: "authentication default",
			err: &GatewayError{
				Type: ErrorTypeAuthentication,
			},
			expected: http.StatusUnauthorized,
		},
		{
			name: "not found default",
			err: &GatewayError{
				Type: ErrorTypeNotFound,
			},
			expected: http.StatusNotFound,
		},
		{
			name: "provider error default",
			err: &GatewayError{
				Type: ErrorTypeProvider,
			},
			expected: http.StatusBadGateway,
		},
		{
			name: "unknown error type",
			err: &GatewayError{
				Type: ErrorType("unknown"),
			},
			expected: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.HTTPStatusCode()
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestGatewayError_ToJSON(t *testing.T) {
	param := "model"
	code := "model_not_found"
	err := &GatewayError{
		Type:    ErrorTypeRateLimit,
		Message: "too many requests",
		Param:   &param,
		Code:    &code,
	}

	result := err.ToJSON()

	errorData, ok := result["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, ErrorTypeRateLimit, errorData["type"])
	assert.Equal(t, "too many requests", errorData["message"])
	assert.Equal(t, param, errorData["param"])
	assert.Equal(t, code, errorData["code"])
}

func TestGatewayError_ToJSON_DefaultsParamAndCodeToNull(t *testing.T) {
	err := &GatewayError{
		Type:    ErrorTypeRateLimit,
		Message: "too many requests",
	}

	result := err.ToJSON()
	errorData := result["error"].(map[string]any)
	value, ok := errorData["param"]
	require.True(t, ok)
	require.Nil(t, value)
	value, ok = errorData["code"]
	require.True(t, ok)
	require.Nil(t, value)
}

func TestNewProviderError(t *testing.T) {
	originalErr := errors.New("connection failed")
	err := NewProviderError("openai", http.StatusBadGateway, "upstream failed", originalErr)

	assert.Equal(t, ErrorTypeProvider, err.Type)
	assert.Equal(t, "openai", err.Provider)
	assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	assert.Equal(t, "upstream failed", err.Message)
	assert.Equal(t, originalErr, err.Err)
}

func TestNewRateLimitError(t *testing.T) {
	err := NewRateLimitError("anthropic", "rate limit exceeded")

	assert.Equal(t, ErrorTypeRateLimit, err.Type)
	assert.Equal(t, "anthropic", err.Provider)
	assert.Equal(t, http.StatusTooManyRequests, err.StatusCode)
	assert.Equal(t, "rate limit exceeded", err.Message)
}

func TestNewInvalidRequestError(t *testing.T) {
	originalErr := errors.New("missing field")
	err := NewInvalidRequestError("invalid input", originalErr)

	assert.Equal(t, ErrorTypeInvalidRequest, err.Type)
	assert.Equal(t, http.StatusBadRequest, err.StatusCode)
	assert.Equal(t, "invalid input", err.Message)
	assert.Equal(t, originalErr, err.Err)
}

func TestNewAuthenticationError(t *testing.T) {
	err := NewAuthenticationError("gemini", "invalid API key")

	assert.Equal(t, ErrorTypeAuthentication, err.Type)
	assert.Equal(t, "gemini", err.Provider)
	assert.Equal(t, http.StatusUnauthorized, err.StatusCode)
	assert.Equal(t, "invalid API key", err.Message)
}

func TestNewNotFoundError(t *testing.T) {
	err := NewNotFoundError("model not found")

	assert.Equal(t, ErrorTypeNotFound, err.Type)
	assert.Equal(t, http.StatusNotFound, err.StatusCode)
	assert.Equal(t, "model not found", err.Message)
}

func TestParseProviderError(t *testing.T) {
	tests := []struct {
		name            string
		provider        string
		statusCode      int
		body            []byte
		expectedType    ErrorType
		expectedStatus  int
		expectedMessage string
		expectedParam   *string
		expectedCode    *string
	}{
		{
			name:           "401 unauthorized",
			provider:       "openai",
			statusCode:     http.StatusUnauthorized,
			body:           []byte(`{"error": {"message": "Invalid API key"}}`),
			expectedType:   ErrorTypeAuthentication,
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "403 forbidden",
			provider:       "anthropic",
			statusCode:     http.StatusForbidden,
			body:           []byte(`{"error": {"message": "Access denied"}}`),
			expectedType:   ErrorTypeAuthentication,
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "429 rate limit",
			provider:       "gemini",
			statusCode:     http.StatusTooManyRequests,
			body:           []byte(`{"error": {"message": "Rate limit exceeded"}}`),
			expectedType:   ErrorTypeRateLimit,
			expectedStatus: http.StatusTooManyRequests,
		},
		{
			name:           "400 bad request",
			provider:       "openai",
			statusCode:     http.StatusBadRequest,
			body:           []byte(`{"error": {"message": "Invalid parameters"}}`),
			expectedType:   ErrorTypeInvalidRequest,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "500 server error",
			provider:       "anthropic",
			statusCode:     http.StatusInternalServerError,
			body:           []byte(`{"error": {"message": "Internal server error"}}`),
			expectedType:   ErrorTypeProvider,
			expectedStatus: http.StatusInternalServerError, // Now preserves original 500
		},
		{
			name:           "502 bad gateway",
			provider:       "gemini",
			statusCode:     http.StatusBadGateway,
			body:           []byte(`{"error": {"message": "Bad gateway"}}`),
			expectedType:   ErrorTypeProvider,
			expectedStatus: http.StatusBadGateway,
		},
		{
			name:           "plain text error response",
			provider:       "openai",
			statusCode:     http.StatusInternalServerError,
			body:           []byte("Internal Server Error"),
			expectedType:   ErrorTypeProvider,
			expectedStatus: http.StatusInternalServerError, // Now preserves original 500
		},
		{
			name:            "top-level Cohere error",
			provider:        "cohere",
			statusCode:      http.StatusUnprocessableEntity,
			body:            []byte(`{"error_type":"INVALID_TOOL_GENERATION","id":"request-id","message":"invalid tool generation"}`),
			expectedType:    ErrorTypeInvalidRequest,
			expectedStatus:  http.StatusUnprocessableEntity,
			expectedMessage: "invalid tool generation",
			expectedCode:    new("INVALID_TOOL_GENERATION"),
		},
		{
			name:            "nested Cohere error",
			provider:        "cohere",
			statusCode:      http.StatusBadRequest,
			body:            []byte(`{"error":{"message":"nested Cohere failure","code":"bad_request"}}`),
			expectedType:    ErrorTypeInvalidRequest,
			expectedStatus:  http.StatusBadRequest,
			expectedMessage: "nested Cohere failure",
			expectedCode:    new("bad_request"),
		},
		{
			name:            "scalar Cohere error",
			provider:        "cohere",
			statusCode:      http.StatusBadGateway,
			body:            []byte(`{"error":"scalar Cohere failure"}`),
			expectedType:    ErrorTypeProvider,
			expectedStatus:  http.StatusBadGateway,
			expectedMessage: "scalar Cohere failure",
		},
		{
			name:            "null Cohere error uses top-level message",
			provider:        "cohere",
			statusCode:      http.StatusUnprocessableEntity,
			body:            []byte(`{"error":null,"message":"top-level Cohere failure","error_type":"INVALID_REQUEST"}`),
			expectedType:    ErrorTypeInvalidRequest,
			expectedStatus:  http.StatusUnprocessableEntity,
			expectedMessage: "top-level Cohere failure",
			expectedCode:    new("INVALID_REQUEST"),
		},
		{
			name:           "json parse with message",
			provider:       "openai",
			statusCode:     http.StatusBadRequest,
			body:           []byte(`{"error": {"message": "Model not found", "type": "not_found", "param": "model", "code": "model_not_found"}}`),
			expectedType:   ErrorTypeInvalidRequest,
			expectedStatus: http.StatusBadRequest,
			expectedParam:  new("model"),
			expectedCode:   new("model_not_found"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ParseProviderError(tt.provider, tt.statusCode, tt.body, nil)

			assert.Equal(t, tt.expectedType, err.Type)
			assert.Equal(t, tt.expectedStatus, err.HTTPStatusCode())
			assert.Equal(t, tt.provider, err.Provider)

			if tt.expectedMessage != "" {
				assert.Equal(t, tt.expectedMessage, err.Message)
			} else {
				assert.NotEmpty(t, err.Message)
			}

			assert.Equal(t, tt.expectedParam, err.Param)
			assert.Equal(t, tt.expectedCode, err.Code)
		})
	}
}

func TestParseProviderError_OpenRouter_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		body        []byte
		wantType    ErrorType
		wantMessage string
		wantCode    *string
	}{
		{
			name:        "numeric code without metadata raw preserves message",
			body:        []byte(`{"error":{"message":"Provider returned error","code":429,"metadata":{"provider_name":"Together","is_byok":false,"retry_after_seconds":1}},"user_id":"user_test_123"}`),
			wantType:    ErrorTypeRateLimit,
			wantMessage: "Provider returned error",
			wantCode:    new("429"),
		},
		{
			name:        "metadata raw with non generic message preserves original message",
			body:        []byte(`{"error":{"message":"The selected provider rejected the request","code":"rate_limit_exceeded","metadata":{"raw":"deepseek/deepseek-v4-pro is temporarily rate-limited upstream. Please retry shortly.","provider_name":"Together"}},"user_id":"user_test_123"}`),
			wantType:    ErrorTypeRateLimit,
			wantMessage: "The selected provider rejected the request",
			wantCode:    new("rate_limit_exceeded"),
		},
		{
			name:        "empty message with metadata raw uses raw and numeric code",
			body:        []byte(`{"error":{"message":"","code":429,"metadata":{"raw":"deepseek/deepseek-v4-pro is temporarily rate-limited upstream. Please retry shortly.","provider_name":"Together","is_byok":false,"retry_after_seconds":1}},"user_id":"user_test_123"}`),
			wantType:    ErrorTypeRateLimit,
			wantMessage: "deepseek/deepseek-v4-pro is temporarily rate-limited upstream. Please retry shortly.",
			wantCode:    new("429"),
		},
		{
			name:        "provider returned prefix with metadata raw uses raw",
			body:        []byte(`{"error":{"message":"Provider returned an error: upstream rejected the request","code":429,"metadata":{"raw":"deepseek/deepseek-v4-pro is temporarily rate-limited upstream. Please retry shortly.","provider_name":"Together"}},"user_id":"user_test_123"}`),
			wantType:    ErrorTypeRateLimit,
			wantMessage: "deepseek/deepseek-v4-pro is temporarily rate-limited upstream. Please retry shortly.",
			wantCode:    new("429"),
		},
		{
			name:        "malformed metadata falls back to parsed message and numeric code",
			body:        []byte(`{"error":{"message":"Provider returned error","code":429,"metadata":"not an object"},"user_id":"user_test_123"}`),
			wantType:    ErrorTypeRateLimit,
			wantMessage: "Provider returned error",
			wantCode:    new("429"),
		},
		{
			name:        "missing metadata and object code falls back without code",
			body:        []byte(`{"error":{"message":"Provider returned error","code":{"status":429}},"user_id":"user_test_123"}`),
			wantType:    ErrorTypeRateLimit,
			wantMessage: "Provider returned error",
			wantCode:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ParseProviderError("openrouter", http.StatusTooManyRequests, tt.body, nil)

			require.Equal(t, tt.wantType, err.Type)
			require.Equal(t, tt.wantMessage, err.Message)
			require.Equal(t, tt.wantCode, err.Code)
		})
	}
}

func TestGatewayError_IsError(t *testing.T) {
	// Test that GatewayError can be used with errors.Is
	originalErr := errors.New("network error")
	gatewayErr := NewProviderError("openai", http.StatusBadGateway, "connection failed", originalErr)

	assert.ErrorIs(t, gatewayErr, originalErr)
}

func TestParseProviderError_Preserves4xxStatusCodes(t *testing.T) {
	// Test that ParseProviderError preserves the original 4xx status codes
	// for errors that are not specifically handled (401, 403, 429)
	tests := []struct {
		name             string
		provider         string
		statusCode       int
		body             []byte
		expectedType     ErrorType
		expectedStatus   int
		expectedProvider string
	}{
		{
			name:             "404 not found",
			provider:         "openai",
			statusCode:       http.StatusNotFound,
			body:             []byte(`{"error": {"message": "Model not found"}}`),
			expectedType:     ErrorTypeNotFound,
			expectedStatus:   http.StatusNotFound,
			expectedProvider: "openai",
		},
		{
			name:             "405 method not allowed",
			provider:         "anthropic",
			statusCode:       http.StatusMethodNotAllowed,
			body:             []byte(`{"error": {"message": "Method not allowed"}}`),
			expectedType:     ErrorTypeInvalidRequest,
			expectedStatus:   http.StatusMethodNotAllowed, // Should preserve 405
			expectedProvider: "anthropic",
		},
		{
			name:             "409 conflict",
			provider:         "gemini",
			statusCode:       http.StatusConflict,
			body:             []byte(`{"error": {"message": "Resource conflict"}}`),
			expectedType:     ErrorTypeInvalidRequest,
			expectedStatus:   http.StatusConflict, // Should preserve 409
			expectedProvider: "gemini",
		},
		{
			name:             "410 gone",
			provider:         "openai",
			statusCode:       http.StatusGone,
			body:             []byte(`{"error": {"message": "Resource is gone"}}`),
			expectedType:     ErrorTypeInvalidRequest,
			expectedStatus:   http.StatusGone, // Should preserve 410
			expectedProvider: "openai",
		},
		{
			name:             "413 payload too large",
			provider:         "anthropic",
			statusCode:       http.StatusRequestEntityTooLarge,
			body:             []byte(`{"error": {"message": "Request too large"}}`),
			expectedType:     ErrorTypeInvalidRequest,
			expectedStatus:   http.StatusRequestEntityTooLarge, // Should preserve 413
			expectedProvider: "anthropic",
		},
		{
			name:             "422 unprocessable entity",
			provider:         "openai",
			statusCode:       http.StatusUnprocessableEntity,
			body:             []byte(`{"error": {"message": "Invalid content"}}`),
			expectedType:     ErrorTypeInvalidRequest,
			expectedStatus:   http.StatusUnprocessableEntity, // Should preserve 422
			expectedProvider: "openai",
		},
		{
			name:             "400 bad request still works",
			provider:         "gemini",
			statusCode:       http.StatusBadRequest,
			body:             []byte(`{"error": {"message": "Bad request"}}`),
			expectedType:     ErrorTypeInvalidRequest,
			expectedStatus:   http.StatusBadRequest, // Should preserve 400
			expectedProvider: "gemini",
		},
		{
			name:             "plain text 404 error",
			provider:         "openai",
			statusCode:       http.StatusNotFound,
			body:             []byte("Not Found"),
			expectedType:     ErrorTypeNotFound,
			expectedStatus:   http.StatusNotFound,
			expectedProvider: "openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalErr := errors.New("original http error")
			err := ParseProviderError(tt.provider, tt.statusCode, tt.body, originalErr)

			assert.Equal(t, tt.expectedType, err.Type)
			assert.Equal(t, tt.expectedStatus, err.StatusCode)
			assert.Equal(t, tt.expectedStatus, err.HTTPStatusCode())
			assert.Equal(t, tt.expectedProvider, err.Provider)
			assert.NotEmpty(t, err.Message)
		})
	}
}

func TestParseProviderError_PreservesWrappedErrorsForAuthAndRateLimit(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantType   ErrorType
	}{
		{
			name:       "401 unauthorized",
			statusCode: http.StatusUnauthorized,
			wantType:   ErrorTypeAuthentication,
		},
		{
			name:       "403 forbidden",
			statusCode: http.StatusForbidden,
			wantType:   ErrorTypeAuthentication,
		},
		{
			name:       "429 rate limit",
			statusCode: http.StatusTooManyRequests,
			wantType:   ErrorTypeRateLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalErr := errors.New("upstream transport error")
			err := ParseProviderError("openai", tt.statusCode, []byte(`{"error":{"message":"boom"}}`), originalErr)

			require.Equal(t, tt.wantType, err.Type)
			require.ErrorIs(t, err, originalErr)
		})
	}
}

func TestParseProviderError_SpecialStatusCodesOverride(t *testing.T) {
	// Verify that special status codes (401, 403, 429) still have their special handling
	tests := []struct {
		name           string
		statusCode     int
		expectedType   ErrorType
		expectedStatus int
	}{
		{
			name:           "401 uses authentication error",
			statusCode:     http.StatusUnauthorized,
			expectedType:   ErrorTypeAuthentication,
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "403 uses authentication error",
			statusCode:     http.StatusForbidden,
			expectedType:   ErrorTypeAuthentication,
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "429 uses rate limit error",
			statusCode:     http.StatusTooManyRequests,
			expectedType:   ErrorTypeRateLimit,
			expectedStatus: http.StatusTooManyRequests,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ParseProviderError("test-provider", tt.statusCode, []byte(`{"error": {"message": "test"}}`), nil)

			assert.Equal(t, tt.expectedType, err.Type)
			assert.Equal(t, tt.expectedStatus, err.HTTPStatusCode())
		})
	}
}

func TestParseProviderError_Preserves5xxStatusCodes(t *testing.T) {
	// Test that ParseProviderError preserves the original 5xx status codes
	// to maintain semantic meaning of different server errors
	tests := []struct {
		name             string
		provider         string
		statusCode       int
		body             []byte
		expectedType     ErrorType
		expectedStatus   int
		expectedProvider string
	}{
		{
			name:             "500 internal server error",
			provider:         "openai",
			statusCode:       http.StatusInternalServerError,
			body:             []byte(`{"error": {"message": "Internal server error"}}`),
			expectedType:     ErrorTypeProvider,
			expectedStatus:   http.StatusInternalServerError, // Should preserve 500
			expectedProvider: "openai",
		},
		{
			name:             "501 not implemented",
			provider:         "anthropic",
			statusCode:       http.StatusNotImplemented,
			body:             []byte(`{"error": {"message": "Feature not implemented"}}`),
			expectedType:     ErrorTypeProvider,
			expectedStatus:   http.StatusNotImplemented, // Should preserve 501
			expectedProvider: "anthropic",
		},
		{
			name:             "502 bad gateway",
			provider:         "gemini",
			statusCode:       http.StatusBadGateway,
			body:             []byte(`{"error": {"message": "Bad gateway"}}`),
			expectedType:     ErrorTypeProvider,
			expectedStatus:   http.StatusBadGateway, // Should preserve 502
			expectedProvider: "gemini",
		},
		{
			name:             "503 service unavailable",
			provider:         "openai",
			statusCode:       http.StatusServiceUnavailable,
			body:             []byte(`{"error": {"message": "Service unavailable"}}`),
			expectedType:     ErrorTypeProvider,
			expectedStatus:   http.StatusServiceUnavailable, // Should preserve 503
			expectedProvider: "openai",
		},
		{
			name:             "504 gateway timeout",
			provider:         "anthropic",
			statusCode:       http.StatusGatewayTimeout,
			body:             []byte(`{"error": {"message": "Gateway timeout"}}`),
			expectedType:     ErrorTypeProvider,
			expectedStatus:   http.StatusGatewayTimeout, // Should preserve 504
			expectedProvider: "anthropic",
		},
		{
			name:             "507 insufficient storage",
			provider:         "gemini",
			statusCode:       http.StatusInsufficientStorage,
			body:             []byte(`{"error": {"message": "Insufficient storage"}}`),
			expectedType:     ErrorTypeProvider,
			expectedStatus:   http.StatusInsufficientStorage, // Should preserve 507
			expectedProvider: "gemini",
		},
		{
			name:             "plain text 503 error",
			provider:         "openai",
			statusCode:       http.StatusServiceUnavailable,
			body:             []byte("Service Temporarily Unavailable"),
			expectedType:     ErrorTypeProvider,
			expectedStatus:   http.StatusServiceUnavailable, // Should preserve 503 even for plain text
			expectedProvider: "openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalErr := errors.New("original http error")
			err := ParseProviderError(tt.provider, tt.statusCode, tt.body, originalErr)

			assert.Equal(t, tt.expectedType, err.Type)
			assert.Equal(t, tt.expectedStatus, err.StatusCode)
			assert.Equal(t, tt.expectedStatus, err.HTTPStatusCode())
			assert.Equal(t, tt.expectedProvider, err.Provider)
			assert.NotEmpty(t, err.Message)
		})
	}
}

func TestGatewayError_ToJSON_ProviderOnlyWhenUpstream(t *testing.T) {
	upstream := ParseProviderError("openai", http.StatusUnauthorized, []byte(`{"error":{"message":"bad key"}}`), nil)
	errorData := upstream.ToJSON()["error"].(map[string]any)
	require.Equal(t, "openai", errorData["provider"])

	gateway := NewAuthenticationError("", "invalid API key")
	errorData = gateway.ToJSON()["error"].(map[string]any)
	_, present := errorData["provider"]
	require.False(t, present, "ToJSON() should omit provider for gateway-originated errors, got %v", errorData["provider"])
}

func TestParseEmbeddedProviderError(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		wantError       bool
		expectedStatus  int
		expectedType    ErrorType
		expectedMessage string
	}{
		{
			name:            "bare error object",
			body:            `{"error":{"message":"upstream exploded","type":"server_error"}}`,
			wantError:       true,
			expectedStatus:  http.StatusBadGateway,
			expectedType:    ErrorTypeProvider,
			expectedMessage: "upstream exploded",
		},
		{
			name:            "error with embedded numeric status code",
			body:            `{"error":{"message":"Rate limited","code":429}}`,
			wantError:       true,
			expectedStatus:  http.StatusTooManyRequests,
			expectedType:    ErrorTypeRateLimit,
			expectedMessage: "Rate limited",
		},
		{
			name:            "error with embedded numeric-string status code",
			body:            `{"error":{"message":"no credit","code":"402"}}`,
			wantError:       true,
			expectedStatus:  http.StatusPaymentRequired,
			expectedType:    ErrorTypeInvalidRequest,
			expectedMessage: "no credit",
		},
		{
			name:            "error with non-HTTP code falls back to 502",
			body:            `{"error":{"message":"quota exceeded","code":1027}}`,
			wantError:       true,
			expectedStatus:  http.StatusBadGateway,
			expectedType:    ErrorTypeProvider,
			expectedMessage: "quota exceeded",
		},
		{
			name:            "string error member",
			body:            `{"error":"model overloaded"}`,
			wantError:       true,
			expectedStatus:  http.StatusBadGateway,
			expectedType:    ErrorTypeProvider,
			expectedMessage: "model overloaded",
		},
		{
			name:            "openrouter error beside metadata keys",
			body:            `{"error":{"message":"Provider returned error","code":429,"metadata":{"raw":"upstream rate limit hit"}},"user_id":"user_123"}`,
			wantError:       true,
			expectedStatus:  http.StatusTooManyRequests,
			expectedType:    ErrorTypeRateLimit,
			expectedMessage: "upstream rate limit hit",
		},
		{
			name:            "anthropic native error shape",
			body:            `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			wantError:       true,
			expectedStatus:  http.StatusBadGateway,
			expectedType:    ErrorTypeProvider,
			expectedMessage: "Overloaded",
		},
		{
			name:      "chat completion success",
			body:      `{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`,
			wantError: false,
		},
		{
			name:      "success content mentioning error key",
			body:      `{"id":"chatcmpl-1","choices":[{"message":{"content":"set \"error\" handler"}}]}`,
			wantError: false,
		},
		{
			name:      "responses object with null error",
			body:      `{"id":"resp_1","object":"response","status":"completed","error":null,"output":[]}`,
			wantError: false,
		},
		{
			name:      "failed responses object passes through",
			body:      `{"id":"resp_1","object":"response","status":"failed","error":{"code":"server_error","message":"boom"},"output":[]}`,
			wantError: false,
		},
		{
			name:      "error member beside usage passes through",
			body:      `{"error":null,"usage":{"total_tokens":10}}`,
			wantError: false,
		},
		{
			name:      "empty body",
			body:      "",
			wantError: false,
		},
		{
			name:      "non-object body",
			body:      `[{"error":"x"}]`,
			wantError: false,
		},
		{
			name:      "invalid json",
			body:      `{"error":{"message":"trunc`,
			wantError: false,
		},
		{
			name:      "jsonl body with error keys",
			body:      "{\"error\":{\"message\":\"a\"}}\n{\"error\":{\"message\":\"b\"}}",
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ParseEmbeddedProviderError("openrouter", []byte(tt.body))
			if !tt.wantError {
				require.Nil(t, err)
				return
			}
			require.NotNil(t, err)
			assert.Equal(t, tt.expectedStatus, err.StatusCode)
			assert.Equal(t, tt.expectedType, err.Type)
			assert.Equal(t, tt.expectedMessage, err.Message)
			assert.Equal(t, "openrouter", err.Provider)
			assert.NotEmpty(t, err.ResponseBody)
		})
	}
}

// BenchmarkParseEmbeddedProviderError exercises the hot-path classification:
// every 200 response body passes through it, so a large success body whose
// content merely mentions "error" must classify quickly.
func BenchmarkParseEmbeddedProviderError(b *testing.B) {
	// Escaped quotes inside content never match the `"error"` byte prefilter,
	// so this body is classified by the prefilter scan alone.
	largeSuccess := []byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"` +
		strings.Repeat(`set the \"error\" handler, log the \"error\" field, and retry. `, 800) +
		`"}}],"usage":{"total_tokens":420}}`)
	// A literal top-level "error" member (some providers emit "error": null on
	// success, and stored Responses API objects always carry one) defeats the
	// prefilter, so this body exercises the structural classification.
	largeSuccessErrorNull := []byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"` +
		strings.Repeat(`retry with backoff after transient failures and log the cause. `, 800) +
		`"}}],"usage":{"total_tokens":420},"error":null}`)
	cases := []struct {
		name string
		body []byte
	}{
		{"large success body mentioning error", largeSuccess},
		{"large success body with error null member", largeSuccessErrorNull},
		{"small success body", []byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"message":{"content":"hi"}}]}`)},
		{"bare error payload", []byte(`{"error":{"message":"Rate limited","code":429}}`)},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			b.SetBytes(int64(len(bc.body)))
			b.ReportAllocs()
			for b.Loop() {
				if err := ParseEmbeddedProviderError("openrouter", bc.body); (err != nil) != (bc.name == "bare error payload") {
					b.Fatal("unexpected classification")
				}
			}
		})
	}
}
