package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestBuildPassthroughHeadersSkipsConfiguredUserPathHeader(t *testing.T) {
	ctx := core.WithUserPathHeaderName(context.Background(), "X-Tenant-Path")
	headers := http.Header{}
	headers.Set("X-Tenant-Path", "/team/alpha")
	headers.Set(core.UserPathHeader, "/team/default")
	headers.Set("OpenAI-Beta", "responses=v1")

	got := buildPassthroughHeaders(ctx, headers)
	if value := got.Get("X-Tenant-Path"); value != "" {
		t.Fatalf("X-Tenant-Path should not be forwarded, got %q", value)
	}
	if value := got.Get(core.UserPathHeader); value != "" {
		t.Fatalf("%s should not be forwarded, got %q", core.UserPathHeader, value)
	}
	if value := got.Get("OpenAI-Beta"); value != "responses=v1" {
		t.Fatalf("OpenAI-Beta = %q, want responses=v1", value)
	}
}
