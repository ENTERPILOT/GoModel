package live

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/enterpilot/gomodel/internal/usage"
)

// sharedStores is one database as two replicas see it.
type sharedStores struct {
	newAuditStore func(t *testing.T) auditlog.LogStore
	newUsageStore func(t *testing.T) usage.UsageStore
	auditReader   auditlog.RecentWriteReader
	usageReader   usage.RecentWriteReader
}

func runSharedStores(t *testing.T, body func(t *testing.T, stores sharedStores)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		auditReader, err := auditlog.NewSQLReader(db)
		require.NoError(t, err)
		usageReader, err := usage.NewSQLReader(db)
		require.NoError(t, err)
		body(t, sharedStores{
			newAuditStore: func(t *testing.T) auditlog.LogStore {
				store, err := auditlog.NewSQLStore(ctx, db, 0)
				require.NoError(t, err)
				t.Cleanup(func() { _ = store.Close() })
				return store
			},
			newUsageStore: func(t *testing.T) usage.UsageStore {
				store, err := usage.NewSQLStore(ctx, db, 0)
				require.NoError(t, err)
				t.Cleanup(func() { _ = store.Close() })
				return store
			},
			auditReader: auditReader,
			usageReader: usageReader,
		})
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		auditReader, err := auditlog.NewMongoDBReader(db)
		require.NoError(t, err)
		usageReader, err := usage.NewMongoDBReader(db)
		require.NoError(t, err)
		body(t, sharedStores{
			newAuditStore: func(t *testing.T) auditlog.LogStore {
				store, err := auditlog.NewMongoDBStore(db, 0)
				require.NoError(t, err)
				return store
			},
			newUsageStore: func(t *testing.T) usage.UsageStore {
				store, err := usage.NewMongoDBStore(db, 0)
				require.NoError(t, err)
				return store
			},
			auditReader: auditReader,
			usageReader: usageReader,
		})
	})
}

// Two replicas on one database: a request recorded on A reaches a dashboard
// on B exactly once, as its persisted events, and A's own dashboard sees it
// only through A's live events.
func TestRemoteFeedAcrossReplicasOnOneStore(t *testing.T) {
	runSharedStores(t, func(t *testing.T, stores sharedStores) {
		auditStore := stores.newAuditStore(t)
		usageStore := stores.newUsageStore(t)
		clock := &testClock{now: time.Now()}
		// Replica A is this process: its loggers stamp storage.WriterID().
		a := newTestHarness(t, storage.WriterID(), stores.auditReader, stores.usageReader, clock)
		b := newTestHarness(t, testPeer, stores.auditReader, stores.usageReader, clock)
		subA := a.subscribe(t)
		subB := b.subscribe(t)
		require.Empty(t, pendingEvents(subA))
		require.Empty(t, pendingEvents(subB))

		auditLogger := auditlog.NewLogger(auditStore, auditlog.Config{Enabled: true, BufferSize: 10, FlushInterval: time.Hour})
		auditLogger.SetLivePublisher(a.broker)
		usageLogger := usage.NewLogger(usageStore, usage.Config{Enabled: true, BufferSize: 10, FlushInterval: time.Hour})
		usageLogger.SetLivePublisher(a.broker)

		const usageID = "00000000-0000-4000-8000-0000000000aa"
		auditLogger.Write(&auditlog.LogEntry{
			ID:             "audit-on-a",
			RequestID:      "req-on-a",
			Timestamp:      clock.Now(),
			RequestedModel: "gpt-5",
			Provider:       "openai",
			StatusCode:     200,
		})
		usageLogger.Write(&usage.UsageEntry{
			ID:          usageID,
			RequestID:   "req-on-a",
			ProviderID:  "chatcmpl-1",
			Timestamp:   clock.Now(),
			Model:       "gpt-5",
			Provider:    "openai",
			Endpoint:    "/v1/chat/completions",
			TotalTokens: 15,
		})
		// Close flushes both batches to the database before returning.
		require.NoError(t, auditLogger.Close())
		require.NoError(t, usageLogger.Close())

		assert.Equal(t, []string{
			EventAuditCompleted + ":audit-on-a",
			EventUsageCompleted + ":" + usageID,
			EventAuditFlushed + ":audit-on-a",
			EventUsageFlushed + ":" + usageID,
		}, eventIDs(t, pendingEvents(subA)), "A streams its own request live")

		b.tick(t)
		events := pendingEvents(subB)
		assert.Equal(t, []string{
			EventAuditFlushed + ":audit-on-a",
			EventUsageFlushed + ":" + usageID,
		}, eventIDs(t, events), "B shows A's request once it is persisted")
		if len(events) == 2 {
			assert.Equal(t, "req-on-a", events[0].RequestID)
			assert.Equal(t, "req-on-a", events[1].RequestID)
		}

		b.tick(t)
		assert.Empty(t, pendingEvents(subB), "and only once")
		a.tick(t)
		assert.Empty(t, pendingEvents(subA), "A does not show its own request twice")
	})
}
