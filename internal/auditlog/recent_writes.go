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
	// PreviewsByID loads the entries with the given ids, in no particular
	// order, without their bodies (see EntryPreview). Ids that match nothing
	// are left out.
	PreviewsByID(ctx context.Context, ids []string) ([]EntryPreview, error)
}

// EntryPreview is an entry loaded for a live preview: everything but the
// request and response bodies and each provider attempt's response body and
// headers, which the query leaves in the database until the entry is opened.
type EntryPreview struct {
	LogEntry
	// RequestBodyCaptured and ResponseBodyCaptured report that the stored
	// entry holds that body.
	RequestBodyCaptured  bool
	ResponseBodyCaptured bool
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

// backgroundIndexBuilder returns the builder of the PostgreSQL background
// indexes. It reports false on SQLite, which builds its indexes inline, and
// on a DB without a pool to hold the build lock on.
func backgroundIndexBuilder(db sqlx.DB) (storage.BackgroundIndexBuilder, bool) {
	pool := sqlx.PostgreSQLPool(db)
	if pool == nil {
		return storage.BackgroundIndexBuilder{}, false
	}
	return storage.BackgroundIndexBuilder{Pool: pool, LockKey: backgroundIndexLockKey, Retry: backgroundIndexRetry}, true
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

// previewColumns are logColumns with the bodies cut out of data, followed by
// whether each body was there.
func previewColumns(dialect sqlx.Dialect) string {
	columns := strings.TrimSuffix(logColumns, "data")
	if dialect == sqlx.PostgreSQL {
		return columns + `data - 'request_body' - 'response_body',` +
			` (data -> 'request_body') IS NOT NULL, (data -> 'response_body') IS NOT NULL`
	}
	return columns + `json_remove(data, '$.request_body', '$.response_body'),` +
		` json_type(data, '$.request_body') IS NOT NULL, json_type(data, '$.response_body') IS NOT NULL`
}

// PreviewsByID implements RecentWriteReader. Provider attempts are hydrated,
// as for a list page, without their response bodies and headers.
func (r *SQLReader) PreviewsByID(ctx context.Context, ids []string) ([]EntryPreview, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err := r.db.Query(ctx,
		"SELECT "+previewColumns(r.db.Dialect())+" FROM audit_logs WHERE "+r.dialect.idColumn+" IN ("+placeholders+")", args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit log previews by id: %w", err)
	}
	defer rows.Close()

	entries := make([]LogEntry, 0, len(ids))
	type captured struct{ request, response bool }
	flags := make([]captured, 0, len(ids))
	for rows.Next() {
		var flag captured
		entry, err := scanSQLLogEntry(extraColumns{row: rows, extra: []any{&flag.request, &flag.response}})
		if err != nil {
			return nil, err
		}
		entries = append(entries, *entry)
		flags = append(flags, flag)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating audit log preview rows: %w", err)
	}
	rows.Close()

	if err := r.loadAttempts(ctx, entries, false); err != nil {
		return nil, err
	}
	previews := make([]EntryPreview, len(entries))
	for i := range entries {
		previews[i] = EntryPreview{
			LogEntry:             entries[i],
			RequestBodyCaptured:  flags[i].request,
			ResponseBodyCaptured: flags[i].response,
		}
	}
	return previews, nil
}

// extraColumns scans a row that carries more columns than its scanner
// expects, into extra after the scanner's own.
type extraColumns struct {
	row   sqlx.Row
	extra []any
}

func (e extraColumns) Scan(dest ...any) error {
	return e.row.Scan(append(dest, e.extra...)...)
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

// PreviewsByID implements RecentWriteReader.
func (r *MongoDBReader) PreviewsByID(ctx context.Context, ids []string) ([]EntryPreview, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	captured := func(field string) bson.D {
		return bson.D{{Key: "$ne", Value: bson.A{bson.D{{Key: "$type", Value: field}}, "missing"}}}
	}
	cursor, err := r.collection.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}}}},
		{{Key: "$addFields", Value: bson.D{
			{Key: "_request_body_captured", Value: captured("$data.request_body")},
			{Key: "_response_body_captured", Value: captured("$data.response_body")},
		}}},
		{{Key: "$project", Value: bson.D{
			{Key: "data.request_body", Value: 0},
			{Key: "data.response_body", Value: 0},
			{Key: "data.attempts.response_body", Value: 0},
			{Key: "data.attempts.response_headers", Value: 0},
		}}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query audit log previews by id: %w", err)
	}
	defer cursor.Close(ctx)

	var rows []struct {
		Row                  mongoLogRow `bson:",inline"`
		RequestBodyCaptured  bool        `bson:"_request_body_captured"`
		ResponseBodyCaptured bool        `bson:"_response_body_captured"`
	}
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to decode audit log previews by id: %w", err)
	}
	previews := make([]EntryPreview, 0, len(rows))
	for _, row := range rows {
		previews = append(previews, EntryPreview{
			LogEntry:             *row.Row.toLogEntry(),
			RequestBodyCaptured:  row.RequestBodyCaptured,
			ResponseBodyCaptured: row.ResponseBodyCaptured,
		})
	}
	return previews, nil
}
