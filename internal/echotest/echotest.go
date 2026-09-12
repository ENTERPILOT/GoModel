// Package echotest builds echo contexts for handler unit tests so each test
// states only what differs: method, target, body, and the occasional header
// or path value.
package echotest

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

// Option adjusts the request or context built by Request.
type Option func(*settings)

type settings struct {
	headers     http.Header
	pathValues  echo.PathValues
	path        string
	values      map[string]any
	contentType string
}

// WithHeader sets a request header.
func WithHeader(key, value string) Option {
	return func(s *settings) { s.headers.Set(key, value) }
}

// WithPathValue binds a route parameter (c.Param) for the handler.
func WithPathValue(name, value string) Option {
	return func(s *settings) {
		s.pathValues = append(s.pathValues, echo.PathValue{Name: name, Value: value})
	}
}

// WithPath sets the route path template (c.Path), for handlers that inspect it.
func WithPath(path string) Option {
	return func(s *settings) { s.path = path }
}

// WithValue stores a context value (c.Set) before the handler runs, for
// values middleware would normally provide.
func WithValue(key string, value any) Option {
	return func(s *settings) { s.values[key] = value }
}

// WithContentType overrides the Content-Type set for a non-nil body.
func WithContentType(contentType string) Option {
	return func(s *settings) { s.contentType = contentType }
}

// Request builds an echo context and recorder for a handler call.
//
// body may be nil (no body), a string or []byte (sent verbatim), an io.Reader,
// or any other value, which is JSON-encoded. A non-nil body gets a JSON
// Content-Type unless WithContentType overrides it.
func Request(t testing.TB, method, target string, body any, opts ...Option) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	s := settings{headers: http.Header{}, values: map[string]any{}, contentType: echo.MIMEApplicationJSON}
	for _, opt := range opts {
		opt(&s)
	}

	req := httptest.NewRequest(method, target, bodyReader(t, body))
	if body != nil {
		req.Header.Set(echo.HeaderContentType, s.contentType)
	}
	maps.Copy(req.Header, s.headers)

	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	if s.path != "" {
		c.SetPath(s.path)
	}
	if len(s.pathValues) > 0 {
		c.SetPathValues(s.pathValues)
	}
	for key, value := range s.values {
		c.Set(key, value)
	}
	return c, rec
}

// Get is Request for a GET with no body.
func Get(t testing.TB, target string, opts ...Option) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	return Request(t, http.MethodGet, target, nil, opts...)
}

// Post is Request for a POST with a JSON body.
func Post(t testing.TB, target string, body any, opts ...Option) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	return Request(t, http.MethodPost, target, body, opts...)
}

// Decode unmarshals the recorded JSON response body into T, failing the test
// when it is not valid JSON for T.
func Decode[T any](t testing.TB, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("echotest: decode response body as %T: %v\nbody: %s", out, err, rec.Body.String())
	}
	return out
}

func bodyReader(t testing.TB, body any) io.Reader {
	t.Helper()
	switch b := body.(type) {
	case nil:
		return nil
	case string:
		return strings.NewReader(b)
	case []byte:
		return bytes.NewReader(b)
	case io.Reader:
		return b
	default:
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("echotest: marshal request body %T: %v", body, err)
		}
		return bytes.NewReader(data)
	}
}
