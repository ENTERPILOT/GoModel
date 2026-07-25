// Package mongotest runs a store's test suite against MongoDB.
//
// It is the document-store counterpart to sqlxtest. MongoDB stays hand-written
// rather than sharing an implementation with the SQL backends, which makes it
// the one place where a behaviour can drift unnoticed: most domains ship a
// MongoDB store with no test that touches a database. A suite written against
// the domain's Store interface can run on every backend, so a domain's
// behaviour is asserted once and checked everywhere.
//
// MongoDB runs only when MONGO_TEST_DSN names a reachable server; otherwise
// the subtest skips. The variable is deliberately not MONGODB_URL — a suite
// that creates and drops databases should take a separate, explicit opt-in.
package mongotest

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// DSNEnv names the environment variable holding the test MongoDB connection
// string.
const DSNEnv = "MONGO_TEST_DSN"

// databaseCounter keeps concurrently running subtests in separate databases.
// Tests must not depend on wall-clock time or randomness for naming.
var databaseCounter atomic.Uint64

// Run executes fn against an empty MongoDB database that is dropped
// afterwards, as a subtest named "mongodb". It skips when no server is
// configured.
func Run(t *testing.T, fn func(t *testing.T, db *mongo.Database)) {
	t.Helper()

	t.Run("mongodb", func(t *testing.T) {
		db := New(t)
		if db == nil {
			return // New already skipped
		}
		fn(t, db)
	})
}

// New returns an empty MongoDB database for one test, or nil after skipping
// when no test server is configured.
func New(t *testing.T) *mongo.Database {
	t.Helper()

	dsn := strings.TrimSpace(os.Getenv(DSNEnv))
	if dsn == "" {
		t.Skipf("%s not set", DSNEnv)
		return nil
	}

	// Every server call is bounded: an unreachable DSN should skip promptly
	// rather than stall every opted-in suite, and cleanup must not be able to
	// hang the test binary.
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(dsn))
	if err != nil {
		t.Skipf("connect to %s: %v", DSNEnv, err)
		return nil
	}
	if err := client.Ping(ctx, nil); err != nil {
		disconnect(client)
		t.Skipf("ping %s: %v", DSNEnv, err)
		return nil
	}

	db := client.Database(DatabaseName(t.Name(), databaseCounter.Add(1)))
	t.Cleanup(func() {
		dropCtx, cancelDrop := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancelDrop()
		_ = db.Drop(dropCtx)
		disconnect(client)
	})
	return db
}

const (
	connectTimeout = 5 * time.Second
	cleanupTimeout = 10 * time.Second
)

func disconnect(client *mongo.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_ = client.Disconnect(ctx)
}

// DatabaseName builds a unique database name for a test.
//
// The counter only separates subtests inside one process, and `go test ./...`
// runs packages in parallel — two packages that happen to share a test name
// would otherwise create *and drop* the same database. The pid separates them.
//
// MongoDB rejects names of 64 bytes or more, so the test name is bounded
// rather than concatenated whole: a nested subtest name easily runs past the
// limit on its own.
func DatabaseName(testName string, counter uint64) string {
	const prefix = "gomodel_test_"

	suffix := "_" + strconv.Itoa(os.Getpid()) + "_" + strconv.FormatUint(counter, 10)
	sanitized := sanitize(testName)
	if budget := 63 - len(prefix) - len(suffix); len(sanitized) > budget {
		sanitized = sanitized[:max(budget, 0)]
	}
	return prefix + sanitized + suffix
}

// sanitize reduces a test name to characters MongoDB accepts in a database
// name: it rejects / \ . " $ and null bytes, and Go test names routinely carry
// slashes.
func sanitize(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
