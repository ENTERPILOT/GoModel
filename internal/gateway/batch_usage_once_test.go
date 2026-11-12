package gateway

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	batchstore "github.com/enterpilot/gomodel/internal/batch"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// countingUsageLogger counts the entries written to it, as the token and cost
// metrics tap in front of storage does.
type countingUsageLogger struct {
	mu      sync.Mutex
	entries []*usage.UsageEntry
}

func (l *countingUsageLogger) Write(entry *usage.UsageEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *countingUsageLogger) Config() usage.Config { return usage.Config{Enabled: true} }
func (l *countingUsageLogger) Close() error         { return nil }

func (l *countingUsageLogger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// failingUpdateStore loses every update, as a store that is briefly down
// after the results were fetched.
type failingUpdateStore struct {
	*batchstore.MemoryStore
}

func (s failingUpdateStore) Update(context.Context, *batchstore.StoredBatch) error {
	return errors.New("store unavailable")
}

func TestBatchResultsLogUsageOnce(t *testing.T) {
	tests := []struct {
		name       string
		store      func(*batchstore.MemoryStore) batchstore.Store
		concurrent bool
	}{
		{name: "concurrent fetches", store: func(m *batchstore.MemoryStore) batchstore.Store { return m }, concurrent: true},
		{name: "fetch after a failed update", store: func(m *batchstore.MemoryStore) batchstore.Store { return failingUpdateStore{m} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &countingUsageLogger{}
			orchestrator := NewBatchOrchestrator(BatchConfig{
				Provider:    &batchResultsProvider{},
				BatchStore:  tt.store(batchstore.NewMemoryStore()),
				UsageLogger: logger,
			})
			created, err := orchestrator.Create(context.Background(), &core.BatchRequest{
				InputFileID: "file-123",
				Endpoint:    "/v1/chat/completions",
				Metadata:    map[string]string{"provider": "openai"},
			}, BatchMeta{RequestID: "req-create", Endpoint: core.DescribeEndpoint(http.MethodPost, "/v1/batches")})
			require.NoError(t, err)

			fetch := func() {
				_, err := orchestrator.Results(context.Background(), created.Batch.ID, "req-results")
				assert.NoError(t, err)
			}
			if tt.concurrent {
				var wg sync.WaitGroup
				for range 8 {
					wg.Go(fetch)
				}
				wg.Wait()
			} else {
				fetch()
				fetch()
			}

			assert.Equal(t, 1, logger.count(), "the batch's usage entry was written more than once")
		})
	}
}
