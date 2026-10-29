package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

type attemptAdmissionStub struct {
	releases int
	err      error
}

func (a *attemptAdmissionStub) Acquire(context.Context, string, string) (func(), error) {
	if a.err != nil {
		return func() {}, a.err
	}
	return func() { a.releases++ }, nil
}

func TestFailoverDoesNotHideAdmissionInfrastructureFailure(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "response", true: "stream"}[stream], func(t *testing.T) {
			o, workflow := failoverTestFixture()
			failure := errors.New("limiter failed")
			ctx := WithAttemptAdmission(context.Background(), &attemptAdmissionStub{err: failure})
			primaryErr := core.NewProviderError("openai", http.StatusTooManyRequests, "primary saturated", nil)
			called := false
			var err error
			if stream {
				_, _, err = tryFailoverStream(ctx, o, workflow, "primary", "openai", primaryErr, func(core.ModelSelector, string, string) (io.ReadCloser, string, string, error) {
					called = true
					return nil, "", "", nil
				})
			} else {
				_, _, err = tryFailoverResponse(ctx, o, workflow, "primary", "openai", primaryErr, nil, func(core.ModelSelector, string, string) (string, string, error) {
					called = true
					return "", "", nil
				})
			}
			require.ErrorIs(t, err, failure)
			require.False(t, called)
		})
	}
}

type cancelledAttemptReader struct{ ctx context.Context }

func (r cancelledAttemptReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func (r cancelledAttemptReader) Close() error { return nil }

func TestAttemptCapacityReleasedOnProviderPanic(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "response", true: "stream"}[stream], func(t *testing.T) {
			releases := 0
			release := func() { releases++ }
			require.Panics(t, func() {
				if stream {
					_, _, _, _ = callReservedStream(release, func() (io.ReadCloser, string, string, error) { panic("provider") })
				} else {
					_, _, _ = callReserved(release, func() (string, string, error) { panic("provider") })
				}
			})
			require.Equal(t, 1, releases)
		})
	}
}

func TestCanceledContextWithoutAdmissionKeepsExistingExecutorBehavior(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release, err := acquireAttempt(ctx, "provider", "model")
	require.NoError(t, err)
	release()
	admission := &attemptAdmissionStub{}
	_, err = acquireAttempt(WithAttemptAdmission(ctx, admission), "provider", "model")
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, admission.releases)
}

func TestAdmittedStreamReleasesAfterCancellation(t *testing.T) {
	admission := &attemptAdmissionStub{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithAttemptAdmission(ctx, admission)
	stream, err := callAdmittedStream(ctx, "provider", "model", func() (io.ReadCloser, error) {
		return cancelledAttemptReader{ctx}, nil
	})
	require.NoError(t, err)
	require.Zero(t, admission.releases)
	cancel()
	_, err = stream.Read(make([]byte, 1))
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, stream.Close())
	require.Equal(t, 1, admission.releases)
}

func TestAdmittedStreamRetainsCapacityUntilFinished(t *testing.T) {
	for _, closeEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "eof", true: "close"}[closeEarly], func(t *testing.T) {
			admission := &attemptAdmissionStub{}
			ctx := WithAttemptAdmission(context.Background(), admission)
			stream, err := callAdmittedStream(ctx, "provider", "model", func() (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("data")), nil
			})
			require.NoError(t, err)
			require.Zero(t, admission.releases)
			if !closeEarly {
				data, err := io.ReadAll(stream)
				require.NoError(t, err)
				require.Equal(t, "data", string(data))
			}
			require.NoError(t, stream.Close())
			require.NoError(t, stream.Close())
			require.Equal(t, 1, admission.releases)
		})
	}
}
