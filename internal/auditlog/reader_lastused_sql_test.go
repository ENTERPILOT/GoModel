package auditlog

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lastUsedFakeDB answers GetLastUsedByAuthKeys with a canned result or error.
type lastUsedFakeDB struct {
	rows *lastUsedFakeRows
	err  error
}

func (db *lastUsedFakeDB) Exec(context.Context, string, ...any) (int64, error) { return 0, nil }

func (db *lastUsedFakeDB) Query(context.Context, string, ...any) (sqlx.Rows, error) {
	if db.err != nil {
		return nil, db.err
	}
	return db.rows, nil
}

func (db *lastUsedFakeDB) QueryRow(context.Context, string, ...any) sqlx.Row    { return nil }
func (db *lastUsedFakeDB) Dialect() sqlx.Dialect                                { return sqlx.SQLite }
func (db *lastUsedFakeDB) Schema(context.Context, ...string) error              { return nil }
func (db *lastUsedFakeDB) InTx(context.Context, func(sqlx.Querier) error) error { return nil }

// lastUsedFakeRows is a sqlx.Rows whose failure points are settable per test.
type lastUsedFakeRows struct {
	next    bool
	scanErr error
	err     error
}

func (r *lastUsedFakeRows) Next() bool        { return r.next }
func (r *lastUsedFakeRows) Scan(...any) error { return r.scanErr }
func (r *lastUsedFakeRows) Err() error        { return r.err }
func (r *lastUsedFakeRows) Close()            {}

func TestSQLReader_GetLastUsedByAuthKeys_Errors(t *testing.T) {
	probe := errors.New("boom")

	t.Run("query error", func(t *testing.T) {
		reader, err := NewSQLReader(&lastUsedFakeDB{err: probe})
		require.NoError(t, err)
		_, err = reader.GetLastUsedByAuthKeys(context.Background(), []string{"key-1"})
		require.ErrorIs(t, err, probe)
		require.ErrorContains(t, err, "failed to query auth key last used")
	})

	t.Run("scan error", func(t *testing.T) {
		db := &lastUsedFakeDB{rows: &lastUsedFakeRows{next: true, scanErr: probe}}
		reader, err := NewSQLReader(db)
		require.NoError(t, err)
		_, err = reader.GetLastUsedByAuthKeys(context.Background(), []string{"key-1"})
		require.ErrorIs(t, err, probe)
		require.ErrorContains(t, err, "failed to scan auth key last used row")
	})

	t.Run("iteration error", func(t *testing.T) {
		db := &lastUsedFakeDB{rows: &lastUsedFakeRows{err: probe}}
		reader, err := NewSQLReader(db)
		require.NoError(t, err)
		_, err = reader.GetLastUsedByAuthKeys(context.Background(), []string{"key-1"})
		require.ErrorIs(t, err, probe)
		require.ErrorContains(t, err, "error iterating auth key last used rows")
	})
}

// batchRecordingDB counts Query calls and the argument count of each call, to
// prove the last-used lookup batches its IN (...) parameters.
type batchRecordingDB struct {
	calls     int
	maxArgs   int
	totalArgs int
	lastArgs  [][]any
}

func (db *batchRecordingDB) Exec(context.Context, string, ...any) (int64, error) { return 0, nil }

func (db *batchRecordingDB) Query(_ context.Context, _ string, args ...any) (sqlx.Rows, error) {
	db.calls++
	db.maxArgs = max(db.maxArgs, len(args))
	db.totalArgs += len(args)
	db.lastArgs = append(db.lastArgs, args)
	return &lastUsedFakeRows{}, nil
}

func (db *batchRecordingDB) QueryRow(context.Context, string, ...any) sqlx.Row    { return nil }
func (db *batchRecordingDB) Dialect() sqlx.Dialect                                { return sqlx.SQLite }
func (db *batchRecordingDB) Schema(context.Context, ...string) error              { return nil }
func (db *batchRecordingDB) InTx(context.Context, func(sqlx.Querier) error) error { return nil }

func TestSQLReader_GetLastUsedByAuthKeys_BatchesKeys(t *testing.T) {
	ids := make([]string, 0, maxLastUsedKeysPerQuery+1)
	for i := 0; i < maxLastUsedKeysPerQuery+1; i++ {
		ids = append(ids, fmt.Sprintf("key-%d", i))
	}

	db := &batchRecordingDB{}
	reader, err := NewSQLReader(db)
	require.NoError(t, err)
	result, err := reader.GetLastUsedByAuthKeys(context.Background(), ids)
	require.NoError(t, err)
	assert.Empty(t, result)

	// Two batches: the full cap and the one-key remainder; every query stays
	// under the SQLite variable limit.
	require.Equal(t, 2, db.calls)
	assert.LessOrEqual(t, db.maxArgs, maxLastUsedKeysPerQuery)
	assert.Equal(t, len(ids), db.totalArgs)
}
