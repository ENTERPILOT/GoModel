package run

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/server"
	"github.com/stretchr/testify/require"
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

func (s *stubLifecycleApp) StartWithListener(_ context.Context, _ net.Listener) error {
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

// A server that never came up still holds a database handle and whatever the
// loggers buffered while it was being built, so it gets torn down — once, on
// one shutdownTimeout budget, from the same place every other exit uses.
func TestServeGeneration_TearsDownOnceAfterAFailedStart(t *testing.T) {
	startErr := errors.New("listen tcp :8080: bind: address already in use")
	app := &stubLifecycleApp{startErr: startErr}

	err := serveGeneration(context.Background(), app, nil)
	require.ErrorIs(t, err, startErr)
	calls := app.startCallCount()
	require.Equal(t, 1, calls)
	calls = app.shutdownCallCount()
	require.Equal(t, 1, calls)

	shutdownCtx := app.capturedShutdownContext()
	require.NotNil(t, shutdownCtx)

	deadline, ok := shutdownCtx.Deadline()
	require.True(t, ok)
	require.Greater(t, time.Until(deadline), time.Duration(0))
}

// The start error is what the operator needs to see and what sets the exit
// code, so a teardown that also fails is logged rather than wrapped around it.
func TestServeGeneration_ShutdownFailureDoesNotMaskTheStartError(t *testing.T) {
	startErr := errors.New("listen failed")
	app := &stubLifecycleApp{startErr: startErr, shutdownErr: errors.New("close failed")}

	err := serveGeneration(context.Background(), app, nil)
	require.ErrorIs(t, err, startErr)
	calls := app.shutdownCallCount()
	require.Equal(t, 1, calls)
}

// A teardown that wedges must not wedge the process with it: the wait is
// bounded by shutdownTimeout and serveGeneration returns regardless.
func TestServeGeneration_StopsWaitingWhenShutdownTimesOut(t *testing.T) {
	previousTimeout := shutdownTimeout
	shutdownTimeout = 10 * time.Millisecond
	defer func() {
		shutdownTimeout = previousTimeout
	}()

	startErr := errors.New("listen failed")
	shutdownBlock := make(chan struct{})
	defer close(shutdownBlock)

	app := &stubLifecycleApp{startErr: startErr, shutdownBlock: shutdownBlock}

	done := make(chan error, 1)
	go func() {
		done <- serveGeneration(context.Background(), app, nil)
	}()

	select {
	case err := <-done:
		require.ErrorIs(t, err, startErr)

	case <-time.After(5 * time.Second):
		t.Fatal("serveGeneration blocked on a shutdown that never returned")
	}
	calls := app.shutdownCallCount()
	require.Equal(t, 1, calls)
}

// The drain window and the shutdown budget live in different packages, so the
// comment tying them together is only as good as this check: the budget has to
// cover the drain plus the usage and audit flushes that follow it.
func TestGracefulDrainFitsInsideTheShutdownBudget(t *testing.T) {
	require.Less(t, server.GracefulDrainTimeout, shutdownTimeout)
	headroom := shutdownTimeout - server.GracefulDrainTimeout
	require.GreaterOrEqual(t, headroom, 5*time.Second)
}

// servingApp mirrors the ordering that matters in the real App:
// StartWithListener blocks until Shutdown stops the server, and Shutdown keeps
// working afterwards — flushing buffered usage and audit records, closing the
// database — before it returns.
type servingApp struct {
	serverStopped chan struct{} // closed by Shutdown, releases StartWithListener
	flushing      chan struct{} // closed by the test, releases Shutdown
	shutdownDone  atomic.Bool
}

func newServingApp() *servingApp {
	return &servingApp{
		serverStopped: make(chan struct{}),
		flushing:      make(chan struct{}),
	}
}

func (a *servingApp) StartWithListener(context.Context, net.Listener) error {
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
func TestServeGeneration_WaitsForTeardownToFinish(t *testing.T) {
	app := newServingApp()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	returned := make(chan error, 1)
	go func() {
		returned <- serveGeneration(ctx, app, nil)
	}()

	cancel() // the SIGINT equivalent

	// Start has returned by now; Shutdown is still flushing.
	select {
	case err := <-returned:
		t.Fatalf("serveGeneration returned mid-teardown (error = %v)", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(app.flushing)
	select {
	case err := <-returned:
		require.NoError(t, err)

	case <-time.After(5 * time.Second):
		t.Fatal("serveGeneration did not return after teardown finished")
	}
	require.True(t, app.shutdownDone.Load())
}

// A server that stops without a signal still owns a database handle and
// buffered records, so it gets the same teardown.
func TestServeGeneration_TearsDownWhenServerStopsOnItsOwn(t *testing.T) {
	app := &stubLifecycleApp{}
	err := serveGeneration(context.Background(), app, nil)
	require.NoError(t, err)
	calls := app.shutdownCallCount()
	require.Equal(t, 1, calls)
}

func TestServeGeneration_ReturnsStartFailure(t *testing.T) {
	startErr := errors.New("listen tcp :8080: bind: address already in use")
	app := &stubLifecycleApp{startErr: startErr}
	err := serveGeneration(context.Background(), app, nil)
	require.ErrorIs(t, err, startErr)
}

func TestMain_KimicodeProviderRegistration(t *testing.T) {
	factory := defaultProviderFactory(&config.Config{})

	registered := factory.RegisteredTypes()
	found := slices.Contains(registered, "kimicode")
	require.True(t, found, "kimicode not in RegisteredTypes() = %v", registered)

	provider, err := factory.Create(providers.ProviderConfig{Type: "kimicode", APIKey: "test"})
	require.NoError(t, err)
	require.NotNil(t, provider)
}

func TestMain_EdenAIProviderRegistration(t *testing.T) {
	factory := defaultProviderFactory(&config.Config{})

	registered := factory.RegisteredTypes()
	found := slices.Contains(registered, "edenai")
	if !found {
		t.Fatalf("edenai not in RegisteredTypes() = %v", registered)
	}

	provider, err := factory.Create(providers.ProviderConfig{Type: "edenai", APIKey: "test"})
	if err != nil {
		t.Fatalf("factory.Create(edenai) error = %v, want nil", err)
	}
	if provider == nil {
		t.Fatal("factory.Create(edenai) returned nil provider")
	}
}

func TestMain_HetznerProviderRegistration(t *testing.T) {
	factory := defaultProviderFactory(&config.Config{})

	registered := factory.RegisteredTypes()
	found := slices.Contains(registered, "hetzner")
	require.True(t, found, "hetzner not in RegisteredTypes() = %v", registered)

	provider, err := factory.Create(providers.ProviderConfig{Type: "hetzner", APIKey: "test"})
	require.NoError(t, err)
	require.NotNil(t, provider)
}
