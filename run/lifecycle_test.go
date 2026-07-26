package run

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/providers"
)

type stubLifecycleApp struct {
	mu            sync.Mutex
	startErr      error
	shutdownErr   error
	startCalls    int
	shutdownCalls int
	shutdownCtx   context.Context
	shutdownBlock <-chan struct{}
}

func (s *stubLifecycleApp) Start(_ context.Context, _ string) error {
	s.mu.Lock()
	s.startCalls++
	s.mu.Unlock()
	return s.startErr
}

func (s *stubLifecycleApp) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.shutdownCalls++
	s.shutdownCtx = ctx
	s.mu.Unlock()
	if s.shutdownBlock != nil {
		<-s.shutdownBlock
	}
	return s.shutdownErr
}

func (s *stubLifecycleApp) startCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startCalls
}

func (s *stubLifecycleApp) shutdownCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shutdownCalls
}

func (s *stubLifecycleApp) capturedShutdownContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shutdownCtx
}

func TestStartApplication_ShutsDownOnStartFailure(t *testing.T) {
	startErr := errors.New("listen tcp :8080: bind: address already in use")
	app := &stubLifecycleApp{startErr: startErr}

	err := startApplication(app, ":8080")
	if !errors.Is(err, startErr) {
		t.Fatalf("error = %v, want start error %v", err, startErr)
	}
	if calls := app.startCallCount(); calls != 1 {
		t.Fatalf("startCalls = %d, want 1", calls)
	}
	if calls := app.shutdownCallCount(); calls != 1 {
		t.Fatalf("shutdownCalls = %d, want 1", calls)
	}
	shutdownCtx := app.capturedShutdownContext()
	if shutdownCtx == nil {
		t.Fatal("shutdown context was not captured")
	}
	deadline, ok := shutdownCtx.Deadline()
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
	if calls := app.shutdownCallCount(); calls != 1 {
		t.Fatalf("shutdownCalls = %d, want 1", calls)
	}
}

func TestStartApplication_DoesNotShutdownOnSuccess(t *testing.T) {
	app := &stubLifecycleApp{}

	if err := startApplication(app, ":8080"); err != nil {
		t.Fatalf("startApplication() error = %v, want nil", err)
	}
	if calls := app.startCallCount(); calls != 1 {
		t.Fatalf("startCalls = %d, want 1", calls)
	}
	if calls := app.shutdownCallCount(); calls != 0 {
		t.Fatalf("shutdownCalls = %d, want 0", calls)
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
	if calls := app.shutdownCallCount(); calls != 1 {
		t.Fatalf("shutdownCalls = %d, want 1", calls)
	}
}

// servingApp mirrors the ordering that matters in the real App: Start blocks
// until Shutdown stops the server, and Shutdown keeps working afterwards —
// flushing buffered usage and audit records, closing the database — before it
// returns.
type servingApp struct {
	serverStopped chan struct{} // closed by Shutdown, releases Start
	flushing      chan struct{} // closed by the test, releases Shutdown
	shutdownDone  atomic.Bool
}

func newServingApp() *servingApp {
	return &servingApp{
		serverStopped: make(chan struct{}),
		flushing:      make(chan struct{}),
	}
}

func (a *servingApp) Start(context.Context, string) error {
	<-a.serverStopped
	return nil
}

func (a *servingApp) Shutdown(context.Context) error {
	close(a.serverStopped)
	<-a.flushing
	a.shutdownDone.Store(true)
	return nil
}

// Run returns straight into process exit, so returning while Shutdown is still
// flushing loses whatever it had not written yet. That is what happened on
// every Ctrl+C: the server stopped, Start returned, the process left, and
// "application shutdown complete" was never reached.
func TestServeUntilShutdown_WaitsForTeardownToFinish(t *testing.T) {
	app := newServingApp()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	returned := make(chan error, 1)
	go func() {
		returned <- serveUntilShutdown(ctx, app, ":0")
	}()

	cancel() // the SIGINT equivalent

	// Start has returned by now; Shutdown is still flushing.
	select {
	case err := <-returned:
		t.Fatalf("serveUntilShutdown returned mid-teardown (error = %v)", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(app.flushing)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("serveUntilShutdown() error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveUntilShutdown did not return after teardown finished")
	}
	if !app.shutdownDone.Load() {
		t.Fatal("teardown did not run to completion")
	}
}

// A server that stops without a signal still owns a database handle and
// buffered records, so it gets the same teardown.
func TestServeUntilShutdown_TearsDownWhenServerStopsOnItsOwn(t *testing.T) {
	app := &stubLifecycleApp{}

	if err := serveUntilShutdown(context.Background(), app, ":0"); err != nil {
		t.Fatalf("serveUntilShutdown() error = %v, want nil", err)
	}
	if calls := app.shutdownCallCount(); calls != 1 {
		t.Fatalf("shutdownCalls = %d, want 1", calls)
	}
}

func TestServeUntilShutdown_ReturnsStartFailure(t *testing.T) {
	startErr := errors.New("listen tcp :8080: bind: address already in use")
	app := &stubLifecycleApp{startErr: startErr}

	if err := serveUntilShutdown(context.Background(), app, ":8080"); !errors.Is(err, startErr) {
		t.Fatalf("serveUntilShutdown() error = %v, want start error %v", err, startErr)
	}
}

func TestMain_KimicodeProviderRegistration(t *testing.T) {
	factory := defaultProviderFactory(&config.Config{})

	registered := factory.RegisteredTypes()
	found := slices.Contains(registered, "kimicode")
	if !found {
		t.Fatalf("kimicode not in RegisteredTypes() = %v", registered)
	}

	provider, err := factory.Create(providers.ProviderConfig{Type: "kimicode", APIKey: "test"})
	if err != nil {
		t.Fatalf("factory.Create(kimicode) error = %v, want nil", err)
	}
	if provider == nil {
		t.Fatal("factory.Create(kimicode) returned nil provider")
	}
}
