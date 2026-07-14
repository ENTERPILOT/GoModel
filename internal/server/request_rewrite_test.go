package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
)

type stubRewriter struct {
	name    string
	calls   int
	rewrite func(in ext.Input) (*ext.Result, error)
}

func (r *stubRewriter) Name() string { return r.name }

func (r *stubRewriter) Rewrite(_ context.Context, in ext.Input) (*ext.Result, error) {
	r.calls++
	if r.rewrite == nil {
		return nil, nil
	}
	return r.rewrite(in)
}

func replaceBodyRewriter(name, old, new string) *stubRewriter {
	return &stubRewriter{
		name: name,
		rewrite: func(in ext.Input) (*ext.Result, error) {
			return &ext.Result{Body: bytes.ReplaceAll(in.Body, []byte(old), []byte(new))}, nil
		},
	}
}

func newRewriteTestProvider() *capturingProvider {
	return &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-4o-mini"},
			response: &core.ChatResponse{
				ID:      "chatcmpl-rewrite",
				Object:  "chat.completion",
				Created: 1234567890,
				Model:   "gpt-4o-mini",
				Choices: []core.Choice{
					{
						Index:        0,
						Message:      core.ResponseMessage{Role: "assistant", Content: "ok"},
						FinishReason: "stop",
					},
				},
			},
		},
	}
}

func postJSON(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestRequestRewriteMiddlewareRewritesChatCompletions(t *testing.T) {
	provider := newRewriteTestProvider()
	var seenAuth, seenPlain string
	annotating := &stubRewriter{
		name: "annotate",
		rewrite: func(in ext.Input) (*ext.Result, error) {
			seenAuth = in.Header.Get("Authorization")
			seenPlain = in.Header.Get("X-Custom-Trace")
			header := http.Header{}
			header.Set("X-Test-Rewritten", "yes")
			return &ext.Result{
				Body:           bytes.ReplaceAll(in.Body, []byte("PING"), []byte("PONG")),
				ResponseHeader: header,
			}, nil
		},
	}
	srv := New(provider, &Config{RequestRewriters: []ext.RequestRewriter{annotating}})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"PING"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-secret")
	req.Header.Set("X-Custom-Trace", "trace-1")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if provider.capturedChatReq == nil {
		t.Fatal("expected chat request to be captured")
	}
	content, _ := provider.capturedChatReq.Messages[0].Content.(string)
	if content != "PONG" {
		t.Errorf("provider saw content %q, want %q", content, "PONG")
	}
	if got := rec.Header().Get("X-Test-Rewritten"); got != "yes" {
		t.Errorf("expected annotation header, got %q", got)
	}
	if seenAuth != "[REDACTED]" {
		t.Errorf("rewriter saw Authorization %q, want it redacted", seenAuth)
	}
	if seenPlain != "trace-1" {
		t.Errorf("rewriter saw X-Custom-Trace %q, want original value", seenPlain)
	}
}

func TestRequestRewriteMiddlewareRewritesMessages(t *testing.T) {
	provider := newRewriteTestProvider()
	srv := New(provider, &Config{
		RequestRewriters: []ext.RequestRewriter{replaceBodyRewriter("swap", "PING", "PONG")},
	})

	rec := postJSON(t, srv, "/v1/messages",
		`{"model":"gpt-4o-mini","max_tokens":16,"messages":[{"role":"user","content":"PING"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if provider.capturedChatReq == nil {
		t.Fatal("expected chat request to be captured")
	}
	body, err := json.Marshal(provider.capturedChatReq)
	if err != nil {
		t.Fatalf("marshal captured request: %v", err)
	}
	if !strings.Contains(string(body), "PONG") || strings.Contains(string(body), "PING") {
		t.Errorf("provider request not rewritten: %s", body)
	}
}

func TestRequestRewriteMiddlewareEndpointGating(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"count_tokens subroute", http.MethodPost, "/v1/messages/count_tokens", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`},
		{"models listing", http.MethodGet, "/v1/models", ""},
		{"embeddings", http.MethodPost, "/v1/embeddings", `{"model":"gpt-4o-mini","input":"hi"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rewriter := &stubRewriter{name: "tracker"}
			srv := New(newRewriteTestProvider(), &Config{RequestRewriters: []ext.RequestRewriter{rewriter}})

			var reqBody *strings.Reader
			if tt.body != "" {
				reqBody = strings.NewReader(tt.body)
			} else {
				reqBody = strings.NewReader("")
			}
			req := httptest.NewRequest(tt.method, tt.path, reqBody)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rewriter.calls != 0 {
				t.Errorf("rewriter invoked %d times on %s %s, want 0", rewriter.calls, tt.method, tt.path)
			}
		})
	}
}

func TestRequestRewriteMiddlewareChainsInRegistrationOrder(t *testing.T) {
	provider := newRewriteTestProvider()
	srv := New(provider, &Config{
		RequestRewriters: []ext.RequestRewriter{
			replaceBodyRewriter("first", "PING", "PING-A"),
			replaceBodyRewriter("second", "PING-A", "PING-A-B"),
		},
	})

	rec := postJSON(t, srv, "/v1/chat/completions",
		`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"PING"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	content, _ := provider.capturedChatReq.Messages[0].Content.(string)
	if content != "PING-A-B" {
		t.Errorf("provider saw content %q, want chained rewrite %q", content, "PING-A-B")
	}
}

func TestRequestRewriteMiddlewareRejectionError(t *testing.T) {
	rejecting := &stubRewriter{
		name: "policy",
		rewrite: func(_ ext.Input) (*ext.Result, error) {
			return nil, &ext.RejectionError{Status: http.StatusUnprocessableEntity, Code: "policy_violation", Message: "blocked"}
		},
	}

	t.Run("openai dialect", func(t *testing.T) {
		srv := New(newRewriteTestProvider(), &Config{RequestRewriters: []ext.RequestRewriter{rejecting}})
		rec := postJSON(t, srv, "/v1/chat/completions",
			`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got %d (%s)", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "invalid_request_error") || !strings.Contains(body, "policy_violation") {
			t.Errorf("expected OpenAI error envelope with code, got: %s", body)
		}
	})

	t.Run("anthropic dialect", func(t *testing.T) {
		srv := New(newRewriteTestProvider(), &Config{RequestRewriters: []ext.RequestRewriter{rejecting}})
		rec := postJSON(t, srv, "/v1/messages",
			`{"model":"gpt-4o-mini","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got %d (%s)", rec.Code, rec.Body.String())
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("invalid JSON error body: %v", err)
		}
		if envelope.Type != "error" {
			t.Errorf("expected anthropic error envelope, got: %s", rec.Body.String())
		}
	})
}

func TestRequestRewriteMiddlewareInternalErrorFailsClosed(t *testing.T) {
	provider := newRewriteTestProvider()
	failing := &stubRewriter{
		name: "broken",
		rewrite: func(_ ext.Input) (*ext.Result, error) {
			return nil, errors.New("boom")
		},
	}
	srv := New(provider, &Config{RequestRewriters: []ext.RequestRewriter{failing}})

	rec := postJSON(t, srv, "/v1/chat/completions",
		`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d (%s)", rec.Code, rec.Body.String())
	}
	if provider.capturedChatReq != nil {
		t.Error("provider must not be called when a rewriter fails (fail-closed)")
	}
}

func TestRequestRewriteMiddlewareLargeBody(t *testing.T) {
	provider := newRewriteTestProvider()
	srv := New(provider, &Config{
		RequestRewriters: []ext.RequestRewriter{replaceBodyRewriter("swap", "NEEDLE", "REPLACED")},
	})

	// Exceed the 64KB inline snapshot capture limit so the middleware takes
	// the lazy full-read path.
	padding := strings.Repeat("x", 70*1024)
	body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"NEEDLE ` + padding + `"}]}`
	rec := postJSON(t, srv, "/v1/chat/completions", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	content, _ := provider.capturedChatReq.Messages[0].Content.(string)
	if !strings.HasPrefix(content, "REPLACED") {
		t.Errorf("large body was not rewritten, content prefix: %.40q", content)
	}
}

func TestExtensionRoutesMiddlewareAndAuthSkipPaths(t *testing.T) {
	provider := newRewriteTestProvider()
	srv := New(provider, &Config{
		MasterKey: "secret",
		ExtraMiddleware: []echo.MiddlewareFunc{
			func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c *echo.Context) error {
					c.Response().Header().Set("X-Ext-Middleware", "ran")
					return next(c)
				}
			},
		},
		ExtraRoutes: []func(*echo.Echo){
			func(e *echo.Echo) {
				e.GET("/sso/callback", func(c *echo.Context) error {
					return c.String(http.StatusOK, "callback")
				})
			},
		},
		ExtraAuthSkipPaths: []string{"/sso/*"},
	})

	t.Run("extension route is public via auth skip path", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/sso/callback", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "callback" {
			t.Fatalf("expected public 200 callback, got %d (%s)", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("X-Ext-Middleware") != "ran" {
			t.Error("extension middleware did not run")
		}
	})

	t.Run("core routes still require auth", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 without credentials, got %d", rec.Code)
		}
	})
}

func TestRequestRewriteMiddlewareAuditKeepsOriginalBody(t *testing.T) {
	provider := newRewriteTestProvider()
	auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogBodies: true}}
	srv := New(provider, &Config{
		AuditLogger:      auditLogger,
		RequestRewriters: []ext.RequestRewriter{replaceBodyRewriter("swap", "PING", "PONG")},
	})

	rec := postJSON(t, srv, "/v1/chat/completions",
		`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"PING"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	content, _ := provider.capturedChatReq.Messages[0].Content.(string)
	if content != "PONG" {
		t.Fatalf("provider saw content %q, want rewritten %q", content, "PONG")
	}
	if len(auditLogger.entries) == 0 {
		t.Fatal("expected an audit entry")
	}
	entryJSON, err := json.Marshal(auditLogger.entries[0])
	if err != nil {
		t.Fatalf("marshal audit entry: %v", err)
	}
	if !strings.Contains(string(entryJSON), "PING") {
		t.Errorf("audit entry must contain the original client body: %s", entryJSON)
	}
	if strings.Contains(string(entryJSON), "PONG") && !strings.Contains(string(entryJSON), `"ok"`) {
		t.Errorf("audit request body appears rewritten: %s", entryJSON)
	}
}

func TestRequestRewriteMiddlewareRecordsRevisions(t *testing.T) {
	type rewriteDetail struct {
		Note string `json:"note"`
	}
	detailed := &stubRewriter{
		name: "swap",
		rewrite: func(in ext.Input) (*ext.Result, error) {
			return &ext.Result{
				Body:   bytes.ReplaceAll(in.Body, []byte("PING"), []byte("PONG")),
				Detail: rewriteDetail{Note: "swapped ping"},
			}, nil
		},
	}

	run := func(t *testing.T, logBodies bool) *auditlog.LogEntry {
		t.Helper()
		auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogBodies: logBodies}}
		srv := New(newRewriteTestProvider(), &Config{
			AuditLogger: auditLogger,
			RequestRewriters: []ext.RequestRewriter{
				detailed,
				replaceBodyRewriter("upper", "PONG", "PONG!"),
			},
		})
		rec := postJSON(t, srv, "/v1/chat/completions",
			`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"PING"}]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		if len(auditLogger.entries) == 0 {
			t.Fatal("expected an audit entry")
		}
		return auditLogger.entries[0]
	}

	t.Run("with body logging", func(t *testing.T) {
		entry := run(t, true)
		revisions := entry.Data.RequestRevisions
		if len(revisions) != 2 {
			t.Fatalf("expected 2 revisions, got %d", len(revisions))
		}
		first, second := revisions[0], revisions[1]
		if first.Seq != 1 || first.Rewriter != "swap" || second.Seq != 2 || second.Rewriter != "upper" {
			t.Errorf("revision order/naming wrong: %+v", revisions)
		}
		if first.BytesBefore == 0 || first.BytesAfter == 0 {
			t.Errorf("revision sizes missing: %+v", first)
		}
		if first.Detail == nil {
			t.Error("rewriter detail must be recorded")
		}
		firstBody, _ := json.Marshal(first.Body)
		secondBody, _ := json.Marshal(second.Body)
		if !strings.Contains(string(firstBody), "PONG") || strings.Contains(string(firstBody), "PONG!") {
			t.Errorf("first revision body must be the intermediate rewrite: %s", firstBody)
		}
		if !strings.Contains(string(secondBody), "PONG!") {
			t.Errorf("second revision body must be the final rewrite: %s", secondBody)
		}
	})

	t.Run("without body logging", func(t *testing.T) {
		entry := run(t, false)
		revisions := entry.Data.RequestRevisions
		if len(revisions) != 2 {
			t.Fatalf("expected 2 revisions, got %d", len(revisions))
		}
		for _, revision := range revisions {
			if revision.Body != nil {
				t.Errorf("revision %d must not capture the body when body logging is off", revision.Seq)
			}
			if revision.BytesBefore == 0 || revision.BytesAfter == 0 {
				t.Errorf("revision %d sizes missing", revision.Seq)
			}
		}
	})
}

func TestRequestRewriteMiddlewareStoresTokensSavedInContext(t *testing.T) {
	compressor := &stubRewriter{
		name: "compressor",
		rewrite: func(in ext.Input) (*ext.Result, error) {
			return &ext.Result{Body: in.Body, TokensSaved: 123}, nil
		},
	}
	trimmer := &stubRewriter{
		name: "trimmer",
		rewrite: func(in ext.Input) (*ext.Result, error) {
			return &ext.Result{Body: in.Body, TokensSaved: 7}, nil
		},
	}
	// A savings claim without an applied body rewrite must not count.
	phantom := &stubRewriter{
		name: "phantom",
		rewrite: func(in ext.Input) (*ext.Result, error) {
			return &ext.Result{TokensSaved: 999}, nil
		},
	}
	noop := &stubRewriter{name: "noop"}

	var got int
	next := func(c *echo.Context) error {
		got = core.RewriteTokensSavedFromContext(c.Request().Context())
		return c.NoContent(http.StatusOK)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[]}`))
	c := e.NewContext(req, httptest.NewRecorder())

	mw := RequestRewriteMiddleware([]ext.RequestRewriter{compressor, trimmer, phantom, noop}, nil)
	if err := mw(next)(c); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}
	if got != 130 {
		t.Fatalf("context tokens saved = %d, want 130 (sum across applied rewriters only)", got)
	}
}

func TestRequestRewriteMiddlewareIgnoresSavingsWithoutAppliedBody(t *testing.T) {
	// Header-only annotator claiming savings without returning a body: the
	// request is unchanged, so no savings may be recorded.
	annotator := &stubRewriter{
		name: "annotator",
		rewrite: func(in ext.Input) (*ext.Result, error) {
			header := http.Header{}
			header.Set("X-Annotate", "yes")
			return &ext.Result{ResponseHeader: header, TokensSaved: 55}, nil
		},
	}

	var got int
	next := func(c *echo.Context) error {
		got = core.RewriteTokensSavedFromContext(c.Request().Context())
		return c.NoContent(http.StatusOK)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[]}`))
	c := e.NewContext(req, httptest.NewRecorder())

	mw := RequestRewriteMiddleware([]ext.RequestRewriter{annotator}, nil)
	if err := mw(next)(c); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}
	if got != 0 {
		t.Fatalf("context tokens saved = %d, want 0 when no body rewrite was applied", got)
	}
}

func TestRequestRewriteMiddlewareNoSavingsLeavesContextZero(t *testing.T) {
	var got int
	next := func(c *echo.Context) error {
		got = core.RewriteTokensSavedFromContext(c.Request().Context())
		return c.NoContent(http.StatusOK)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[]}`))
	c := e.NewContext(req, httptest.NewRecorder())

	// A rewriter that changes the body without reporting savings must not
	// invent a savings value.
	mw := RequestRewriteMiddleware([]ext.RequestRewriter{replaceBodyRewriter("swap", "gpt", "GPT")}, nil)
	if err := mw(next)(c); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}
	if got != 0 {
		t.Fatalf("context tokens saved = %d, want 0", got)
	}
}
