package requestlog

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
)

const (
	partialWriteFailuresMetric       = "gomodel_request_log_partial_write_failures_total"
	legacyPartialWriteFailuresMetric = "gomodel_audit_log_partial_write_failures_total"
)

// The counter is exported under its new name and, for dashboards and alerts
// built before the rename, under its old one.
func TestPartialWriteFailuresAreExportedUnderBothNames(t *testing.T) {
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	names := make(map[string]bool, len(families))
	for _, family := range families {
		names[family.GetName()] = true
	}
	require.True(t, names[partialWriteFailuresMetric], "%s is not registered", partialWriteFailuresMetric)
	require.True(t, names[legacyPartialWriteFailuresMetric], "%s is not registered", legacyPartialWriteFailuresMetric)

	before := testutil.ToFloat64(requestLogPartialWriteFailures)
	legacyBefore := testutil.ToFloat64(legacyRequestLogPartialWriteFailures)
	countPartialWriteFailure()
	require.InDelta(t, before+1, testutil.ToFloat64(requestLogPartialWriteFailures), 0)
	require.InDelta(t, legacyBefore+1, testutil.ToFloat64(legacyRequestLogPartialWriteFailures), 0)
}

func TestMongoDBStorePartialWriteCountsUnderBothMetricNames(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(db, 0)
		require.NoError(t, err)

		before := testutil.ToFloat64(requestLogPartialWriteFailures)
		legacyBefore := testutil.ToFloat64(legacyRequestLogPartialWriteFailures)

		// Two entries with one id: the second insert fails on the duplicate key.
		now := time.Now().UTC()
		err = store.WriteBatch(context.Background(), []*LogEntry{
			{ID: "duplicate", Timestamp: now},
			{ID: "duplicate", Timestamp: now},
		})
		require.ErrorIs(t, err, ErrPartialWrite)

		require.InDelta(t, before+1, testutil.ToFloat64(requestLogPartialWriteFailures), 0)
		require.InDelta(t, legacyBefore+1, testutil.ToFloat64(legacyRequestLogPartialWriteFailures), 0)
	})
}
