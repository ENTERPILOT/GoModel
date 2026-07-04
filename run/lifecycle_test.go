package run

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"gomodel/internal/providers"
	"gomodel/internal/providers/kimi"
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

func TestMain_SetUserPathHeaderWiring(t *testing.T) {
	// Verify SetUserPathHeader exists and can be called without panic
	factory := providers.NewProviderFactory()
	
	// Test that SetUserPathHeader can be called with various valid headers
	headers := []string{
		"X-GoModel-User-Path",
		"X-Tenant-Path",
		"X-Custom-Header",
		"", // empty string should not panic
	}
	
	for _, header := range headers {
		didPanic := false
		func() {
			defer func() {
				if r := recover(); r != nil {
					didPanic = true
					t.Errorf("SetUserPathHeader(%q) panicked: %v", header, r)
				}
			}()
			factory.SetUserPathHeader(header)
		}()
		if didPanic {
			continue
		}
	}
}

func TestMain_KimiProviderRegistration(t *testing.T) {
	// Mirrors the wiring done in main.go: registering kimi.Registration with a
	// provider factory must succeed at startup without panicking. This is the
	// unit-level equivalent of the runtime path that emits
	// "provider registered" name=kimi type=kimi via initializeProviders.
	factory := providers.NewProviderFactory()

	didPanic := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				didPanic = true
				t.Errorf("factory.Add(kimi.Registration) panicked: %v", r)
			}
		}()
		factory.Add(kimi.Registration)
	}()
	if didPanic {
		return
	}

	// The factory must report kimi among its registered types.
	registered := false
	for _, t2 := range factory.RegisteredTypes() {
		if t2 == "kimi" {
			registered = true
			break
		}
	}
	if !registered {
		t.Fatalf("RegisteredTypes() = %v, want contains \"kimi\"", factory.RegisteredTypes())
	}

	// The factory must also be able to instantiate a Kimi provider using the
	// same shape that initializeProviders uses (factory.Create with type=kimi).
	provider, err := factory.Create(providers.ProviderConfig{
		Type:   "kimi",
		APIKey: "kimi-test-key",
	})
	if err != nil {
		t.Fatalf("factory.Create() error = %v", err)
	}
	if provider == nil {
		t.Fatal("factory.Create() returned nil provider")
	}
}
