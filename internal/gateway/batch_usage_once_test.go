package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	batchstore "github.com/enterpilot/gomodel/internal/batch"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// countingUsageLogger counts the entries written to it, as the token and cost
// metrics tap in front of storage does. With stopAfter set it panics once
// that many entries were written, as a process that stops mid-write.
type countingUsageLogger struct {
	mu        sync.Mutex
	entries   []*usage.UsageEntry
	stopAfter *int
}

func (l *countingUsageLogger) Write(entry *usage.UsageEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopAfter != nil && len(l.entries) == *l.stopAfter {
		panic("process stopped")
	}
	l.entries = append(l.entries, entry)
}

func (l *countingUsageLogger) Config() usage.Config { return usage.Config{Enabled: true} }
func (l *countingUsageLogger) Close() error         { return nil }

func (l *countingUsageLogger) count() int {
	return len(l.ids())
}

func (l *countingUsageLogger) ids() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	ids := make([]string, 0, len(l.entries))
	for _, entry := range l.entries {
		ids = append(ids, entry.ID)
	}
	return ids
}

// failingUpdateStore loses every update, as a store that is briefly down
// after the results were fetched.
type failingUpdateStore struct {
	*batchstore.MemoryStore
}

func (s failingUpdateStore) Update(context.Context, *batchstore.StoredBatch) error {
	return errors.New("store unavailable")
}

// failingClaimStore cannot claim or mark usage, as a store that is down while
// the results are fetched.
type failingClaimStore struct {
	*batchstore.MemoryStore
}

func (s failingClaimStore) ClaimUsage(context.Context, string, time.Time, time.Duration) (batchstore.UsageClaim, error) {
	return batchstore.UsagePending, errors.New("store unavailable")
}

func (s failingClaimStore) CompleteUsage(context.Context, string, time.Time) error {
	return errors.New("store unavailable")
}

// batchItemsProvider answers batch results with the given number of billable
// items. Its status calls report a completed batch once block, when set, is
// closed; entered is signalled when such a call starts waiting.
type batchItemsProvider struct {
	batchBudgetProvider
	items   int
	entered chan struct{}
	block   chan struct{}
}

func (p *batchItemsProvider) GetBatchResults(context.Context, string, string) (*core.BatchResultsResponse, error) {
	result := &core.BatchResultsResponse{Object: "list"}
	for i := range p.items {
		result.Data = append(result.Data, core.BatchResultItem{
			Index:      i,
			StatusCode: 200,
			Model:      "gpt-4o-mini",
			Provider:   "openai",
			Response: map[string]any{
				"id":    fmt.Sprintf("resp-%d", i),
				"model": "gpt-4o-mini",
				"usage": map[string]any{"input_tokens": float64(10), "output_tokens": float64(5), "total_tokens": float64(15)},
			},
		})
	}
	return result, nil
}

func (p *batchItemsProvider) refreshed() (*core.BatchResponse, error) {
	if p.block != nil {
		p.entered <- struct{}{}
		<-p.block
	}
	return &core.BatchResponse{Status: "completed"}, nil
}

func (p *batchItemsProvider) GetBatch(context.Context, string, string) (*core.BatchResponse, error) {
	return p.refreshed()
}

func (p *batchItemsProvider) CancelBatch(context.Context, string, string) (*core.BatchResponse, error) {
	return p.refreshed()
}

func newUsageOrchestrator(provider core.RoutableProvider, store batchstore.Store, logger usage.LoggerInterface) *BatchOrchestrator {
	return NewBatchOrchestrator(BatchConfig{Provider: provider, BatchStore: store, UsageLogger: logger})
}

func createUsageBatch(t *testing.T, orchestrator *BatchOrchestrator) string {
	t.Helper()
	created, err := orchestrator.Create(context.Background(), &core.BatchRequest{
		InputFileID: "file-123",
		Endpoint:    "/v1/chat/completions",
		Metadata:    map[string]string{"provider": "openai"},
	}, BatchMeta{RequestID: "req-create", Endpoint: core.DescribeEndpoint(http.MethodPost, "/v1/batches")})
	require.NoError(t, err)
	return created.Batch.ID
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
			orchestrator := newUsageOrchestrator(&batchItemsProvider{items: 1}, tt.store(batchstore.NewMemoryStore()), logger)
			id := createUsageBatch(t, orchestrator)

			fetch := func() {
				_, err := orchestrator.Results(context.Background(), id, "req-results")
				assert.NoError(t, err)
			}
			if tt.concurrent {
				start := make(chan struct{})
				var wg sync.WaitGroup
				for range 8 {
					wg.Go(func() {
						<-start
						fetch()
					})
				}
				close(start)
				wg.Wait()
			} else {
				fetch()
				fetch()
			}

			assert.Equal(t, 1, logger.count(), "the batch's usage entry was written more than once")
		})
	}
}

// A status poll or a cancel reads the batch, waits for the provider, and
// saves its copy. A results fetch that logs the usage in between must stay
// logged, or the next fetch writes it again.
func TestBatchResultsUsageSurvivesStaleUpdate(t *testing.T) {
	tests := []struct {
		name   string
		update func(*BatchOrchestrator, string) error
	}{
		{name: "status poll", update: func(o *BatchOrchestrator, id string) error {
			_, err := o.Get(context.Background(), id)
			return err
		}},
		{name: "cancel", update: func(o *BatchOrchestrator, id string) error {
			_, err := o.Cancel(context.Background(), id)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			logger := &countingUsageLogger{}
			provider := &batchItemsProvider{items: 1, entered: make(chan struct{}), block: make(chan struct{})}
			store := batchstore.NewMemoryStore()
			orchestrator := newUsageOrchestrator(provider, store, logger)
			id := createUsageBatch(t, orchestrator)

			updated := make(chan error, 1)
			go func() { updated <- tt.update(orchestrator, id) }()
			<-provider.entered // the update has read the batch and waits for the provider

			_, err := orchestrator.Results(ctx, id, "req-results")
			require.NoError(t, err)
			require.Equal(t, 1, logger.count())

			close(provider.block)
			require.NoError(t, <-updated)
			stored, err := store.Get(ctx, id)
			require.NoError(t, err)
			assert.NotNil(t, stored.UsageLoggedAt, "the stale copy cleared the usage marker")

			_, err = orchestrator.Results(ctx, id, "req-results")
			require.NoError(t, err)
			assert.Equal(t, 1, logger.count(), "the batch's usage entry was written again")
			stored, err = store.Get(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, 10, stored.Batch.Usage.InputTokens, "the totals the stale copy dropped are restored")
		})
	}
}

// A process that stops after claiming the usage leaves entries unwritten. The
// next fetch waits for the claim to grow stale, then writes every entry; the
// ones written twice share an ID, so storage keeps one of each.
func TestBatchResultsRecoverInterruptedUsage(t *testing.T) {
	const items = 3
	tests := []struct {
		name    string
		written int
	}{
		{name: "stopped before the first entry", written: 0},
		{name: "stopped partway through the entries", written: 2},
		{name: "stopped before marking the usage as logged", written: items},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			provider := &batchItemsProvider{items: items}
			store := batchstore.NewMemoryStore()
			stopped := &countingUsageLogger{stopAfter: &tt.written}
			id := createUsageBatch(t, newUsageOrchestrator(provider, store, stopped))

			if tt.written < items {
				require.Panics(t, func() { _, _ = newUsageOrchestrator(provider, store, stopped).Results(ctx, id, "req-results") })
			} else {
				_, err := newUsageOrchestrator(provider, failingCompleteStore{store}, stopped).Results(ctx, id, "req-results")
				require.NoError(t, err)
			}
			require.Equal(t, tt.written, stopped.count())
			claim, err := store.ClaimUsage(ctx, id, time.Now(), time.Hour)
			require.NoError(t, err)
			require.Equal(t, batchstore.UsagePending, claim, "the stopped writer's claim is still held")

			logger := &countingUsageLogger{}
			restarted := newUsageOrchestrator(provider, store, logger)
			restarted.usageClaimTimeout = 50 * time.Millisecond
			_, err = restarted.Results(ctx, id, "req-results")
			require.NoError(t, err)

			assert.Len(t, logger.ids(), items, "the takeover writes every entry")
			assert.Subset(t, logger.ids(), stopped.ids(), "entries written twice keep their ID")
			stored, err := store.Get(ctx, id)
			require.NoError(t, err)
			assert.NotNil(t, stored.UsageLoggedAt)

			_, err = restarted.Results(ctx, id, "req-results")
			require.NoError(t, err)
			assert.Len(t, logger.ids(), items, "logged usage is not written again")
		})
	}
}

// failingCompleteStore claims usage but cannot mark it as logged, and loses
// the update that would save the marker instead.
type failingCompleteStore struct {
	*batchstore.MemoryStore
}

func (s failingCompleteStore) CompleteUsage(context.Context, string, time.Time) error {
	return errors.New("store unavailable")
}

func (s failingCompleteStore) Update(context.Context, *batchstore.StoredBatch) error {
	return errors.New("store unavailable")
}

// Losing billable usage is worse than counting it twice in a metric: when the
// store cannot claim, the entries are written all the same.
func TestBatchResultsLogUsageWhenClaimFails(t *testing.T) {
	logger := &countingUsageLogger{}
	orchestrator := newUsageOrchestrator(&batchItemsProvider{items: 2}, failingClaimStore{batchstore.NewMemoryStore()}, logger)
	id := createUsageBatch(t, orchestrator)

	_, err := orchestrator.Results(context.Background(), id, "req-results")
	require.NoError(t, err)
	assert.Equal(t, 2, logger.count())
}

// A fetch whose caller went away while it waited for another fetch's claim
// writes nothing and leaves the claim alone.
func TestBatchResultsCanceledWaitWritesNothing(t *testing.T) {
	logger := &countingUsageLogger{}
	store := batchstore.NewMemoryStore()
	orchestrator := newUsageOrchestrator(&batchItemsProvider{items: 1}, store, logger)
	id := createUsageBatch(t, orchestrator)
	claim, err := store.ClaimUsage(context.Background(), id, time.Now(), time.Hour)
	require.NoError(t, err)
	require.Equal(t, batchstore.UsageClaimed, claim)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = orchestrator.Results(ctx, id, "req-results")
	require.NoError(t, err)

	assert.Zero(t, logger.count())
	stored, err := store.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Nil(t, stored.UsageLoggedAt)
}
