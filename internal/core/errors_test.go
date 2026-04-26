package core

import (
	"errors"
	"net/http"
	"testing"
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
			if got := tt.err.Error(); got != tt.expected {
				t.Errorf("Error() = %v, want %v", got, tt.expected)
			}
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

	if unwrapped := gatewayErr.Unwrap(); unwrapped != originalErr {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, originalErr)
	}
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
			if got := tt.err.HTTPStatusCode(); got != tt.expected {
				t.Errorf("HTTPStatusCode() = %v, want %v", got, tt.expected)
			}
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
	if !ok {
		t.Fatal("ToJSON() should return map with 'error' key")
	}

	if errorData["type"] != ErrorTypeRateLimit {
		t.Errorf("ToJSON() type = %v, want %v", errorData["type"], ErrorTypeRateLimit)
	}

	if errorData["message"] != "too many requests" {
		t.Errorf("ToJSON() message = %v, want %v", errorData["message"], "too many requests")
	}

	if errorData["param"] != param {
		t.Errorf("ToJSON() param = %v, want %v", errorData["param"], param)
	}

	if errorData["code"] != code {
		t.Errorf("ToJSON() code = %v, want %v", errorData["code"], code)
	}
}

func TestGatewayError_ToJSON_DefaultsParamAndCodeToNull(t *testing.T) {
	err := &GatewayError{
		Type:    ErrorTypeRateLimit,
		Message: "too many requests",
	}

	result := err.ToJSON()
	errorData := result["error"].(map[string]any)

	if value, ok := errorData["param"]; !ok || value != nil {
		t.Fatalf("ToJSON() param = %v, want nil", value)
	}

	if value, ok := errorData["code"]; !ok || value != nil {
		t.Fatalf("ToJSON() code = %v, want nil", value)
	}
}

func TestNewProviderError(t *testing.T) {
	originalErr := errors.New("connection failed")
	err := NewProviderError("openai", http.StatusBadGateway, "upstream failed", originalErr)

	if err.Type != ErrorTypeProvider {
		t.Errorf("Type = %v, want %v", err.Type, ErrorTypeProvider)
	}

	if err.Provider != "openai" {
		t.Errorf("Provider = %v, want %v", err.Provider, "openai")
	}

	if err.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %v, want %v", err.StatusCode, http.StatusBadGateway)
	}

	if err.Message != "upstream failed" {
		t.Errorf("Message = %v, want %v", err.Message, "upstream failed")
	}

	if err.Err != originalErr {
		t.Errorf("Err = %v, want %v", err.Err, originalErr)
	}
}

func TestNewRateLimitError(t *testing.T) {
	err := NewRateLimitError("anthropic", "rate limit exceeded")

	if err.Type != ErrorTypeRateLimit {
		t.Errorf("Type = %v, want %v", err.Type, ErrorTypeRateLimit)
	}

	if err.Provider != "anthropic" {
		t.Errorf("Provider = %v, want %v", err.Provider, "anthropic")
	}

	if err.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %v, want %v", err.StatusCode, http.StatusTooManyRequests)
	}

	if err.Message != "rate limit exceeded" {
		t.Errorf("Message = %v, want %v", err.Message, "rate limit exceeded")
	}
}

func TestNewInvalidRequestError(t *testing.T) {
	originalErr := errors.New("missing field")
	err := NewInvalidRequestError("invalid input", originalErr)

	if err.Type != ErrorTypeInvalidRequest {
		t.Errorf("Type = %v, want %v", err.Type, ErrorTypeInvalidRequest)
	}

	if err.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %v, want %v", err.StatusCode, http.StatusBadRequest)
	}

	if err.Message != "invalid input" {
		t.Errorf("Message = %v, want %v", err.Message, "invalid input")
	}

	if err.Err != originalErr {
		t.Errorf("Err = %v, want %v", err.Err, originalErr)
	}
}

func TestNewAuthenticationError(t *testing.T) {
	err := NewAuthenticationError("gemini", "invalid API key")

	if err.Type != ErrorTypeAuthentication {
		t.Errorf("Type = %v, want %v", err.Type, ErrorTypeAuthentication)
	}

	if err.Provider != "gemini" {
		t.Errorf("Provider = %v, want %v", err.Provider, "gemini")
	}

	if err.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %v, want %v", err.StatusCode, http.StatusUnauthorized)
	}

	if err.Message != "invalid API key" {
		t.Errorf("Message = %v, want %v", err.Message, "invalid API key")
	}
}

func TestNewNotFoundError(t *testing.T) {
	err := NewNotFoundError("model not found")

	if err.Type != ErrorTypeNotFound {
		t.Errorf("Type = %v, want %v", err.Type, ErrorTypeNotFound)
	}

	if err.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %v, want %v", err.StatusCode, http.StatusNotFound)
	}

	if err.Message != "model not found" {
		t.Errorf("Message = %v, want %v", err.Message, "model not found")
	}
}

func TestParseProviderError(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		statusCode     int
		body           []byte
		expectedType   ErrorType
		expectedStatus int
		expectedParam  *string
		expectedCode   *string
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

			if err.Type != tt.expectedType {
				t.Errorf("Type = %v, want %v", err.Type, tt.expectedType)
			}

			if err.HTTPStatusCode() != tt.expectedStatus {
				t.Errorf("HTTPStatusCode() = %v, want %v", err.HTTPStatusCode(), tt.expectedStatus)
			}

			if err.Provider != tt.provider {
				t.Errorf("Provider = %v, want %v", err.Provider, tt.provider)
			}

			if err.Message == "" {
				t.Error("Message should not be empty")
			}

			if !equalStringPointers(err.Param, tt.expectedParam) {
				t.Errorf("Param = %v, want %v", err.Param, tt.expectedParam)
			}

			if !equalStringPointers(err.Code, tt.expectedCode) {
				t.Errorf("Code = %v, want %v", err.Code, tt.expectedCode)
			}
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
			wantCode:    stringPointer("429"),
		},
		{
			name:        "metadata raw with non generic message preserves original message",
			body:        []byte(`{"error":{"message":"The selected provider rejected the request","code":"rate_limit_exceeded","metadata":{"raw":"deepseek/deepseek-v4-pro is temporarily rate-limited upstream. Please retry shortly.","provider_name":"Together"}},"user_id":"user_test_123"}`),
			wantType:    ErrorTypeRateLimit,
			wantMessage: "The selected provider rejected the request",
			wantCode:    stringPointer("rate_limit_exceeded"),
		},
		{
			name:        "empty message with metadata raw uses raw and numeric code",
			body:        []byte(`{"error":{"message":"","code":429,"metadata":{"raw":"deepseek/deepseek-v4-pro is temporarily rate-limited upstream. Please retry shortly.","provider_name":"Together","is_byok":false,"retry_after_seconds":1}},"user_id":"user_test_123"}`),
			wantType:    ErrorTypeRateLimit,
			wantMessage: "deepseek/deepseek-v4-pro is temporarily rate-limited upstream. Please retry shortly.",
			wantCode:    stringPointer("429"),
		},
		{
			name:        "provider returned prefix with metadata raw uses raw",
			body:        []byte(`{"error":{"message":"Provider returned an error: upstream rejected the request","code":429,"metadata":{"raw":"deepseek/deepseek-v4-pro is temporarily rate-limited upstream. Please retry shortly.","provider_name":"Together"}},"user_id":"user_test_123"}`),
			wantType:    ErrorTypeRateLimit,
			wantMessage: "deepseek/deepseek-v4-pro is temporarily rate-limited upstream. Please retry shortly.",
			wantCode:    stringPointer("429"),
		},
		{
			name:        "malformed metadata falls back to parsed message and numeric code",
			body:        []byte(`{"error":{"message":"Provider returned error","code":429,"metadata":"not an object"},"user_id":"user_test_123"}`),
			wantType:    ErrorTypeRateLimit,
			wantMessage: "Provider returned error",
			wantCode:    stringPointer("429"),
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

			if err.Type != tt.wantType {
				t.Fatalf("Type = %v, want %v", err.Type, tt.wantType)
			}
			if err.Message != tt.wantMessage {
				t.Fatalf("Message = %q, want %q", err.Message, tt.wantMessage)
			}
			if !equalStringPointers(err.Code, tt.wantCode) {
				t.Fatalf("Code = %v, want %v", err.Code, tt.wantCode)
			}
		})
	}
}

func equalStringPointers(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

func stringPointer(value string) *string {
	return &value
}

func TestGatewayError_AsError(t *testing.T) {
	// Test that GatewayError can be used with errors.As
	originalErr := NewRateLimitError("openai", "too many requests")
	var err error = originalErr

	var gatewayErr *GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Error("errors.As should work with GatewayError")
	}

	if gatewayErr.Type != ErrorTypeRateLimit {
		t.Errorf("Type = %v, want %v", gatewayErr.Type, ErrorTypeRateLimit)
	}
}

func TestGatewayError_IsError(t *testing.T) {
	// Test that GatewayError can be used with errors.Is
	originalErr := errors.New("network error")
	gatewayErr := NewProviderError("openai", http.StatusBadGateway, "connection failed", originalErr)

	if !errors.Is(gatewayErr, originalErr) {
		t.Error("errors.Is should work with wrapped errors in GatewayError")
	}
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

			if err.Type != tt.expectedType {
				t.Errorf("Type = %v, want %v", err.Type, tt.expectedType)
			}

			if err.StatusCode != tt.expectedStatus {
				t.Errorf("StatusCode = %v, want %v", err.StatusCode, tt.expectedStatus)
			}

			if err.HTTPStatusCode() != tt.expectedStatus {
				t.Errorf("HTTPStatusCode() = %v, want %v", err.HTTPStatusCode(), tt.expectedStatus)
			}

			if err.Provider != tt.expectedProvider {
				t.Errorf("Provider = %v, want %v", err.Provider, tt.expectedProvider)
			}

			if err.Message == "" {
				t.Error("Message should not be empty")
			}
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

			if err.Type != tt.wantType {
				t.Fatalf("Type = %v, want %v", err.Type, tt.wantType)
			}
			if !errors.Is(err, originalErr) {
				t.Fatalf("expected wrapped original error, got %v", err)
			}
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

			if err.Type != tt.expectedType {
				t.Errorf("Type = %v, want %v", err.Type, tt.expectedType)
			}

			if err.HTTPStatusCode() != tt.expectedStatus {
				t.Errorf("HTTPStatusCode() = %v, want %v", err.HTTPStatusCode(), tt.expectedStatus)
			}
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

			if err.Type != tt.expectedType {
				t.Errorf("Type = %v, want %v", err.Type, tt.expectedType)
			}

			if err.StatusCode != tt.expectedStatus {
				t.Errorf("StatusCode = %v, want %v", err.StatusCode, tt.expectedStatus)
			}

			if err.HTTPStatusCode() != tt.expectedStatus {
				t.Errorf("HTTPStatusCode() = %v, want %v", err.HTTPStatusCode(), tt.expectedStatus)
			}

			if err.Provider != tt.expectedProvider {
				t.Errorf("Provider = %v, want %v", err.Provider, tt.expectedProvider)
			}

			if err.Message == "" {
				t.Error("Message should not be empty")
			}
		})
	}
}
