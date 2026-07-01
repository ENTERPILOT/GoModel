package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
	"gomodel/internal/tagging"
)

func TestTaggingCaptureExtractsLabelsAndStripSet(t *testing.T) {
	service := tagging.NewService([]tagging.Rule{
		{Header: "X-Team", Prefix: "team-", Delimiter: ","},
		{Header: "X-Secret-Tag", DoNotPass: true, Delimiter: ","},
	}, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Team", "team-alpha, team-beta")
	req.Header.Set("X-Secret-Tag", "internal")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var gotLabels []string
	var gotStrip map[string]struct{}
	handler := TaggingCapture(service)(func(c *echo.Context) error {
		ctx := c.Request().Context()
		gotLabels = core.RequestLabelsFromContext(ctx)
		gotStrip = core.TaggingStripHeadersFromContext(ctx)
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}

	if want := []string{"alpha", "beta", "internal"}; !reflect.DeepEqual(gotLabels, want) {
		t.Fatalf("labels = %#v, want %#v", gotLabels, want)
	}
	if _, ok := gotStrip["X-Secret-Tag"]; !ok {
		t.Fatalf("strip set missing X-Secret-Tag: %#v", gotStrip)
	}
	// The tagged headers themselves stay on the inbound request; stripping
	// happens at the provider forwarding boundary.
	if c.Request().Header.Get("X-Secret-Tag") != "internal" {
		t.Fatal("inbound header must not be removed by the middleware")
	}
}

func TestTaggingCaptureNoRulesIsNoOp(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Team", "alpha")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	called := false
	handler := TaggingCapture(tagging.NewService(nil, nil))(func(c *echo.Context) error {
		called = true
		if labels := core.RequestLabelsFromContext(c.Request().Context()); labels != nil {
			t.Fatalf("labels = %#v, want nil", labels)
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if !called {
		t.Fatal("next handler not called")
	}
}

func TestBuildPassthroughHeadersStripsDoNotPassTaggingHeaders(t *testing.T) {
	strip := map[string]struct{}{"X-Secret-Tag": {}}
	ctx := core.WithTaggingStripHeaders(context.Background(), strip)

	headers := http.Header{}
	headers.Set("X-Secret-Tag", "internal")
	headers.Set("X-Team", "alpha")

	got := buildPassthroughHeaders(ctx, headers)
	if value := got.Get("X-Secret-Tag"); value != "" {
		t.Fatalf("X-Secret-Tag should not be forwarded, got %q", value)
	}
	if value := got.Get("X-Team"); value != "alpha" {
		t.Fatalf("X-Team = %q, want alpha (pass-through by default)", value)
	}
}
