package usage

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStore implements UsageStore for testing
type mockStore struct {
	entries  []*UsageEntry
	writeErr error
	mu       sync.Mutex
	closed   bool
}

func (m *mockStore) WriteBatch(ctx context.Context, entries []*UsageEntry) error {
	if m.writeErr != nil {
		return m.writeErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entries...)
	return nil
}

type capturedUsageLiveEvent struct {
	eventType string
	entry     *UsageEntry
}

type capturingUsageLivePublisher struct {
	mu     sync.Mutex
	events []capturedUsageLiveEvent
}

func (p *capturingUsageLivePublisher) PublishUsageEvent(eventType string, entry *UsageEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, capturedUsageLiveEvent{eventType: eventType, entry: entry})
}

func (p *capturingUsageLivePublisher) snapshot() []capturedUsageLiveEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	events := make([]capturedUsageLiveEvent, len(p.events))
	copy(events, p.events)
	return events
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

	assert.NoError(t, logger.Close())

	// Verify store was closed
	assert.True(t, store.closed)
}

func TestLoggerFlushBatchPublishesFailedLiveEvent(t *testing.T) {
	entry := &UsageEntry{ID: "usage-1", RequestID: "req-1"}
	publisher := &capturingUsageLivePublisher{}
	logger := &Logger{store: &mockStore{writeErr: errors.New("write failed")}}
	logger.SetLivePublisher(publisher)

	logger.flushBatch([]*UsageEntry{entry})

	events := publisher.snapshot()
	require.Len(t, events, 1)
	require.Equal(t, LiveEventUsageFailed, events[0].eventType)
	require.Equal(t, entry, events[0].entry)
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
	assert.NoError(t, logger.Close())

	// Verify all entries were flushed
	entries := store.getEntries()
	assert.Len(t, entries, 10)
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
	assert.NoError(t, logger.Close())
	// Second close should not panic and should return nil
	assert.NoError(t, logger.Close())
	// Third close for good measure
	assert.NoError(t, logger.Close())

	// Verify entry was flushed only once
	entries := store.getEntries()
	assert.Len(t, entries, 1)
}

func TestNoopLogger(t *testing.T) {
	logger := &NoopLogger{}

	// Write should not panic
	logger.Write(&UsageEntry{ID: "test"})

	// Config should show disabled
	cfg := logger.Config()
	assert.False(t, cfg.Enabled)
	assert.True(t, cfg.EnforceReturningUsageData)
	// Close should not error
	assert.NoError(t, logger.Close())
}

func TestNewNoopLogger_PreservesConfiguredUsagePolicy(t *testing.T) {
	logger := NewNoopLogger(Config{
		Enabled:                   true,
		EnforceReturningUsageData: false,
	})

	cfg := logger.Config()
	assert.False(t, cfg.Enabled)
	assert.False(t, cfg.EnforceReturningUsageData)
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
