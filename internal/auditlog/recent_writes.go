package auditlog

import (
	"context"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// RecentWriteReader finds the entries other gateway processes persisted
// lately, so a replica's live log view can show its peers' traffic. It reads
// the write stamp the logger puts on every flushed entry (LogEntry.Stamp);
// entries written without one are never returned.
type RecentWriteReader interface {
	// RecentWrites lists the entries stamped at or after since (unix
	// microseconds) by any writer other than excludeWriter, newest first and
	// at most limit of them; none for a nonpositive limit.
	RecentWrites(ctx context.Context, since int64, excludeWriter string, limit int) ([]storage.RecentWrite, error)
	// EntriesByID loads the entries with the given ids, in no particular
	// order. Ids that match nothing are left out.
	EntriesByID(ctx context.Context, ids []string) ([]LogEntry, error)
}

// NewRecentWriteReader creates a RecentWriteReader on the storage backend.
// Returns nil when store is nil.
func NewRecentWriteReader(store storage.Storage) (RecentWriteReader, error) {
	if store == nil {
		return nil, nil
	}
	return storage.ResolveSQLBackend[RecentWriteReader](
		context.Background(),
		store,
		func(db sqlx.DB) (RecentWriteReader, error) { return NewSQLReader(db) },
		func(db *mongo.Database) (RecentWriteReader, error) { return NewMongoDBReader(db) },
	)
}

// writtenAtIndex serves RecentWrites. It is partial, so it only ever holds
// the stamped rows: history written before the column existed stays out.
const writtenAtIndex = "idx_audit_written_at"

const writtenAtIndexDefinition = "audit_logs(written_at) WHERE written_at IS NOT NULL"

// writtenAtIndexes creates the SQLite index inline. PostgreSQL builds it in
// the background (writtenAtBackgroundIndex) because the table it lands on can
// already hold years of history.
func writtenAtIndexes(dialect sqlx.Dialect) []string {
	if dialect != sqlx.SQLite {
		return nil
	}
	return []string{"CREATE INDEX IF NOT EXISTS " + writtenAtIndex + " ON " + writtenAtIndexDefinition}
}

// writtenAtBackgroundIndex is the PostgreSQL written_at index, built
// CONCURRENTLY so writes keep flowing while it scans an existing table.
var writtenAtBackgroundIndex = storage.BackgroundIndex{
	Name:   writtenAtIndex,
	Create: "CREATE INDEX CONCURRENTLY IF NOT EXISTS " + writtenAtIndex + " ON " + writtenAtIndexDefinition,
	Label:  "audit written_at",
}

// backgroundIndexLockKey serializes the audit index builds across replicas
// (see storage.BackgroundIndexBuilder).
const backgroundIndexLockKey int64 = 0x676d2d6175646974

// backgroundIndexRetry is the pause between build attempts; tests shorten it.
var backgroundIndexRetry = storage.BackgroundIndexRetry

// buildWrittenAtIndex builds the PostgreSQL written_at index, retrying until
// it is valid or ctx ends. It does nothing on SQLite, which builds the index
// inline, or on a DB without a pool to hold the build lock on.
func buildWrittenAtIndex(ctx context.Context, db sqlx.DB) {
	pool := sqlx.PostgreSQLPool(db)
	if pool == nil {
		return
	}
	builder := storage.BackgroundIndexBuilder{Pool: pool, LockKey: backgroundIndexLockKey, Retry: backgroundIndexRetry}
	builder.Build(ctx, writtenAtBackgroundIndex)
}

// RecentWrites implements RecentWriteReader.
func (r *SQLReader) RecentWrites(ctx context.Context, since int64, excludeWriter string, limit int) ([]storage.RecentWrite, error) {
	if limit <= 0 {
		// Not left to the engines: MongoDB reads a zero limit as no limit,
		// SQLite a negative one, and PostgreSQL rejects a negative one.
		return nil, nil
	}
	rows, err := r.db.Query(ctx,
		"SELECT "+r.dialect.idColumn+", written_at FROM audit_logs"+
			" WHERE written_at IS NOT NULL AND written_at >= ? AND writer_id <> ?"+
			" ORDER BY written_at DESC LIMIT ?",
		since, excludeWriter, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent audit log writes: %w", err)
	}
	defer rows.Close()

	var writes []storage.RecentWrite
	for rows.Next() {
		var write storage.RecentWrite
		if err := rows.Scan(&write.ID, &write.WrittenAt); err != nil {
			return nil, fmt.Errorf("failed to scan recent audit log write: %w", err)
		}
		writes = append(writes, write)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating recent audit log writes: %w", err)
	}
	return writes, nil
}

// EntriesByID implements RecentWriteReader. Provider attempts are hydrated,
// as for a list page.
func (r *SQLReader) EntriesByID(ctx context.Context, ids []string) ([]LogEntry, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err := r.db.Query(ctx,
		selectLogColumns+" WHERE "+r.dialect.idColumn+" IN ("+placeholders+")", args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit logs by id: %w", err)
	}
	defer rows.Close()

	entries := make([]LogEntry, 0, len(ids))
	for rows.Next() {
		entry, err := scanSQLLogEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, *entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating audit log rows: %w", err)
	}
	rows.Close()

	if err := r.loadAttempts(ctx, entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// RecentWrites implements RecentWriteReader.
func (r *MongoDBReader) RecentWrites(ctx context.Context, since int64, excludeWriter string, limit int) ([]storage.RecentWrite, error) {
	if limit <= 0 {
		// Not left to the engines: MongoDB reads a zero limit as no limit,
		// SQLite a negative one, and PostgreSQL rejects a negative one.
		return nil, nil
	}
	filter := bson.D{
		{Key: "written_at", Value: bson.D{{Key: "$gte", Value: since}}},
		{Key: "writer_id", Value: bson.D{{Key: "$ne", Value: excludeWriter}}},
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "written_at", Value: -1}}).
		SetLimit(int64(limit)).
		SetProjection(bson.D{{Key: "_id", Value: 1}, {Key: "written_at", Value: 1}})
	cursor, err := r.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent audit log writes: %w", err)
	}
	defer cursor.Close(ctx)

	var rows []struct {
		ID        string `bson:"_id"`
		WrittenAt int64  `bson:"written_at"`
	}
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to decode recent audit log writes: %w", err)
	}
	writes := make([]storage.RecentWrite, len(rows))
	for i, row := range rows {
		writes[i] = storage.RecentWrite{ID: row.ID, WrittenAt: row.WrittenAt}
	}
	return writes, nil
}

// EntriesByID implements RecentWriteReader.
func (r *MongoDBReader) EntriesByID(ctx context.Context, ids []string) ([]LogEntry, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	cursor, err := r.collection.Find(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}})
	if err != nil {
		return nil, fmt.Errorf("failed to query audit logs by id: %w", err)
	}
	defer cursor.Close(ctx)

	var rows []mongoLogRow
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to decode audit logs by id: %w", err)
	}
	entries := make([]LogEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, *row.toLogEntry())
	}
	return entries, nil
}
