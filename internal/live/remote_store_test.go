package live

import (
	"context"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/requestlog"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/enterpilot/gomodel/internal/usage"
)

// sharedStores is one database as two replicas see it.
type sharedStores struct {
	newRequestLogStore func(t *testing.T) requestlog.LogStore
	newUsageStore      func(t *testing.T) usage.UsageStore
	requestLogReader   requestlog.RecentWriteReader
	usageReader        usage.RecentWriteReader
}

func runSharedStores(t *testing.T, body func(t *testing.T, stores sharedStores)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		requestLogReader, err := requestlog.NewSQLReader(db)
		require.NoError(t, err)
		usageReader, err := usage.NewSQLReader(db)
		require.NoError(t, err)
		body(t, sharedStores{
			newRequestLogStore: func(t *testing.T) requestlog.LogStore {
				store, err := requestlog.NewSQLStore(ctx, db, 0)
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
			requestLogReader: requestLogReader,
			usageReader:      usageReader,
		})
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		requestLogReader, err := requestlog.NewMongoDBReader(db)
		require.NoError(t, err)
		usageReader, err := usage.NewMongoDBReader(db)
		require.NoError(t, err)
		body(t, sharedStores{
			newRequestLogStore: func(t *testing.T) requestlog.LogStore {
				store, err := requestlog.NewMongoDBStore(db, 0)
				require.NoError(t, err)
				return store
			},
			newUsageStore: func(t *testing.T) usage.UsageStore {
				store, err := usage.NewMongoDBStore(db, 0)
				require.NoError(t, err)
				return store
			},
			requestLogReader: requestLogReader,
			usageReader:      usageReader,
		})
	})
}

// Two replicas on one database: a request recorded on A reaches a dashboard
// on B exactly once, as its persisted events, and A's own dashboard sees it
// only through A's live events.
func TestRemoteFeedAcrossReplicasOnOneStore(t *testing.T) {
	runSharedStores(t, func(t *testing.T, stores sharedStores) {
		requestLogStore := stores.newRequestLogStore(t)
		usageStore := stores.newUsageStore(t)
		clock := &testClock{now: time.Now()}
		// Replica A is this process: its loggers stamp storage.WriterID().
		a := newTestHarness(t, storage.WriterID(), stores.requestLogReader, stores.usageReader, clock)
		b := newTestHarness(t, testPeer, stores.requestLogReader, stores.usageReader, clock)
		subA := a.subscribe(t)
		subB := b.subscribe(t)
		require.Empty(t, pendingEvents(subA))
		require.Empty(t, pendingEvents(subB))

		requestLogger := requestlog.NewLogger(requestLogStore, requestlog.Config{Enabled: true, BufferSize: 10, FlushInterval: time.Hour})
		requestLogger.SetLivePublisher(a.broker)
		usageLogger := usage.NewLogger(usageStore, usage.Config{Enabled: true, BufferSize: 10, FlushInterval: time.Hour})
		usageLogger.SetLivePublisher(a.broker)

		const usageID = "00000000-0000-4000-8000-0000000000aa"
		requestLogger.Write(&requestlog.LogEntry{
			ID:             "request-log-on-a",
			RequestID:      "req-on-a",
			Timestamp:      clock.Now(),
			RequestedModel: "gpt-5",
			Provider:       "openai",
			StatusCode:     200,
			Data:           &requestlog.LogData{RequestBody: requestlog.CaptureLoggedBody([]byte(`{"model":"gpt-5"}`))},
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
		require.NoError(t, requestLogger.Close())
		require.NoError(t, usageLogger.Close())

		assert.Equal(t, []string{
			EventRequestLogCompleted + ":request-log-on-a",
			EventUsageCompleted + ":" + usageID,
			EventRequestLogFlushed + ":request-log-on-a",
			EventUsageFlushed + ":" + usageID,
		}, eventIDs(t, pendingEvents(subA)), "A streams its own request live")

		b.tick(t)
		events := pendingEvents(subB)
		assert.Equal(t, []string{
			EventRequestLogFlushed + ":request-log-on-a",
			EventUsageFlushed + ":" + usageID,
		}, eventIDs(t, events), "B shows A's request once it is persisted")
		if len(events) == 2 {
			assert.Equal(t, "req-on-a", events[0].RequestID)
			assert.Equal(t, "req-on-a", events[1].RequestID)

			var requestLog struct {
				RemoteWrittenAt int64          `json:"_remote_written_at"`
				Data            map[string]any `json:"data"`
			}
			require.NoError(t, json.Unmarshal(events[0].Data, &requestLog))
			assert.Positive(t, requestLog.RemoteWrittenAt, "marked as another replica's")
			assert.NotContains(t, requestLog.Data, "request_body", "the body stays in the database")
			assert.Equal(t, true, requestLog.Data["request_body_captured"])
		}

		b.tick(t)
		assert.Empty(t, pendingEvents(subB), "and only once")
		a.tick(t)
		assert.Empty(t, pendingEvents(subA), "A does not show its own request twice")
	})
}
