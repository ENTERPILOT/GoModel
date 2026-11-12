package auditlog

import (
	"context"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// buildBackgroundIndexes builds the written_at, model and trigram search
// indexes, then retires the legacy model index.
//
// Every build and drop holds the builder's lock. Several replicas start
// against one database, and an invalid index is either an abandoned build or
// another replica's build in progress: only the lock tells them apart, and
// dropping the second kind stalls audit_logs and discards the build.
//
// They are built one after the other, since PostgreSQL runs one concurrent
// build per table at a time. The written_at index gets one attempt first and
// its retries last, so one that keeps failing holds up neither of the others;
// those get one attempt per start, as a failure there is rarely transient.
func buildBackgroundIndexes(ctx context.Context, db sqlx.DB) {
	builder, ok := backgroundIndexBuilder(db)
	if !ok {
		return
	}
	built := builder.TryBuild(ctx, writtenAtBackgroundIndex)
	// The legacy index goes only once both replacements serve the model filter.
	if builder.BuildOnce(ctx, modelBackgroundIndexes()...) {
		builder.TryDrop(ctx, legacyModelBackgroundIndex)
	}
	if ctx.Err() != nil {
		return
	}
	if trigram, ok := trigramBackgroundIndex(ctx, db, postgresErrorMessage); ok {
		builder.BuildOnce(ctx, trigram)
	}
	if !built {
		builder.Build(ctx, writtenAtBackgroundIndex)
	}
}
