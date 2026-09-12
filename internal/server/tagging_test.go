package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/tagging"
)

func TestTaggingCaptureExtractsLabelsAndStripSet(t *testing.T) {
	service := tagging.NewService([]tagging.Rule{
		{Header: "X-Team", Prefix: "team-", Delimiter: ","},
		{Header: "X-Secret-Tag", DoNotPass: true, Delimiter: ","},
	}, nil)

	c, _ := echotest.Post(t, "/v1/chat/completions", nil,
		echotest.WithHeader("X-Team", "team-alpha, team-beta"),
		echotest.WithHeader("X-Secret-Tag", "internal"))

	var gotLabels []string
	var gotStrip map[string]struct{}
	handler := TaggingCapture(service)(func(c *echo.Context) error {
		ctx := c.Request().Context()
		gotLabels = core.RequestLabelsFromContext(ctx)
		gotStrip = core.TaggingStripHeadersFromContext(ctx)
		return nil
	})
	err := handler(c)
	require.NoError(t, err)
	want := []string{"alpha", "beta", "internal"}
	require.Equal(t, want, gotLabels)
	_, ok := gotStrip["X-Secret-Tag"]
	require.True(t, ok, "strip set missing X-Secret-Tag: %#v", gotStrip)

	// The tagged headers themselves stay on the inbound request; stripping
	// happens at the provider forwarding boundary.
	require.Equal(t, "internal", c.Request().Header.Get("X-Secret-Tag"))
}

func TestTaggingCaptureNoRulesIsNoOp(t *testing.T) {
	c, _ := echotest.Post(t, "/v1/chat/completions", nil, echotest.WithHeader("X-Team", "alpha"))

	called := false
	handler := TaggingCapture(tagging.NewService(nil, nil))(func(c *echo.Context) error {
		called = true
		labels := core.RequestLabelsFromContext(c.Request().Context())
		require.Nil(t, labels)

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
	require.True(t, called)
}

func TestBuildPassthroughHeadersStripsDoNotPassTaggingHeaders(t *testing.T) {
	strip := map[string]struct{}{"X-Secret-Tag": {}}
	ctx := core.WithTaggingStripHeaders(context.Background(), strip)

	headers := http.Header{}
	headers.Set("X-Secret-Tag", "internal")
	headers.Set("X-Team", "alpha")

	got := buildPassthroughHeaders(ctx, headers)
	value := got.Get("X-Secret-Tag")
	require.Empty(t, value)
	value = got.Get("X-Team")
	require.Equal(t, "alpha", value)
}
