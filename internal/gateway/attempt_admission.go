package gateway

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/enterpilot/gomodel/internal/core"
)

// AttemptAdmission reserves capacity immediately before a provider call.
type AttemptAdmission interface {
	Acquire(context.Context, string, string) (func(), error)
}

type attemptAdmissionKey struct{}

func stopsFailover(err error) bool {
	var rejection interface{ StopFailover() bool }
	return errors.As(err, &rejection) && rejection.StopFailover()
}

func routeAdmissionRejected(err error) bool {
	var rejection interface{ RouteRejected() bool }
	return errors.As(err, &rejection) && rejection.RouteRejected()
}

// WithAttemptAdmission installs request-scoped admission for shared execution.
func WithAttemptAdmission(ctx context.Context, admission AttemptAdmission) context.Context {
	return context.WithValue(ctx, attemptAdmissionKey{}, admission)
}

func acquireAttempt(ctx context.Context, provider, model string) (func(), error) {
	admission, ok := ctx.Value(attemptAdmissionKey{}).(AttemptAdmission)
	if !ok {
		return func() {}, nil
	}
	if err := ctx.Err(); err != nil {
		return func() {}, err
	}
	return admission.Acquire(ctx, provider, model)
}

type admittedStream struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func callAdmittedStream(ctx context.Context, provider, model string, call func() (io.ReadCloser, error)) (io.ReadCloser, error) {
	release, err := acquireAttempt(ctx, provider, model)
	if err != nil {
		return nil, err
	}
	stream, _, _, err := callReservedStream(release, func() (io.ReadCloser, string, string, error) {
		stream, err := call()
		return stream, "", "", err
	})
	return stream, err
}

func callReserved[T any](release func(), call func() (T, string, error)) (response T, provider string, err error) {
	defer func() { finishAttempt(response, err, release) }()
	return call()
}

func callReservedStream(release func(), call func() (io.ReadCloser, string, string, error)) (stream io.ReadCloser, provider, model string, err error) {
	transferred := false
	defer func() {
		if !transferred {
			release()
		}
	}()
	stream, provider, model, err = call()
	if err == nil && stream != nil {
		stream = &admittedStream{ReadCloser: stream, release: release}
		transferred = true
	}
	return
}

func (s *admittedStream) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	if err != nil {
		s.once.Do(s.release)
	}
	return n, err
}

func (s *admittedStream) Close() error {
	defer s.once.Do(s.release)
	return s.ReadCloser.Close()
}

func finishAttempt(response any, err error, release func()) {
	if passthrough, ok := response.(*core.PassthroughResponse); ok && err == nil && passthrough != nil && passthrough.Body != nil {
		passthrough.Body = &admittedStream{ReadCloser: passthrough.Body, release: release}
		return
	}
	release()
}
