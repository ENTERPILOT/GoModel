package usage

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// RecentWriteReader finds the usage entries other gateway processes
// persisted lately, so a replica's live log view can show its peers'
// traffic. It reads the write stamp the logger puts on every flushed entry
// (UsageEntry.Stamp); entries written without one are never returned.
type RecentWriteReader interface {
	// RecentWrites lists the entries stamped at or after since (unix
	// microseconds) by any writer other than excludeWriter, newest first and
	// at most limit of them.
	RecentWrites(ctx context.Context, since int64, excludeWriter string, limit int) ([]storage.RecentWrite, error)
	// LogEntriesByID loads the entries with the given ids, in no particular
	// order. Ids that match nothing are left out.
	LogEntriesByID(ctx context.Context, ids []string) ([]UsageLogEntry, error)
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
// SQLite creates it inline; PostgreSQL builds it in the background (see
// backgroundIndexes).
const writtenAtIndex = "idx_usage_written_at"

const writtenAtIndexDefinition = "usage(written_at) WHERE written_at IS NOT NULL"

// RecentWrites implements RecentWriteReader.
func (r *SQLReader) RecentWrites(ctx context.Context, since int64, excludeWriter string, limit int) ([]storage.RecentWrite, error) {
	rows, err := r.db.Query(ctx,
		"SELECT id, written_at FROM usage"+
			" WHERE written_at IS NOT NULL AND written_at >= ? AND writer_id <> ?"+
			" ORDER BY written_at DESC LIMIT ?",
		since, excludeWriter, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent usage writes: %w", err)
	}
	defer rows.Close()

	var writes []storage.RecentWrite
	for rows.Next() {
		var write storage.RecentWrite
		if err := rows.Scan(&write.ID, &write.WrittenAt); err != nil {
			return nil, fmt.Errorf("failed to scan recent usage write: %w", err)
		}
		writes = append(writes, write)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating recent usage writes: %w", err)
	}
	return writes, nil
}

// LogEntriesByID implements RecentWriteReader.
func (r *SQLReader) LogEntriesByID(ctx context.Context, ids []string) ([]UsageLogEntry, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := r.db.Query(ctx,
		`SELECT `+usageLogColumns+` FROM usage WHERE id IN (`+sqlPlaceholders(len(ids))+`)`,
		appendStringArgs(make([]any, 0, len(ids)), ids)...)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage by id: %w", err)
	}
	defer rows.Close()

	entries, err := scanUsageLogEntries(rows)
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage rows: %w", err)
	}
	return entries, nil
}

// RecentWrites implements RecentWriteReader.
func (r *MongoDBReader) RecentWrites(ctx context.Context, since int64, excludeWriter string, limit int) ([]storage.RecentWrite, error) {
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
		return nil, fmt.Errorf("failed to query recent usage writes: %w", err)
	}
	defer cursor.Close(ctx)

	var rows []struct {
		ID        string `bson:"_id"`
		WrittenAt int64  `bson:"written_at"`
	}
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to decode recent usage writes: %w", err)
	}
	writes := make([]storage.RecentWrite, len(rows))
	for i, row := range rows {
		writes[i] = storage.RecentWrite{ID: row.ID, WrittenAt: row.WrittenAt}
	}
	return writes, nil
}

// LogEntriesByID implements RecentWriteReader.
func (r *MongoDBReader) LogEntriesByID(ctx context.Context, ids []string) ([]UsageLogEntry, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	cursor, err := r.collection.Find(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}})
	if err != nil {
		return nil, fmt.Errorf("failed to query usage by id: %w", err)
	}
	defer cursor.Close(ctx)

	var rows []mongoUsageLogRow
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to decode usage by id: %w", err)
	}
	entries := make([]UsageLogEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, row.toUsageLogEntry())
	}
	return entries, nil
}
