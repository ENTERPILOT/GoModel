package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

type stubLifecycleApp struct {
	startErr      error
	shutdownErr   error
	startCalls    int
	shutdownCalls int
	shutdownCtx   context.Context
	shutdownBlock <-chan struct{}
}

func (s *stubLifecycleApp) Start(_ context.Context, _ string) error {
	s.startCalls++
	return s.startErr
}

func (s *stubLifecycleApp) Shutdown(ctx context.Context) error {
	s.shutdownCalls++
	s.shutdownCtx = ctx
	if s.shutdownBlock != nil {
		<-s.shutdownBlock
	}
	return s.shutdownErr
}

func TestStartApplication_ShutsDownOnStartFailure(t *testing.T) {
	startErr := errors.New("listen tcp :8080: bind: address already in use")
	app := &stubLifecycleApp{startErr: startErr}

	err := startApplication(app, ":8080")
	if !errors.Is(err, startErr) {
		t.Fatalf("error = %v, want start error %v", err, startErr)
	}
	if app.startCalls != 1 {
		t.Fatalf("startCalls = %d, want 1", app.startCalls)
	}
	if app.shutdownCalls != 1 {
		t.Fatalf("shutdownCalls = %d, want 1", app.shutdownCalls)
	}
	if app.shutdownCtx == nil {
		t.Fatal("shutdown context was not captured")
	}
	deadline, ok := app.shutdownCtx.Deadline()
	if !ok {
		t.Fatal("shutdown context should have a deadline")
	}
	if time.Until(deadline) <= 0 {
		t.Fatal("shutdown context deadline should be in the future")
	}
}

func TestStartApplication_ReportsShutdownFailure(t *testing.T) {
	startErr := errors.New("listen failed")
	shutdownErr := errors.New("close failed")
	app := &stubLifecycleApp{
		startErr:    startErr,
		shutdownErr: shutdownErr,
	}

	err := startApplication(app, ":8080")
	if !errors.Is(err, startErr) {
		t.Fatalf("error = %v, want start error %v", err, startErr)
	}
	if !errors.Is(err, shutdownErr) {
		t.Fatalf("error = %v, want shutdown error %v", err, shutdownErr)
	}
	if app.shutdownCalls != 1 {
		t.Fatalf("shutdownCalls = %d, want 1", app.shutdownCalls)
	}
}

func TestStartApplication_DoesNotShutdownOnSuccess(t *testing.T) {
	app := &stubLifecycleApp{}

	if err := startApplication(app, ":8080"); err != nil {
		t.Fatalf("startApplication() error = %v, want nil", err)
	}
	if app.startCalls != 1 {
		t.Fatalf("startCalls = %d, want 1", app.startCalls)
	}
	if app.shutdownCalls != 0 {
		t.Fatalf("shutdownCalls = %d, want 0", app.shutdownCalls)
	}
}

func TestStartApplication_StopsWaitingWhenShutdownTimesOut(t *testing.T) {
	previousTimeout := shutdownTimeout
	shutdownTimeout = 10 * time.Millisecond
	defer func() {
		shutdownTimeout = previousTimeout
	}()

	startErr := errors.New("listen failed")
	shutdownBlock := make(chan struct{})
	defer close(shutdownBlock)

	app := &stubLifecycleApp{
		startErr:      startErr,
		shutdownBlock: shutdownBlock,
	}

	err := startApplication(app, ":8080")
	if !errors.Is(err, startErr) {
		t.Fatalf("error = %v, want start error %v", err, startErr)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
	if app.shutdownCalls != 1 {
		t.Fatalf("shutdownCalls = %d, want 1", app.shutdownCalls)
	}
}
