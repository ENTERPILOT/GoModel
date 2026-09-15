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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/session"
)

type stubRewriter struct {
	name    string
	calls   int
	rewrite func(in ext.Input) (*ext.Result, error)
}

type feedbackRewriter struct {
	stubRewriter
	feedbackCaptureObserver
}

type filteredFeedbackRewriter struct {
	feedbackRewriter
	want        bool
	panicFilter bool
}

func (r *filteredFeedbackRewriter) WantsResponseFeedback(ext.Input, *ext.Result) bool {
	if r.panicFilter {
		panic("feedback filter failed")
	}
	return r.want
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

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, provider.capturedChatReq)

	content, _ := provider.capturedChatReq.Messages[0].Content.(string)
	assert.Equal(t, "PONG", content)
	got := rec.Header().Get("X-Test-Rewritten")
	assert.Equal(t, "yes", got)
	assert.Equal(t, "[REDACTED]", seenAuth)
	assert.Equal(t, "trace-1", seenPlain)
}

func TestRequestRewriteMiddlewareDeliversProviderFeedback(t *testing.T) {
	provider := newRewriteTestProvider()
	provider.response.Usage = core.Usage{
		PromptTokens:        2048,
		PromptTokensDetails: &core.PromptTokensDetails{CachedTokens: 1536},
	}
	rewriter := &feedbackRewriter{name: "feedback"}
	srv := New(provider, &Config{
		RequestRewriters: []ext.RequestRewriter{rewriter},
		SessionDetector:  session.NewDetector(session.BuiltinRules(), false),
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-Id", "feedback-session")
	req.Header.Set("X-Request-Id", "feedback-request")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, rewriter.feedback, 1)

	got := rewriter.feedback[0]
	require.Equal(t, "feedback-request", got.requestID)
	require.Equal(t, "feedback-session", got.sessionID)
	require.Equal(t, 1536, got.cacheRead)
	require.True(t, got.usageObserved, "feedback = %+v", got)
}

func TestRequestRewriteMiddlewareHonorsResponseFeedbackFilter(t *testing.T) {
	for _, want := range []bool{false, true} {
		rewriter := &filteredFeedbackRewriter{
			name: "filtered",
			want: want,
		}
		var attached bool
		next := func(c *echo.Context) error {
			attached = hasResponseFeedbackObservers(c)
			return c.NoContent(http.StatusOK)
		}
		c, _ := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[]}`)
		err := RequestRewriteMiddleware([]ext.RequestRewriter{rewriter}, nil)(next)(c)
		require.NoError(t, err, "want=%v: middleware: %v", want, err)
		require.Equal(t, want, attached)
	}
}

func TestRequestRewriteMiddlewareIsolatesResponseFeedbackFilterPanic(t *testing.T) {
	provider := newRewriteTestProvider()
	rewriter := &filteredFeedbackRewriter{
		name:        "panicking-filter",
		panicFilter: true,
	}
	srv := New(provider, &Config{RequestRewriters: []ext.RequestRewriter{rewriter}})

	rec := postJSON(t, srv, "/v1/chat/completions",
		`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, provider.capturedChatReq)
	require.Empty(t, rewriter.feedback)
}

func TestRequestRewriteMiddlewareExposesSessionID(t *testing.T) {
	tests := []struct {
		name    string
		session string // stamped into the context before rewriters; "" = not detected
		want    string
	}{
		{name: "detected session propagates", session: "sess-42", want: "sess-42"},
		{name: "no detected session yields empty", session: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newRewriteTestProvider()
			var seenSession string
			capturing := &stubRewriter{
				name: "capture-session",
				rewrite: func(in ext.Input) (*ext.Result, error) {
					seenSession = in.SessionID
					return nil, nil
				},
			}
			// Session detection runs before rewriters and stamps the request
			// context; ExtraMiddleware runs even earlier, so it stands in for
			// the detector here.
			stampSession := func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c *echo.Context) error {
					if tt.session != "" {
						req := c.Request()
						c.SetRequest(req.WithContext(core.WithSessionID(req.Context(), tt.session)))
					}
					return next(c)
				}
			}
			srv := New(provider, &Config{
				RequestRewriters: []ext.RequestRewriter{capturing},
				ExtraMiddleware:  []echo.MiddlewareFunc{stampSession},
			})

			rec := postJSON(t, srv, "/v1/chat/completions",
				`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			// The empty-session expectation must not pass vacuously.
			require.Equal(t, 1, capturing.calls)
			assert.Equal(t, tt.want, seenSession)
		})
	}
}

func TestRequestRewriteMiddlewareRewritesMessages(t *testing.T) {
	provider := newRewriteTestProvider()
	srv := New(provider, &Config{
		RequestRewriters: []ext.RequestRewriter{replaceBodyRewriter("swap", "PING", "PONG")},
	})

	rec := postJSON(t, srv, "/v1/messages",
		`{"model":"gpt-4o-mini","max_tokens":16,"messages":[{"role":"user","content":"PING"}]}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, provider.capturedChatReq)

	body, err := json.Marshal(provider.capturedChatReq)
	require.NoError(t, err)
	assert.Contains(t, string(body), "PONG")
	assert.NotContains(t, string(body), "PING", "provider request not rewritten: %s", body)
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

			assert.Equal(t, 0, rewriter.calls, "rewriter invoked %d times on %s %s, want 0", rewriter.calls, tt.method, tt.path)
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

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	content, _ := provider.capturedChatReq.Messages[0].Content.(string)
	assert.Equal(t, "PING-A-B", content)
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

		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

		body := rec.Body.String()
		assert.Contains(t, body, "invalid_request_error")
		assert.Contains(t, body, "policy_violation")
	})

	t.Run("anthropic dialect", func(t *testing.T) {
		srv := New(newRewriteTestProvider(), &Config{RequestRewriters: []ext.RequestRewriter{rejecting}})
		rec := postJSON(t, srv, "/v1/messages",
			`{"model":"gpt-4o-mini","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)

		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

		var envelope struct {
			Type string `json:"type"`
		}
		err := json.Unmarshal(rec.Body.Bytes(), &envelope)
		require.NoError(t, err)
		assert.Equal(t, "error", envelope.Type, "expected anthropic error envelope, got: %s", rec.Body.String())
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

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Nil(t, provider.capturedChatReq)
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

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	content, _ := provider.capturedChatReq.Messages[0].Content.(string)
	assert.True(t, strings.HasPrefix(content, "REPLACED"), "large body was not rewritten, content prefix: %.40q", content)
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
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "callback", rec.Body.String())
		assert.Equal(t, "ran", rec.Header().Get("X-Ext-Middleware"))
	})

	t.Run("core routes still require auth", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
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

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	content, _ := provider.capturedChatReq.Messages[0].Content.(string)
	require.Equal(t, "PONG", content)
	require.NotEmpty(t, auditLogger.entries)

	entryJSON, err := json.Marshal(auditLogger.entries[0])
	require.NoError(t, err)
	assert.Contains(t, string(entryJSON), "PING", "audit entry must contain the original client body: %s", entryJSON)

	if strings.Contains(string(entryJSON), "PONG") {
		assert.Contains(t, string(entryJSON), `"ok"`, "audit request body appears rewritten")
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

	run := func(t *testing.T, logBodies, logRevisionBodies bool) *auditlog.LogEntry {
		t.Helper()
		auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogBodies: logBodies, LogRevisionBodies: logRevisionBodies}}
		srv := New(newRewriteTestProvider(), &Config{
			AuditLogger: auditLogger,
			RequestRewriters: []ext.RequestRewriter{
				detailed,
				replaceBodyRewriter("upper", "PONG", "PONG!"),
			},
		})
		rec := postJSON(t, srv, "/v1/chat/completions",
			`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"PING"}]}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotEmpty(t, auditLogger.entries)

		return auditLogger.entries[0]
	}

	t.Run("with body logging", func(t *testing.T) {
		entry := run(t, true, true)
		revisions := entry.Data.RequestRevisions
		require.Len(t, revisions, 2)

		first, second := revisions[0], revisions[1]
		assert.Equal(t, 1, first.Seq)
		assert.Equal(t, "swap", first.Rewriter)
		assert.Equal(t, 2, second.Seq)
		assert.Equal(t, "upper", second.Rewriter, "revision order/naming wrong: %+v", revisions)
		assert.NotZero(t, first.BytesBefore)
		assert.NotZero(t, first.BytesAfter, "revision sizes missing: %+v", first)
		assert.NotNil(t, first.Detail)

		firstBody, _ := json.Marshal(first.Body)
		secondBody, _ := json.Marshal(second.Body)
		assert.Contains(t, string(firstBody), "PONG")
		assert.NotContains(t, string(firstBody), "PONG!", "first revision body must be the intermediate rewrite: %s", firstBody)
		assert.Contains(t, string(secondBody), "PONG!", "second revision body must be the final rewrite: %s", secondBody)
	})

	t.Run("without body logging", func(t *testing.T) {
		entry := run(t, false, true)
		revisions := entry.Data.RequestRevisions
		require.Len(t, revisions, 2)

		for _, revision := range revisions {
			assert.Nil(t, revision.Body, "revision %d must not capture the body when body logging is off", revision.Seq)
			assert.NotZero(t, revision.BytesBefore)
			assert.NotZero(t, revision.BytesAfter, "revision %d sizes missing", revision.Seq)
		}
	})

	// LOGGING_LOG_REVISION_BODIES=false keeps the revision metadata but
	// drops the rewritten body copy even though body logging is on.
	t.Run("without revision body logging", func(t *testing.T) {
		entry := run(t, true, false)
		revisions := entry.Data.RequestRevisions
		require.Len(t, revisions, 2)

		for _, revision := range revisions {
			assert.Nil(t, revision.Body, "revision %d must not capture the body when revision body logging is off", revision.Seq)
			assert.NotZero(t, revision.BytesBefore)
			assert.NotZero(t, revision.BytesAfter, "revision %d sizes missing", revision.Seq)
			if revision.Rewriter == "swap" {
				assert.NotNil(t, revision.Detail, "rewriter detail must survive without the body")
			}
		}
	})
}

func TestRequestRewriteMiddlewareRecordsNoChangeRevisions(t *testing.T) {
	// A rewriter that inspects the request and forwards it untouched is
	// still a step operators need to see, so it gets a no-change revision.
	quiet := &stubRewriter{name: "quiet"}
	// Response headers and a structured detail without a body change (a
	// rewriter annotating why it did nothing) must not turn the step into a
	// real revision — but the detail is the explanation, so it is kept.
	annotating := &stubRewriter{
		name: "annotating",
		rewrite: func(ext.Input) (*ext.Result, error) {
			header := http.Header{}
			header.Set("X-Test-Rewriter", "skipped")
			return &ext.Result{
				ResponseHeader: header,
				Detail:         map[string]any{"reason": "nothing to compress"},
			}, nil
		},
	}

	auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogBodies: true}}
	srv := New(newRewriteTestProvider(), &Config{
		AuditLogger: auditLogger,
		RequestRewriters: []ext.RequestRewriter{
			quiet,
			replaceBodyRewriter("swap", "PING", "PONG"),
			annotating,
		},
	})
	rec := postJSON(t, srv, "/v1/chat/completions",
		`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"PING"}]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "skipped", rec.Header().Get("X-Test-Rewriter"))
	require.NotEmpty(t, auditLogger.entries)

	revisions := auditLogger.entries[0].Data.RequestRevisions
	require.Len(t, revisions, 3)

	for i, want := range []struct {
		rewriter string
		noChange bool
	}{{"quiet", true}, {"swap", false}, {"annotating", true}} {
		got := revisions[i]
		assert.Equal(t, i+1, got.Seq)
		assert.Equal(t, want.rewriter, got.Rewriter)
		assert.Equal(t, want.noChange, got.NoChange, "revision %d = %+v, want rewriter %q no_change=%v", i+1, got, want.rewriter, want.noChange)
	}

	quietRev := revisions[0]
	assert.NotZero(t, quietRev.BytesBefore)
	assert.Equal(t, quietRev.BytesBefore, quietRev.BytesAfter, "no-change revision must report equal sizes: %+v", quietRev)
	assert.Nil(t, quietRev.Body)
	assert.Equal(t, 0, quietRev.TokensSaved, "no-change revision must carry no body or savings: %+v", quietRev)

	// The trailing no-change step sees the body the previous rewriter produced.
	assert.Equal(t, revisions[1].BytesAfter, revisions[2].BytesBefore, "no-change revision must measure the current body: %+v", revisions[2])

	// A rewriter that reports why it changed nothing keeps that explanation.
	detail, ok := revisions[2].Detail.(map[string]any)
	assert.True(t, ok)
	assert.Equal(t, "nothing to compress", detail["reason"], "no-change revision must keep the rewriter detail, got %+v", revisions[2].Detail)
	assert.Nil(t, quietRev.Detail, "a rewriter that returned no result has no detail to record: %+v", quietRev)
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

	c, _ := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[]}`)

	mw := RequestRewriteMiddleware([]ext.RequestRewriter{compressor, trimmer, phantom, noop}, nil)
	err := mw(next)(c)
	require.NoError(t, err)
	require.Equal(t, 130, got)
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

	c, _ := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[]}`)

	mw := RequestRewriteMiddleware([]ext.RequestRewriter{annotator}, nil)
	err := mw(next)(c)
	require.NoError(t, err)
	require.Equal(t, 0, got)
}

func TestRequestRewriteMiddlewareNoSavingsLeavesContextZero(t *testing.T) {
	var got int
	next := func(c *echo.Context) error {
		got = core.RewriteTokensSavedFromContext(c.Request().Context())
		return c.NoContent(http.StatusOK)
	}

	c, _ := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[]}`)

	// A rewriter that changes the body without reporting savings must not
	// invent a savings value.
	mw := RequestRewriteMiddleware([]ext.RequestRewriter{replaceBodyRewriter("swap", "gpt", "GPT")}, nil)
	err := mw(next)(c)
	require.NoError(t, err)
	require.Equal(t, 0, got)
}
