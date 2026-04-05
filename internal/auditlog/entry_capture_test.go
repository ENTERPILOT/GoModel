package auditlog

import (
	"context"
	"net/http"
	"testing"

	"gomodel/internal/core"
)

func TestCaptureInternalJSONExchange_PreservesHeadersWithoutBodies(t *testing.T) {
	entry := &LogEntry{
		RequestID: "req_123",
		Data:      &LogData{},
	}
	ctx := core.WithRequestSnapshot(context.Background(), core.NewRequestSnapshot(
		"POST",
		"/v1/chat/completions",
		nil,
		nil,
		map[string][]string{
			"Traceparent": {`00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00`},
		},
		"application/json",
		nil,
		false,
		"req_123",
		nil,
		"/team/alpha",
	))

	CaptureInternalJSONExchange(entry, ctx, "POST", "/v1/chat/completions", nil, nil, nil, Config{
		LogHeaders: true,
		LogBodies:  false,
	})

	if entry.Data == nil {
		t.Fatal("Data = nil, want populated log data")
	}
	if got := entry.Data.RequestHeaders[http.CanonicalHeaderKey("X-Request-ID")]; got != "req_123" {
		t.Fatalf("RequestHeaders[X-Request-ID] = %q, want req_123", got)
	}
	if got := entry.Data.RequestHeaders[http.CanonicalHeaderKey(core.UserPathHeader)]; got != "/team/alpha" {
		t.Fatalf("RequestHeaders[%s] = %q, want /team/alpha", core.UserPathHeader, got)
	}
	if got := entry.Data.RequestHeaders["Traceparent"]; got == "" {
		t.Fatal("RequestHeaders[Traceparent] = empty, want propagated trace header")
	}
	if got := entry.Data.ResponseHeaders[http.CanonicalHeaderKey("X-Request-ID")]; got != "req_123" {
		t.Fatalf("ResponseHeaders[X-Request-ID] = %q, want req_123", got)
	}
	if entry.Data.RequestBody != nil || entry.Data.ResponseBody != nil {
		t.Fatal("expected no bodies when body logging is disabled")
	}
}

func TestCaptureInternalJSONExchange_PreservesHeadersWhenBodyMarshalFails(t *testing.T) {
	entry := &LogEntry{
		RequestID: "req_456",
		Data:      &LogData{},
	}
	ctx := core.WithEffectiveUserPath(context.Background(), "/team/beta")

	CaptureInternalJSONExchange(entry, ctx, "POST", "/v1/chat/completions", func() {}, func() {}, nil, Config{
		LogHeaders: true,
		LogBodies:  true,
	})

	if entry.Data == nil {
		t.Fatal("Data = nil, want populated log data")
	}
	if got := entry.Data.RequestHeaders[http.CanonicalHeaderKey("X-Request-ID")]; got != "req_456" {
		t.Fatalf("RequestHeaders[X-Request-ID] = %q, want req_456", got)
	}
	if got := entry.Data.RequestHeaders[http.CanonicalHeaderKey(core.UserPathHeader)]; got != "/team/beta" {
		t.Fatalf("RequestHeaders[%s] = %q, want /team/beta", core.UserPathHeader, got)
	}
	if got := entry.Data.ResponseHeaders[http.CanonicalHeaderKey("X-Request-ID")]; got != "req_456" {
		t.Fatalf("ResponseHeaders[X-Request-ID] = %q, want req_456", got)
	}
	if entry.Data.RequestBody != nil || entry.Data.ResponseBody != nil {
		t.Fatal("expected marshal failures to skip bodies while preserving headers")
	}
}

func TestCaptureInternalJSONExchange_DoesNotReuseIngressSnapshotOnMarshalFailure(t *testing.T) {
	entry := &LogEntry{
		RequestID: "req_789",
		Data:      &LogData{},
	}
	ctx := core.WithRequestSnapshot(context.Background(), core.NewRequestSnapshot(
		"POST",
		"/v1/chat/completions",
		nil,
		nil,
		map[string][]string{
			"Traceparent": {`00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00`},
		},
		"application/json",
		[]byte(`{"outer":"body"}`),
		false,
		"req_outer",
		nil,
		"/team/outer",
	))
	ctx = core.WithEffectiveUserPath(ctx, "/team/internal")

	CaptureInternalJSONExchange(entry, ctx, "POST", "/v1/chat/completions", func() {}, nil, nil, Config{
		LogHeaders: true,
		LogBodies:  true,
	})

	if entry.Data == nil {
		t.Fatal("Data = nil, want populated log data")
	}
	if entry.Data.RequestBody != nil {
		t.Fatalf("RequestBody = %#v, want nil to avoid leaking ingress snapshot body", entry.Data.RequestBody)
	}
	if entry.Data.RequestBodyTooBigToHandle {
		t.Fatal("RequestBodyTooBigToHandle = true, want false for marshal failure")
	}
	if got := entry.Data.RequestHeaders[http.CanonicalHeaderKey("X-Request-ID")]; got != "req_789" {
		t.Fatalf("RequestHeaders[X-Request-ID] = %q, want req_789", got)
	}
	if got := entry.Data.RequestHeaders[http.CanonicalHeaderKey(core.UserPathHeader)]; got != "/team/internal" {
		t.Fatalf("RequestHeaders[%s] = %q, want /team/internal", core.UserPathHeader, got)
	}
}
