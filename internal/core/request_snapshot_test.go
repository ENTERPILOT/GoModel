package core

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
	got := snapshot.GetRouteParams()["provider"]
	require.Equal(t, "openai", got)
	got = snapshot.GetQueryParams()["limit"][0]
	require.Equal(t, "5", got)
	got = snapshot.GetHeaders()["X-Test"][0]
	require.Equal(t, "a", got)
	got = string(snapshot.CapturedBody())
	require.Equal(t, `{"model":"gpt-5-mini"}`, got)
	got = string(snapshot.CapturedBodyView())
	require.Equal(t, `{"model":"gpt-5-mini"}`, got)
	got = snapshot.GetTraceMetadata()["Traceparent"]
	require.Equal(t, "trace-1", got)
	got = snapshot.UserPath
	require.Equal(t, "/team/a", got)

	clonedHeaders := snapshot.GetHeaders()
	clonedHeaders["X-Test"][0] = "changed-again"
	got = snapshot.GetHeaders()["X-Test"][0]
	require.Equal(t, "a", got)

	view := snapshot.CapturedBodyView()
	require.NotEmpty(t, view)
	require.NotEmpty(t, snapshot.capturedBody)
	require.Same(t, &snapshot.capturedBody[0], &view[0])

	clonedBody := snapshot.CapturedBody()
	require.NotSame(t, &snapshot.capturedBody[0], &clonedBody[0])
}

func TestNewRequestSnapshotWithOwnedMaps_TakesOwnershipOfCapturedBytes(t *testing.T) {
	routeParams := map[string]string{"provider": "openai"}
	queryParams := map[string][]string{"limit": {"5"}}
	headers := map[string][]string{"X-Test": {"a"}}
	traceMetadata := map[string]string{"Traceparent": "trace-1"}
	rawBody := []byte(`{"model":"gpt-5-mini"}`)

	snapshot := NewRequestSnapshotWithOwnedMaps(
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

	view := snapshot.CapturedBodyView()
	require.NotEmpty(t, view)
	got := snapshot.UserPath
	require.Equal(t, "/team/a", got)
	require.Same(t, &rawBody[0], &view[0])

	clonedBody := snapshot.CapturedBody()
	require.NotSame(t, &rawBody[0], &clonedBody[0])

	// Route/query/trace maps are owned: mutating the caller's map is visible
	// through the snapshot (no defensive copy was taken at construction).
	routeParams["provider"] = "anthropic"
	got = snapshot.GetRouteParams()["provider"]
	require.Equal(t, "anthropic", got)

	queryParams["limit"] = []string{"9"}
	require.Equal(t, []string{"9"}, snapshot.GetQueryParams()["limit"], "query params not owned")
	traceMetadata["Traceparent"] = "trace-2"
	got = snapshot.GetTraceMetadata()["Traceparent"]
	require.Equal(t, "trace-2", got)

	// Headers are still defensively cloned: mutating the caller's map after
	// construction must not affect the snapshot.
	headers["X-Test"] = []string{"b"}
	require.Equal(t, []string{"a"}, snapshot.HeadersView()["X-Test"], "headers not cloned")
}

func BenchmarkNewRequestSnapshotClonedBody(b *testing.B) {
	body := []byte(`{"model":"gpt-5-mini","messages":[{"role":"user","content":"hello world"}],"response_format":{"type":"json_schema"}}`)

	b.ReportAllocs()
	for b.Loop() {
		_ = NewRequestSnapshot("POST", "/v1/chat/completions", nil, nil, nil, "application/json", body, false, "req-123", nil)
	}
}

func BenchmarkNewRequestSnapshotWithOwnedMaps(b *testing.B) {
	body := []byte(`{"model":"gpt-5-mini","messages":[{"role":"user","content":"hello world"}],"response_format":{"type":"json_schema"}}`)

	b.ReportAllocs()
	for b.Loop() {
		_ = NewRequestSnapshotWithOwnedMaps("POST", "/v1/chat/completions", nil, nil, nil, "application/json", body, false, "req-123", nil)
	}
}

func TestRequestSnapshotWithUserPath_RewritesCapturedHeader(t *testing.T) {
	snapshot := NewRequestSnapshot(
		"POST",
		"/v1/chat/completions",
		nil,
		nil,
		map[string][]string{UserPathHeader: {"/team/from-header"}},
		"application/json",
		nil,
		false,
		"req-123",
		nil,
		"/team/from-header",
	)

	updated := snapshot.WithUserPath("/team/from-auth-key")
	require.NotNil(t, updated)
	got := updated.UserPath
	require.Equal(t, "/team/from-auth-key", got)
	got = updated.GetHeaders()[UserPathHeader][0]
	require.Equal(t, "/team/from-auth-key", got)
	got = snapshot.UserPath
	require.Equal(t, "/team/from-header", got)
}
