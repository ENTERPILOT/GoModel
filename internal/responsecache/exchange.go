package responsecache

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
)

// exchange abstracts the transport for one cache-mediated request so the
// cache decision logic (skip checks, hit lookup, replay, miss capture) runs
// identically for HTTP requests and transport-free internal calls.
type exchange interface {
	// Context returns the request context carrying workflow, snapshot,
	// request-ID, and label values.
	Context() context.Context
	// Path is the request path used for cache keying, e.g. "/v1/chat/completions".
	Path() string
	Method() string
	// RequestHeader returns a request header value ("" when absent).
	RequestHeader(name string) string
	// ReplayHit writes a cached response to the caller.
	ReplayHit(requestBody, cached []byte, cacheType string) error
	// Capture runs next and returns the response bytes when they are a
	// cacheable success (HTTP 200, valid JSON or SSE, no failover involved).
	Capture(warnMessage string, next func() error) ([]byte, bool, error)
	// MarkHit records transport-level side effects of a cache hit (audit
	// enrichment on the HTTP path; a no-op for internal calls).
	MarkHit(cacheType string)
}

// echoExchange adapts an HTTP request served through Echo to the exchange
// interface. It is the only place cache logic touches the web framework.
type echoExchange struct {
	c *echo.Context
}

func (e *echoExchange) Context() context.Context { return e.c.Request().Context() }
func (e *echoExchange) Path() string             { return e.c.Request().URL.Path }
func (e *echoExchange) Method() string           { return e.c.Request().Method }

func (e *echoExchange) RequestHeader(name string) string {
	return e.c.Request().Header.Get(name)
}

func (e *echoExchange) ReplayHit(requestBody, cached []byte, cacheType string) error {
	return writeCachedResponse(e.c, e.Path(), requestBody, cached, cacheType)
}

func (e *echoExchange) Capture(warnMessage string, next func() error) ([]byte, bool, error) {
	return captureResponseForCache(e.c, e.Path(), warnMessage, next)
}

func (e *echoExchange) MarkHit(cacheType string) {
	auditlog.EnrichEntryWithCacheType(e.c, cacheType)
}

// InternalResponse is the buffered outcome of the transport-free LLM call a
// cache miss executes.
type InternalResponse struct {
	StatusCode   int
	ContentType  string
	Body         []byte
	FailoverUsed bool
}

// internalExchange runs the cache for a transport-free internal JSON request:
// request headers come from the originating request's snapshot (allowlisted),
// and the response is buffered instead of written to a socket.
type internalExchange struct {
	ctx    context.Context
	method string
	path   string
	header http.Header

	next func(ctx context.Context) (*InternalResponse, error)

	status       int
	respHeader   http.Header
	respBody     []byte
	failoverUsed bool
}

func newInternalExchange(
	ctx context.Context,
	method, path string,
	next func(ctx context.Context) (*InternalResponse, error),
) *internalExchange {
	return &internalExchange{
		ctx:        ctx,
		method:     method,
		path:       path,
		header:     internalRequestHeaders(ctx),
		next:       next,
		respHeader: make(http.Header),
	}
}

func (e *internalExchange) Context() context.Context { return e.ctx }
func (e *internalExchange) Path() string             { return e.path }
func (e *internalExchange) Method() string           { return e.method }

func (e *internalExchange) RequestHeader(name string) string {
	return e.header.Get(name)
}

func (e *internalExchange) ReplayHit(requestBody, cached []byte, cacheType string) error {
	if isStreamingRequest(e.path, requestBody) {
		e.respHeader.Set("Content-Type", "text/event-stream")
		e.respHeader.Set("Cache-Control", "no-cache")
		e.respHeader.Set("Connection", "keep-alive")
	} else {
		e.respHeader.Set("Content-Type", "application/json")
	}
	e.respHeader.Set("X-Cache", cacheHeaderValue(cacheType))
	e.status = http.StatusOK
	e.respBody = bytes.Clone(cached)
	return nil
}

// runNext executes the buffered LLM call and records its outcome on the
// exchange. Cache layers reach it through the next closures handed to
// handle(), mirroring how the HTTP path executes the real handler.
func (e *internalExchange) runNext() error {
	resp, err := e.next(e.ctx)
	if err != nil {
		return err
	}
	if resp == nil {
		return errors.New("internal cache request returned no response")
	}
	e.status = resp.StatusCode
	if resp.ContentType != "" {
		e.respHeader.Set("Content-Type", resp.ContentType)
	}
	e.respBody = resp.Body
	e.failoverUsed = resp.FailoverUsed
	return nil
}

func (e *internalExchange) Capture(warnMessage string, next func() error) ([]byte, bool, error) {
	if err := next(); err != nil {
		return nil, false, err
	}
	if !shouldStoreCapturedResponse(e.status) || len(e.respBody) == 0 {
		return nil, false, nil
	}
	if e.failoverUsed {
		return nil, false, nil
	}
	data, ok := cacheableResponseBody(e.respBody, e.respHeader.Get("Content-Type"))
	if !ok {
		slog.Warn(warnMessage, "path", e.path)
		return nil, false, nil
	}
	return data, true, nil
}

// MarkHit is a no-op: audit entries live in the Echo context store, which a
// transport-free call does not have. (This matches the previous synthetic-
// context behavior, where the store was always empty.)
func (e *internalExchange) MarkHit(string) {}

func (e *internalExchange) result() *InternalHandleResult {
	return &InternalHandleResult{
		StatusCode: e.status,
		Headers:    e.respHeader.Clone(),
		Body:       bytes.Clone(e.respBody),
		CacheType:  internalCacheType(e.respHeader.Get("X-Cache")),
	}
}
