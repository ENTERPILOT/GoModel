package usage

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockStore implements UsageStore for testing
type mockStore struct {
	entries []*UsageEntry
	mu      sync.Mutex
	closed  bool
}

func (m *mockStore) WriteBatch(ctx context.Context, entries []*UsageEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entries...)
	return nil
}

func (m *mockStore) Flush(ctx context.Context) error {
	return nil
}

func (m *mockStore) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockStore) getEntries() []*UsageEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*UsageEntry, len(m.entries))
	copy(result, m.entries)
	return result
}

func TestLogger(t *testing.T) {
	store := &mockStore{}
	cfg := Config{
		Enabled:       true,
		BufferSize:    100,
		FlushInterval: 100 * time.Millisecond,
	}

	logger := NewLogger(store, cfg)

	// Write some entries
	for i := range 5 {
		logger.Write(&UsageEntry{
			ID:           "test-" + string(rune('0'+i)),
			RequestID:    "req-" + string(rune('0'+i)),
			Model:        "gpt-4",
			Provider:     "openai",
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
		})
	}

	// Poll for entries with timeout
	timeout := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	var entries []*UsageEntry
	for {
		select {
		case <-timeout:
			t.Fatalf("timeout waiting for entries: expected 5, got %d", len(entries))
		case <-ticker.C:
			entries = store.getEntries()
			if len(entries) == 5 {
				goto entriesReady
			}
		}
	}
entriesReady:

	// Close logger
	if err := logger.Close(); err != nil {
		t.Errorf("logger close error: %v", err)
	}

	// Verify store was closed
	if !store.closed {
		t.Error("store should be closed")
	}
}

func TestLoggerClose(t *testing.T) {
	store := &mockStore{}
	cfg := Config{
		Enabled:       true,
		BufferSize:    1000,
		FlushInterval: 1 * time.Hour, // Long interval so flush is triggered by close
	}

	logger := NewLogger(store, cfg)

	// Write entries
	for i := range 10 {
		logger.Write(&UsageEntry{
			ID:        "test-" + string(rune('0'+i)),
			RequestID: "req-" + string(rune('0'+i)),
		})
	}

	// Close immediately - should flush pending entries
	if err := logger.Close(); err != nil {
		t.Errorf("logger close error: %v", err)
	}

	// Verify all entries were flushed
	entries := store.getEntries()
	if len(entries) != 10 {
		t.Errorf("expected 10 entries after close, got %d", len(entries))
	}
}

func TestLoggerCloseIdempotent(t *testing.T) {
	store := &mockStore{}
	cfg := Config{
		Enabled:       true,
		BufferSize:    100,
		FlushInterval: 1 * time.Hour,
	}

	logger := NewLogger(store, cfg)

	// Write an entry
	logger.Write(&UsageEntry{ID: "test-1", RequestID: "req-1"})

	// First close should succeed
	if err := logger.Close(); err != nil {
		t.Errorf("first close error: %v", err)
	}

	// Second close should not panic and should return nil
	if err := logger.Close(); err != nil {
		t.Errorf("second close error: %v", err)
	}

	// Third close for good measure
	if err := logger.Close(); err != nil {
		t.Errorf("third close error: %v", err)
	}

	// Verify entry was flushed only once
	entries := store.getEntries()
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

func TestNoopLogger(t *testing.T) {
	logger := &NoopLogger{}

	// Write should not panic
	logger.Write(&UsageEntry{ID: "test"})

	// Config should show disabled
	cfg := logger.Config()
	if cfg.Enabled {
		t.Error("NoopLogger should report disabled")
	}
	if !cfg.EnforceReturningUsageData {
		t.Error("NoopLogger should preserve default stream usage policy")
	}

	// Close should not error
	if err := logger.Close(); err != nil {
		t.Errorf("NoopLogger close error: %v", err)
	}
}

func TestNewNoopLogger_PreservesConfiguredUsagePolicy(t *testing.T) {
	logger := NewNoopLogger(Config{
		Enabled:                   true,
		EnforceReturningUsageData: false,
	})

	cfg := logger.Config()
	if cfg.Enabled {
		t.Error("NewNoopLogger should report disabled")
	}
	if cfg.EnforceReturningUsageData {
		t.Error("NewNoopLogger should preserve false enforcement setting")
	}
}

func TestLoggerBufferFull(t *testing.T) {
	store := &mockStore{}
	cfg := Config{
		Enabled:       true,
		BufferSize:    2, // Very small buffer
		FlushInterval: 1 * time.Hour,
	}

	logger := NewLogger(store, cfg)
	defer logger.Close()

	// Track dropped entries via atomic counter
	var written atomic.Int32

	// Try to write more than buffer size
	for i := range 10 {
		logger.Write(&UsageEntry{ID: "test-" + string(rune('0'+i))})
		written.Add(1)
	}

	// Some entries may be dropped
	// Just verify it doesn't panic/deadlock
}
