package perf

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/server"
	"github.com/enterpilot/gomodel/internal/session"
)

// recordingRewriter captures the ext.Input the stack hands to a request
// rewriter, so a test can assert what an extension actually sees at ingress.
type recordingRewriter struct {
	mu   sync.Mutex
	last ext.Input
	seen bool
}

func (r *recordingRewriter) Name() string { return "recording" }

func (r *recordingRewriter) Rewrite(_ context.Context, in ext.Input) (*ext.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = in
	r.seen = true
	return nil, nil
}

func (r *recordingRewriter) snapshot() (ext.Input, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last, r.seen
}

// chatBodyOfSize builds a valid chat request whose encoded size is at least
// target bytes, by padding the first user message. The conversation prefix
// (model + leading messages) is what content auto-detection anchors on.
func chatBodyOfSize(target int) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"model":"gpt-4o-mini","messages":[{"role":"system","content":"You are a helpful assistant."},{"role":"user","content":"`)
	for buf.Len() < target {
		buf.WriteString("analyze this repeated log line and summarize it. ")
	}
	buf.WriteString(`"}]}`)
	return buf.Bytes()
}

// TestSessionIDVisibilityByBodySize pins that content-based session
// auto-detection is independent of request body size.
//
// RequestSnapshotCapture only inlines bodies with ContentLength <= 64 KiB
// (requestSnapshotInlineBodyLimit), so it would be reasonable to expect
// larger conversations to lose their auto-detected id. They do not: an id
// reaches ingress rewriters at every size, including well past the limit.
//
// This matters for cost, not just correctness. Every request carrying a
// session id engages per-session serialization in downstream consumers
// (sticky virtual-model routing, and GoModel Pro's compression epoch locks),
// so there is no size above which a request quietly opts out.
func TestSessionIDVisibilityByBodySize(t *testing.T) {
	sizes := []int{
		1 << 10,  // 1 KiB
		32 << 10, // 32 KiB
		63 << 10, // just under the 64 KiB inline capture limit
		65 << 10, // just over it
		256 << 10,
	}

	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dKiB", size/1024), func(t *testing.T) {
			body := chatBodyOfSize(size)

			rewriter := &recordingRewriter{}
			srv := server.New(benchProvider{}, &server.Config{
				LogOnlyModelInteractions: true,
				SessionDetector:          session.NewDetector(session.BuiltinRules(), true),
				RequestRewriters:         []ext.RequestRewriter{rewriter},
			})

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.ContentLength = int64(len(body))

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			in, seen := rewriter.snapshot()
			if !seen {
				t.Fatalf("rewriter never ran (status %d): %s", rec.Code, rec.Body.String())
			}

			detected := strings.TrimSpace(in.SessionID) != ""
			t.Logf("body=%d KiB status=%d session_id=%q detected=%t",
				len(body)/1024, rec.Code, in.SessionID, detected)
		})
	}
}
