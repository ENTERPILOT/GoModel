// Package core provides core types and interfaces for the LLM gateway.
package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ErrorType represents the type of error that occurred
type ErrorType string

const (
	// ErrorTypeProvider indicates an upstream provider error (5xx)
	ErrorTypeProvider ErrorType = "provider_error"
	// ErrorTypeRateLimit indicates a rate limit error (429)
	ErrorTypeRateLimit ErrorType = "rate_limit_error"
	// ErrorTypeInvalidRequest indicates a client error (4xx)
	ErrorTypeInvalidRequest ErrorType = "invalid_request_error"
	// ErrorTypeAuthentication indicates an authentication error (401)
	ErrorTypeAuthentication ErrorType = "authentication_error"
	// ErrorTypeNotFound indicates a not found error (404)
	ErrorTypeNotFound ErrorType = "not_found_error"
)

// GatewayError is the base error type for all gateway errors
type GatewayError struct {
	Type       ErrorType `json:"type"`
	Message    string    `json:"message"`
	StatusCode int       `json:"status_code"`
	Provider   string    `json:"provider,omitempty"`
	Param      *string   `json:"param" extensions:"x-nullable"`
	Code       *string   `json:"code" extensions:"x-nullable"`
	// Original error for debugging (not exposed to clients)
	Err error `json:"-"`
}

// OpenAIErrorEnvelope documents the public OpenAI-compatible error response.
type OpenAIErrorEnvelope struct {
	Error OpenAIErrorObject `json:"error" binding:"required"`
}

// OpenAIErrorObject is the error object exposed in public API responses.
type OpenAIErrorObject struct {
	Type    ErrorType `json:"type" binding:"required"`
	Message string    `json:"message" binding:"required"`
	Param   *string   `json:"param" binding:"required" extensions:"x-nullable"`
	Code    *string   `json:"code" binding:"required" extensions:"x-nullable"`
}

// Error implements the error interface
func (e *GatewayError) Error() string {
	if e.Provider != "" {
		return fmt.Sprintf("[%s] %s: %s", e.Provider, e.Type, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// Unwrap implements the error unwrapping interface
func (e *GatewayError) Unwrap() error {
	return e.Err
}

// HTTPStatusCode returns the appropriate HTTP status code for this error
func (e *GatewayError) HTTPStatusCode() int {
	if e.StatusCode != 0 {
		return e.StatusCode
	}
	// Default status codes based on error type
	switch e.Type {
	case ErrorTypeRateLimit:
		return http.StatusTooManyRequests
	case ErrorTypeInvalidRequest:
		return http.StatusBadRequest
	case ErrorTypeAuthentication:
		return http.StatusUnauthorized
	case ErrorTypeNotFound:
		return http.StatusNotFound
	case ErrorTypeProvider:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// ToJSON converts the error to a JSON-compatible map
func (e *GatewayError) ToJSON() map[string]any {
	var param any
	if e.Param != nil {
		param = *e.Param
	}

	var code any
	if e.Code != nil {
		code = *e.Code
	}

	return map[string]any{
		"error": map[string]any{
			"type":    e.Type,
			"message": e.Message,
			"param":   param,
			"code":    code,
		},
	}
}

// WithParam annotates the error with the offending parameter name.
func (e *GatewayError) WithParam(param string) *GatewayError {
	e.Param = &param
	return e
}

// WithCode annotates the error with a machine-readable error code.
func (e *GatewayError) WithCode(code string) *GatewayError {
	e.Code = &code
	return e
}

// NewProviderError creates a new provider error (upstream 5xx)
func NewProviderError(provider string, statusCode int, message string, err error) *GatewayError {
	return &GatewayError{
		Type:       ErrorTypeProvider,
		Message:    message,
		StatusCode: statusCode,
		Provider:   provider,
		Err:        err,
	}
}

// NewRateLimitError creates a new rate limit error (429)
func NewRateLimitError(provider string, message string) *GatewayError {
	return &GatewayError{
		Type:       ErrorTypeRateLimit,
		Message:    message,
		StatusCode: http.StatusTooManyRequests,
		Provider:   provider,
	}
}

// NewInvalidRequestError creates a new invalid request error (400)
func NewInvalidRequestError(message string, err error) *GatewayError {
	return NewInvalidRequestErrorWithStatus(http.StatusBadRequest, message, err)
}

// NewInvalidRequestErrorWithStatus creates a new invalid request error with a specific status code
func NewInvalidRequestErrorWithStatus(statusCode int, message string, err error) *GatewayError {
	return &GatewayError{
		Type:       ErrorTypeInvalidRequest,
		Message:    message,
		StatusCode: statusCode,
		Err:        err,
	}
}

// NewAuthenticationError creates a new authentication error (401)
func NewAuthenticationError(provider string, message string) *GatewayError {
	return &GatewayError{
		Type:       ErrorTypeAuthentication,
		Message:    message,
		StatusCode: http.StatusUnauthorized,
		Provider:   provider,
	}
}

// NewNotFoundError creates a new not found error (404)
func NewNotFoundError(message string) *GatewayError {
	return &GatewayError{
		Type:       ErrorTypeNotFound,
		Message:    message,
		StatusCode: http.StatusNotFound,
	}
}

// ParseProviderError parses an error response from a provider and returns an appropriate GatewayError
func ParseProviderError(provider string, statusCode int, body []byte, originalErr error) *GatewayError {
	message := string(body)
	errorResponse := parseProviderErrorBody(body)
	if errorResponse.Message != "" {
		message = errorResponse.Message
	}

	// Determine error type based on status code
	var gatewayErr *GatewayError
	switch {
	case statusCode == http.StatusUnauthorized:
		gatewayErr = &GatewayError{
			Type:       ErrorTypeAuthentication,
			Message:    message,
			StatusCode: http.StatusUnauthorized,
			Provider:   provider,
			Err:        originalErr,
		}
	case statusCode == http.StatusForbidden:
		gatewayErr = &GatewayError{
			Type:       ErrorTypeAuthentication,
			Message:    message,
			StatusCode: http.StatusForbidden,
			Provider:   provider,
			Err:        originalErr,
		}
	case statusCode == http.StatusTooManyRequests:
		gatewayErr = &GatewayError{
			Type:       ErrorTypeRateLimit,
			Message:    message,
			StatusCode: http.StatusTooManyRequests,
			Provider:   provider,
			Err:        originalErr,
		}
	case statusCode == http.StatusNotFound:
		// 404 - model or resource not found
		gatewayErr = NewNotFoundError(message)
		gatewayErr.Provider = provider
		gatewayErr.Err = originalErr
	case statusCode >= 400 && statusCode < 500:
		// Client errors from provider - mark as invalid request and preserve both provider info and original status code
		gatewayErr = NewInvalidRequestErrorWithStatus(statusCode, message, originalErr)
		gatewayErr.Provider = provider
	case statusCode >= 500:
		// Server errors from provider - preserve the original status code (500, 503, 504, etc.)
		gatewayErr = NewProviderError(provider, statusCode, message, originalErr)
	default:
		// For any other status codes (2xx, 3xx, etc.), treat as provider error with Bad Gateway
		gatewayErr = NewProviderError(provider, http.StatusBadGateway, message, originalErr)
	}

	if errorResponse.Param != "" {
		gatewayErr = gatewayErr.WithParam(errorResponse.Param)
	}
	if errorResponse.Code != "" {
		gatewayErr = gatewayErr.WithCode(errorResponse.Code)
	}

	return gatewayErr
}

type providerErrorDetails struct {
	Message string
	Param   string
	Code    string
}

func parseProviderErrorBody(body []byte) providerErrorDetails {
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Error) == 0 {
		return providerErrorDetails{}
	}

	if message := jsonString(payload.Error); message != "" {
		return providerErrorDetails{Message: message}
	}

	var errorFields map[string]json.RawMessage
	if err := json.Unmarshal(payload.Error, &errorFields); err != nil {
		return providerErrorDetails{}
	}

	details := providerErrorDetails{
		Message: jsonString(errorFields["message"]),
		Param:   jsonString(errorFields["param"]),
		Code:    jsonScalarString(errorFields["code"]),
	}

	if raw := providerErrorMetadataRaw(errorFields["metadata"]); shouldPreferProviderRaw(details.Message, raw) {
		details.Message = raw
	}

	return details
}

func providerErrorMetadataRaw(raw json.RawMessage) string {
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return ""
	}
	return jsonString(metadata["raw"])
}

func shouldPreferProviderRaw(message, raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(message)) {
	case "", "provider returned error":
		return true
	default:
		return false
	}
}

func jsonString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func jsonScalarString(raw json.RawMessage) string {
	if value := jsonString(raw); value != "" {
		return value
	}

	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return ""
	}
	return number.String()
}
