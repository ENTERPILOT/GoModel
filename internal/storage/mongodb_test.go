package storage

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
)

// TestNewMongoDBConnectsAndPings runs only when MONGO_TEST_DSN names a
// reachable server; it never writes, so a throwaway database name suffices.
func TestNewMongoDBConnectsAndPings(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(mongotest.DSNEnv))
	if dsn == "" {
		t.Skipf("%s not set", mongotest.DSNEnv)
	}
	ctx := context.Background()

	store, err := NewMongoDB(ctx, MongoDBConfig{URL: dsn, Database: "gomodel_test_storage_ping"})
	require.NoError(t, err)

	checker, ok := store.(HealthChecker)
	require.True(t, ok, "%T does not implement HealthChecker", store)

	err = checker.Ping(ctx)
	assert.NoError(t, err, "Ping")
	assert.Equal(t, "gomodel_test_storage_ping", store.Database().Name())

	err = store.Close()
	assert.NoError(t, err, "Close")

	err = checker.Ping(ctx)
	assert.Error(t, err, "Ping after Close should fail")
}

func TestNewMongoDBRequiresURL(t *testing.T) {
	_, err := NewMongoDB(context.Background(), MongoDBConfig{})
	require.Error(t, err, "NewMongoDB with empty URL should fail")
}

func TestResolveMongoDatabase(t *testing.T) {
	tests := []struct {
		name string
		cfg  MongoDBConfig
		want string
	}{
		{
			name: "explicit database wins over URL path",
			cfg:  MongoDBConfig{URL: "mongodb://localhost:27017/from_url", Database: "explicit"},
			want: "explicit",
		},
		{
			name: "database from URL path",
			cfg:  MongoDBConfig{URL: "mongodb://localhost:27017/from_url"},
			want: "from_url",
		},
		{
			name: "URL path with query options",
			cfg:  MongoDBConfig{URL: "mongodb://localhost:27017/mydb?retryWrites=true&w=majority"},
			want: "mydb",
		},
		{
			name: "srv scheme",
			cfg:  MongoDBConfig{URL: "mongodb+srv://user:pass@cluster.example.com/appdb"},
			want: "appdb",
		},
		{
			name: "multi-host URL",
			cfg:  MongoDBConfig{URL: "mongodb://h1:27017,h2:27017/replicadb"},
			want: "replicadb",
		},
		{
			name: "no database in URL falls back to default",
			cfg:  MongoDBConfig{URL: "mongodb://localhost:27017"},
			want: DefaultMongoDatabase,
		},
		{
			name: "trailing slash only falls back to default",
			cfg:  MongoDBConfig{URL: "mongodb://localhost:27017/"},
			want: DefaultMongoDatabase,
		},
		{
			name: "database with trailing slash is trimmed",
			cfg:  MongoDBConfig{URL: "mongodb://localhost:27017/mydb/"},
			want: "mydb",
		},
		{
			name: "multi-segment path falls back to default",
			cfg:  MongoDBConfig{URL: "mongodb://localhost:27017/a/b"},
			want: DefaultMongoDatabase,
		},
		{
			name: "unparseable URL falls back to default",
			cfg:  MongoDBConfig{URL: "mongodb://[::1"},
			want: DefaultMongoDatabase,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, resolveMongoDatabase(tt.cfg), "resolveMongoDatabase(%+v)", tt.cfg)
		})
	}
}
