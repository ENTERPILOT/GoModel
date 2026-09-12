// Package providertest holds helpers for provider adapter tests: upstream
// test servers that record what the adapter sent, and a shared contract
// check for providers built on the OpenAI-compatible adapter.
package providertest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// Recorded is one upstream request as the test server received it.
type Recorded struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
	// ReadErr is the error, if any, from reading the request body; Body then
	// holds whatever arrived before it.
	ReadErr error
}

// JSON decodes the recorded body as a JSON object.
func (r Recorded) JSON(t testing.TB) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("providertest: request body is not a JSON object: %v\nbody: %s", err, r.Body)
	}
	return out
}

// Capture collects the requests an upstream test server received.
type Capture struct {
	mu       sync.Mutex
	requests []Recorded
}

// Count returns how many requests the server received.
func (c *Capture) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// All returns every recorded request in arrival order.
func (c *Capture) All() []Recorded {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Recorded(nil), c.requests...)
}

// Last returns the most recent request, failing the test when none arrived
// or when its body could not be read in full.
func (c *Capture) Last(t testing.TB) Recorded {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == 0 {
		t.Fatal("providertest: upstream received no requests")
	}
	last := c.requests[len(c.requests)-1]
	if last.ReadErr != nil {
		t.Fatalf("providertest: reading upstream request body: %v", last.ReadErr)
	}
	return last
}

func (c *Capture) record(r *http.Request) {
	body, err := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, Recorded{
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   r.URL.Query(),
		Header:  r.Header.Clone(),
		Body:    body,
		ReadErr: err,
	})
}

// Server starts a test server that records each request before handing it
// to handler. The body stays readable by handler. The server closes when the
// test ends.
func Server(t testing.TB, handler http.HandlerFunc) (*httptest.Server, *Capture) {
	t.Helper()
	capture := &Capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.record(r)
		if handler != nil {
			handler(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, capture
}

// JSONServer starts a recording server that answers every request with
// status and body as application/json.
func JSONServer(t testing.TB, status int, body string) (*httptest.Server, *Capture) {
	t.Helper()
	return Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// SSEServer starts a recording server that answers every request with body
// as text/event-stream.
func SSEServer(t testing.TB, body string) (*httptest.Server, *Capture) {
	t.Helper()
	return Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	})
}

// RouteServer starts a recording server that dispatches on request path and
// answers unknown paths with 404. Each handler receives the recorder-wrapped
// request.
func RouteServer(t testing.TB, routes map[string]http.HandlerFunc) (*httptest.Server, *Capture) {
	t.Helper()
	return Server(t, func(w http.ResponseWriter, r *http.Request) {
		if handler, ok := routes[r.URL.Path]; ok {
			handler(w, r)
			return
		}
		http.NotFound(w, r)
	})
}
