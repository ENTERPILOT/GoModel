package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage"
)

// TestLoggerFlushesWhileSQLiteReadIsOpen holds a read open on SQLite for
// as long as the logger writes. The flushes run on the writer connection, so
// each one lands within the busy timeout and the buffer never fills: no entry
// is dropped.
func TestLoggerFlushesWhileSQLiteReadIsOpen(t *testing.T) {
	ctx := context.Background()
	sqlite, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "usage.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlite.Close() })

	usageStore, err := createUsageStore(ctx, sqlite, 0)
	require.NoError(t, err)
	const bufferSize = 10
	logger := NewLogger(usageStore, Config{Enabled: true, BufferSize: bufferSize, FlushInterval: 5 * time.Millisecond})

	// A scan of a table in main, read one row at a time and left open, holds
	// a read lock on the database and stands in for a dashboard query that
	// outlasts many flushes.
	_, err = sqlite.DB().ExecContext(ctx, `CREATE TABLE held (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = sqlite.DB().ExecContext(ctx, `INSERT INTO held (id) VALUES (1), (2), (3)`)
	require.NoError(t, err)
	read, err := sqlite.ReadDB().QueryContext(ctx, `SELECT id FROM held ORDER BY id`)
	require.NoError(t, err)
	defer read.Close()
	require.True(t, read.Next())

	countRows := func() int {
		var count int
		if err := sqlite.ReadDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM usage`).Scan(&count); err != nil {
			return -1
		}
		return count
	}

	const rounds = 5
	for round := range rounds {
		// A full buffer per round: it drains only if the previous flush landed.
		for range bufferSize {
			logger.Write(&UsageEntry{
				ID:        uuid.NewString(),
				RequestID: uuid.NewString(),
				Timestamp: time.Now().UTC(),
				Model:     "gpt-5",
				Provider:  "openai",
				Endpoint:  "/v1/chat/completions",
			})
		}
		want := (round + 1) * bufferSize
		require.Eventually(t, func() bool { return countRows() == want }, 5*time.Second, 5*time.Millisecond,
			"round %d: flush waited on the open read", round)
	}

	assert.True(t, read.Next(), "the read is still open")
	require.NoError(t, read.Close())
	require.NoError(t, logger.Close())
	assert.Equal(t, rounds*bufferSize, countRows())
}
