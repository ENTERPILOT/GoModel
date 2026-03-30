package core

import "testing"

func TestNewRequestSnapshot_DefensivelyCopiesMutableFields(t *testing.T) {
	routeParams := map[string]string{"provider": "openai"}
	queryParams := map[string][]string{"limit": {"5"}}
	headers := map[string][]string{"X-Test": {"a", "b"}}
	rawBody := []byte(`{"model":"gpt-5-mini"}`)
	traceMetadata := map[string]string{"Traceparent": "trace-1"}

	snapshot := NewRequestSnapshot(
		"POST",
		"/v1/chat/completions",
		routeParams,
		queryParams,
		headers,
		"application/json",
		rawBody,
		false,
		"req-123",
		traceMetadata,
		"/team/a",
	)

	routeParams["provider"] = "anthropic"
	queryParams["limit"][0] = "99"
	headers["X-Test"][0] = "mutated"
	rawBody[0] = '['
	traceMetadata["Traceparent"] = "trace-2"

	if got := snapshot.GetRouteParams()["provider"]; got != "openai" {
		t.Fatalf("GetRouteParams provider = %q, want openai", got)
	}
	if got := snapshot.GetQueryParams()["limit"][0]; got != "5" {
		t.Fatalf("GetQueryParams limit = %q, want 5", got)
	}
	if got := snapshot.GetHeaders()["X-Test"][0]; got != "a" {
		t.Fatalf("GetHeaders X-Test = %q, want a", got)
	}
	if got := string(snapshot.CapturedBody()); got != `{"model":"gpt-5-mini"}` {
		t.Fatalf("CapturedBody = %q, want original body", got)
	}
	if got := string(snapshot.CapturedBodyView()); got != `{"model":"gpt-5-mini"}` {
		t.Fatalf("CapturedBodyView = %q, want original body", got)
	}
	if got := snapshot.GetTraceMetadata()["Traceparent"]; got != "trace-1" {
		t.Fatalf("GetTraceMetadata Traceparent = %q, want trace-1", got)
	}
	if got := snapshot.UserPath; got != "/team/a" {
		t.Fatalf("UserPath = %q, want /team/a", got)
	}

	clonedHeaders := snapshot.GetHeaders()
	clonedHeaders["X-Test"][0] = "changed-again"
	if got := snapshot.GetHeaders()["X-Test"][0]; got != "a" {
		t.Fatalf("GetHeaders returned mutable state, got %q", got)
	}

	view := snapshot.CapturedBodyView()
	if len(view) == 0 || len(snapshot.capturedBody) == 0 {
		t.Fatal("captured body unexpectedly empty")
	}
	if &view[0] != &snapshot.capturedBody[0] {
		t.Fatal("CapturedBodyView did not return the underlying snapshot bytes")
	}

	clonedBody := snapshot.CapturedBody()
	if &clonedBody[0] == &snapshot.capturedBody[0] {
		t.Fatal("CapturedBody returned underlying snapshot bytes, want defensive copy")
	}
}

func TestNewRequestSnapshotWithOwnedBody_TakesOwnershipOfCapturedBytes(t *testing.T) {
	rawBody := []byte(`{"model":"gpt-5-mini"}`)

	snapshot := NewRequestSnapshotWithOwnedBody(
		"POST",
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		rawBody,
		false,
		"req-123",
		nil,
		"/team/a",
	)

	view := snapshot.CapturedBodyView()
	if len(view) == 0 {
		t.Fatal("captured body unexpectedly empty")
	}
	if &view[0] != &rawBody[0] {
		t.Fatal("snapshot did not take ownership of the captured body bytes")
	}

	clonedBody := snapshot.CapturedBody()
	if &clonedBody[0] == &rawBody[0] {
		t.Fatal("CapturedBody returned owned bytes directly, want defensive copy")
	}
}

func BenchmarkNewRequestSnapshotClonedBody(b *testing.B) {
	body := []byte(`{"model":"gpt-5-mini","messages":[{"role":"user","content":"hello world"}],"response_format":{"type":"json_schema"}}`)

	b.ReportAllocs()
	for b.Loop() {
		_ = NewRequestSnapshot("POST", "/v1/chat/completions", nil, nil, nil, "application/json", body, false, "req-123", nil)
	}
}

func BenchmarkNewRequestSnapshotWithOwnedBody(b *testing.B) {
	body := []byte(`{"model":"gpt-5-mini","messages":[{"role":"user","content":"hello world"}],"response_format":{"type":"json_schema"}}`)

	b.ReportAllocs()
	for b.Loop() {
		_ = NewRequestSnapshotWithOwnedBody("POST", "/v1/chat/completions", nil, nil, nil, "application/json", body, false, "req-123", nil)
	}
}
