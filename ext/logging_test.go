package ext

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type taggedHandler struct {
	slog.Handler
	tag string
}

func tagWith(tag string) func(slog.Handler) slog.Handler {
	return func(next slog.Handler) slog.Handler { return &taggedHandler{Handler: next, tag: tag} }
}

func TestRegistryLogHandlerWrappersKeepRegistrationOrder(t *testing.T) {
	reg := &Registry{}
	reg.WrapLogHandler(tagWith("first"))
	reg.WrapLogHandler(nil)
	reg.WrapLogHandler(tagWith("second"))

	got := reg.LogHandlerWrappers()
	require.Len(t, got, 2)
	base := slog.NewJSONHandler(io.Discard, nil)
	for i, want := range []string{"first", "second"} {
		wrapped, ok := got[i](base).(*taggedHandler)
		require.True(t, ok)
		assert.Equal(t, want, wrapped.tag)
		assert.Same(t, base, wrapped.Handler)
	}
}

func TestRegistryLogHandlerWrappersSnapshotIsIsolated(t *testing.T) {
	reg := &Registry{}
	assert.Empty(t, reg.LogHandlerWrappers())

	reg.WrapLogHandler(tagWith("one"))
	snapshot := reg.LogHandlerWrappers()
	snapshot[0] = nil
	reg.WrapLogHandler(tagWith("two"))

	require.Len(t, snapshot, 1)
	got := reg.LogHandlerWrappers()
	require.Len(t, got, 2)
	assert.NotNil(t, got[0])
}
