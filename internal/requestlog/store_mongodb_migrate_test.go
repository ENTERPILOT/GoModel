package requestlog

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
)

// writeLegacyDocument inserts an entry the way v0.2.5 did: into audit_logs,
// which MongoDB creates on the first insert.
func writeLegacyDocument(t *testing.T, db *mongo.Database, id string, statusCode int) {
	t.Helper()
	_, err := db.Collection(legacyCollectionName).InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: id},
		{Key: "timestamp", Value: time.Unix(1700000000, 0).UTC()},
		{Key: "status_code", Value: statusCode},
		{Key: "path", Value: "/v1/chat/completions"},
	})
	require.NoError(t, err)
}

// startMongoStore starts a store and waits for it to finish moving what an
// older release left in audit_logs.
func startMongoStore(t *testing.T, db *mongo.Database) *MongoDBStore {
	t.Helper()
	store, err := NewMongoDBStore(db, 0)
	require.NoError(t, err)
	store.absorb.Wait()
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mongoEntry(t *testing.T, db *mongo.Database, id string) *LogEntry {
	t.Helper()
	reader, err := NewMongoDBReader(db)
	require.NoError(t, err)
	entry, err := reader.GetLogByID(context.Background(), id)
	require.NoError(t, err)
	return entry
}

func collectionNames(t *testing.T, db *mongo.Database) []string {
	t.Helper()
	names, err := requestLogCollectionNames(context.Background(), db)
	require.NoError(t, err)
	slices.Sort(names)
	return names
}

func TestNewMongoDBStoreRenamesLegacyCollection(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		writeLegacyDocument(t, db, "written-before", 200)
		_, err := db.Collection(legacyCollectionName).Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: bson.D{{Key: "request_id", Value: 1}},
		})
		require.NoError(t, err)

		// Twice: the second start finds the work done.
		for range 2 {
			assert.Equal(t, requestLogCollectionName, startMongoStore(t, db).collection.Name())
		}

		assert.Equal(t, []string{requestLogCollectionName}, collectionNames(t, db))
		require.NotNil(t, mongoEntry(t, db, "written-before"))
	})
}

// A replica still on v0.2.5, or a binary rolled back to it, keeps inserting
// into audit_logs, which MongoDB creates again. The next start moves those
// entries over, in the background.
func TestNewMongoDBStoreAbsorbsARecreatedLegacyCollection(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		store := startMongoStore(t, db)
		at := time.Unix(1700000000, 0).UTC()
		require.NoError(t, store.WriteBatch(ctx, []*LogEntry{testLogEntry("written-before", at), testLogEntry("in-both", at)}))

		writeLegacyDocument(t, db, "written-by-older-replica", 200)
		writeLegacyDocument(t, db, "in-both", 500)

		startMongoStore(t, db)

		assert.Equal(t, []string{requestLogCollectionName}, collectionNames(t, db))
		for _, id := range []string{"written-before", "written-by-older-replica", "in-both"} {
			require.NotNil(t, mongoEntry(t, db, id), id)
		}
		assert.Equal(t, 200, mongoEntry(t, db, "in-both").StatusCode, "an entry already present is left alone")
	})
}

// A start interrupted between setting the legacy collection aside and merging
// it leaves the staging collection behind; the next start finishes the move,
// along with whatever an older replica has written since.
func TestNewMongoDBStoreFinishesAnInterruptedAbsorb(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		startMongoStore(t, db)

		writeLegacyDocument(t, db, "staged", 200)
		require.NoError(t, renameCollection(ctx, db, legacyCollectionName, legacyStagingPrefix+"interrupted"))
		writeLegacyDocument(t, db, "written-since", 200)

		startMongoStore(t, db)

		assert.Equal(t, []string{requestLogCollectionName}, collectionNames(t, db))
		require.NotNil(t, mongoEntry(t, db, "staged"))
		require.NotNil(t, mongoEntry(t, db, "written-since"))
	})
}

// With request logging turned off no store is created, and the reader still
// has to find what an older release logged. It reads the legacy collection
// where it is: renaming it is the store's job.
func TestMongoDBReaderReadsTheLegacyCollectionInPlace(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		writeLegacyDocument(t, db, "written-before", 200)
		require.NotNil(t, mongoEntry(t, db, "written-before"))
		assert.Equal(t, []string{legacyCollectionName}, collectionNames(t, db))

		startMongoStore(t, db)
		require.NotNil(t, mongoEntry(t, db, "written-before"))
		assert.Equal(t, []string{requestLogCollectionName}, collectionNames(t, db))
	})
}

// A replica with request logging turned off may create its reader before a
// store on another replica renames the collection. The reader follows it.
func TestMongoDBReaderFollowsTheRename(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		writeLegacyDocument(t, db, "written-before", 200)
		reader, err := NewMongoDBReader(db)
		require.NoError(t, err)
		entry, err := reader.GetLogByID(ctx, "written-before")
		require.NoError(t, err)
		require.NotNil(t, entry)

		startMongoStore(t, db)
		require.Equal(t, []string{requestLogCollectionName}, collectionNames(t, db))
		entry, err = reader.GetLogByID(ctx, "written-before")
		require.NoError(t, err)
		require.NotNil(t, entry, "the reader kept reading audit_logs after the rename")
	})
}

// A reader created while an older release's entries wait in a recreated
// audit_logs reads the request log, not those entries.
func TestMongoDBReaderPrefersTheRequestLogCollection(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		store := startMongoStore(t, db)
		require.NoError(t, store.WriteBatch(ctx, []*LogEntry{testLogEntry("written-before", time.Unix(1700000000, 0).UTC())}))
		writeLegacyDocument(t, db, "written-by-older-replica", 200)

		require.NotNil(t, mongoEntry(t, db, "written-before"))
		assert.Nil(t, mongoEntry(t, db, "written-by-older-replica"))
	})
}

// Only an error the server answered with keeps the store on audit_logs. A
// command that did not get through fails the start, which is retried.
func TestRenameRefused(t *testing.T) {
	assert.True(t, renameRefused(mongo.CommandError{Code: 13, Name: "Unauthorized"}))
	assert.False(t, renameRefused(mongo.CommandError{Code: 6, Labels: []string{"NetworkError"}}))
	assert.False(t, renameRefused(mongo.CommandError{Code: 50, Name: "MaxTimeMSExpired"}))
	assert.False(t, renameRefused(mongo.CommandError{Code: 12587, Name: "BackgroundOperationInProgressForNamespace"}))
	assert.False(t, renameRefused(context.DeadlineExceeded))
}

func TestRenameCollectionReportsRaces(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		err := renameCollection(ctx, db, legacyCollectionName, requestLogCollectionName)
		assert.True(t, isMongoError(err, mongoNamespaceNotFound), "missing source: %v", err)

		writeLegacyDocument(t, db, "one", 200)
		_, err = db.Collection(requestLogCollectionName).InsertOne(ctx, bson.D{{Key: "_id", Value: "two"}})
		require.NoError(t, err)
		err = renameCollection(ctx, db, legacyCollectionName, requestLogCollectionName)
		assert.True(t, isMongoError(err, mongoNamespaceExists), "existing target: %v", err)
	})
}

// Two replicas moving the legacy collection at once: one that drops its
// staging collection late removes nothing the other staged or an older replica
// wrote since.
func TestAbsorbLegacyCollectionsSurvivesAnotherReplicasLateDrop(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		startMongoStore(t, db)

		// Replica A sets a batch aside and merges it, then stalls before its drop.
		writeLegacyDocument(t, db, "staged-by-a", 200)
		stagedByA := db.Collection(legacyStagingPrefix + "a")
		require.NoError(t, renameCollection(ctx, db, legacyCollectionName, stagedByA.Name()))
		require.NoError(t, mergeIntoRequestLog(ctx, stagedByA))

		// An older replica writes again, and replica B runs the whole move.
		writeLegacyDocument(t, db, "written-since", 200)
		require.NoError(t, absorbLegacyCollections(ctx, db))

		// The older replica writes once more, then A's drop arrives. It can
		// only remove A's own collection, not the batch a later move stages.
		writeLegacyDocument(t, db, "written-last", 200)
		require.NoError(t, stagedByA.Drop(ctx))
		require.NoError(t, absorbLegacyCollections(ctx, db))

		assert.Equal(t, []string{requestLogCollectionName}, collectionNames(t, db))
		for _, id := range []string{"staged-by-a", "written-since", "written-last"} {
			require.NotNil(t, mongoEntry(t, db, id), id)
		}
	})
}
