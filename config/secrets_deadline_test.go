package config

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A reference whose backend never answers fails once secretResolveTimeout
// passes, so it cannot wedge startup, a reload, or an admin save.
func TestResolveGivesUpAfterTheResolveTimeout(t *testing.T) {
	previous := secretResolveTimeout
	secretResolveTimeout = 50 * time.Millisecond
	t.Cleanup(func() { secretResolveTimeout = previous })

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	secrets := NewSecrets()
	require.NoError(t, secrets.Register("slow", SecretResolverFunc(func(ctx context.Context, _ string) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-release:
			return "late", nil
		}
	})))

	done := make(chan error, 1)
	go func() {
		_, err := secrets.Resolve(context.Background(), "${slow:key}")
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(5 * time.Second):
		t.Fatal("resolution did not give up after the timeout")
	}
}

// A file read that blocks in the kernel, as on a stalled network mount,
// cannot observe its context; the caller stops waiting for it instead, and
// later callers share the blocked read rather than start another.
func TestCancellableResolverReturnsWhenItsContextEnds(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var calls atomic.Int32
	resolve := cancellable(func(context.Context, string) (string, error) {
		calls.Add(1)
		<-release // ignores its context
		return "late", nil
	})

	for range 3 {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		done := make(chan error, 1)
		go func() {
			_, err := resolve(ctx, "/mnt/stalled/key")
			done <- err
		}()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.DeadlineExceeded)
		case <-time.After(5 * time.Second):
			t.Fatal("the resolver call was waited on past its context")
		}
		cancel()
	}
	assert.Equal(t, int32(1), calls.Load(), "callers share the blocked read")

	value, err := cancellable(func(context.Context, string) (string, error) { return "v", nil })(t.Context(), "x")
	require.NoError(t, err)
	assert.Equal(t, "v", value)
}
