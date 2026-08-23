package virtualmodels

import (
	"context"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// legacyTables are the pre-v0.1.44 stores that virtual models replaced.
var legacyTables = []string{"aliases", "model_overrides"}

// rejectUnmigratedLegacyData fails startup when the database still carries
// rows in the legacy aliases / model_overrides tables while virtual_models is
// empty. The one-time seed that imported those rows shipped in v0.1.44 and was
// removed in v0.1.81; starting without it would silently drop every access
// policy and alias the legacy rows expressed, so the upgrade has to pass
// through a release that still seeds. Once virtual_models has rows the legacy
// tables are ignored, as on any database created after the seed shipped.
func rejectUnmigratedLegacyData(ctx context.Context, store Store, conn storage.Storage) error {
	existing, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("list virtual models: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}
	for _, table := range legacyTables {
		rows, err := countLegacyRows(ctx, conn, table)
		if err != nil {
			return fmt.Errorf("inspect legacy %s: %w", table, err)
		}
		if rows > 0 {
			return fmt.Errorf("the legacy %s table holds %d row(s) that were never migrated into virtual_models; "+
				"upgrade through any GoModel release from v0.1.44 to v0.1.80 first so its one-time seed imports them, "+
				"or drop the table if those entries are no longer wanted", table, rows)
		}
	}
	return nil
}

// countLegacyRows counts the rows of a legacy table, treating a table that was
// never created as empty.
func countLegacyRows(ctx context.Context, conn storage.Storage, table string) (int64, error) {
	return storage.ResolveSQLBackend[int64](ctx, conn,
		func(db sqlx.DB) (int64, error) {
			var count int64
			err := db.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count)
			if err != nil && isMissingTableError(err) {
				return 0, nil
			}
			return count, err
		},
		func(db *mongo.Database) (int64, error) {
			return db.Collection(table).CountDocuments(ctx, bson.M{})
		})
}

// isMissingTableError reports whether err says the queried table does not
// exist: SQLite says "no such table", PostgreSQL raises SQLSTATE 42P01.
func isMissingTableError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "no such table") || strings.Contains(message, "42P01")
}
