package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
)

// importBatchSize is how many rows are read before each write to the
// destination.
const importBatchSize = 500

// mongoDuplicateKeyCode is MongoDB's E11000 duplicate key error.
const mongoDuplicateKeyCode = 11000

// importColumns lists the usage columns read from the source, each with the
// value used when the column is NULL or missing because the source database
// was last opened by an older GoModel.
var importColumns = []struct{ name, fallback string }{
	{"id", "''"},
	{"request_id", "''"},
	{"provider_id", "''"},
	{"timestamp", "''"},
	{"model", "''"},
	{"provider", "''"},
	{"provider_name", "''"},
	{"endpoint", "''"},
	{"user_path", "''"},
	{"session_id", "''"},
	{"cache_type", "''"},
	{"labels", "NULL"},
	{"input_tokens", "0"},
	{"output_tokens", "0"},
	{"total_tokens", "0"},
	{"rewrite_tokens_saved", "0"},
	{"rewrite_cost_saved", "NULL"},
	{"raw_data", "NULL"},
	{"input_cost", "NULL"},
	{"output_cost", "NULL"},
	{"total_cost", "NULL"},
	{"cost_source", "''"},
	{"costs_calculation_caveat", "''"},
	{"auth_key_id", "''"},
	{"markup_percent", "NULL"},
	{"markup_selector", "''"},
	{"billed_cost", "NULL"},
}

// ImportSQLiteFile copies every usage row from another GoModel instance's
// SQLite database file into dst. The source is opened read-only. Rows whose
// id dst already holds are skipped, so running it again is safe. It returns
// the number of rows read from the source. retentionDays is the
// destination's configured usage retention, so the store sets up the same
// indexes (a MongoDB TTL index) the gateway would.
func ImportSQLiteFile(ctx context.Context, sourcePath string, dst storage.Storage, retentionDays int) (int, error) {
	dsn, err := readOnlySQLiteDSN(sourcePath)
	if err != nil {
		return 0, fmt.Errorf("open source database: %w", err)
	}
	src, err := sql.Open("sqlite", dsn)
	if err != nil {
		return 0, fmt.Errorf("open source database: %w", err)
	}
	defer src.Close()

	store, err := createUsageStore(dst, retentionDays)
	if err != nil {
		return 0, fmt.Errorf("open destination usage store: %w", err)
	}
	defer store.Close()

	return copySQLiteUsage(ctx, src, store, importBatchSize)
}

// readOnlySQLiteDSN returns a read-only SQLite URI for an existing file. The
// path is escaped so characters such as '#', '?', or '%' in a file name are
// not read as URI syntax.
func readOnlySQLiteDSN(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath // Windows drive letter: file:///C:/...
	}
	u := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro&_busy_timeout=5000"}
	return u.String(), nil
}

func copySQLiteUsage(ctx context.Context, src *sql.DB, dst UsageStore, batchSize int) (int, error) {
	query, err := sqliteImportQuery(ctx, src)
	if err != nil {
		return 0, err
	}
	rows, err := src.QueryContext(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("read source usage: %w", err)
	}
	defer rows.Close()

	total := 0
	batch := make([]*UsageEntry, 0, batchSize)
	flush := func() error {
		if err := writeImportBatch(ctx, dst, batch); err != nil {
			return err
		}
		total += len(batch)
		batch = batch[:0]
		return nil
	}
	for rows.Next() {
		entry, err := scanImportRow(rows)
		if err != nil {
			return total, err
		}
		batch = append(batch, entry)
		if len(batch) == batchSize {
			if err := flush(); err != nil {
				return total, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return total, fmt.Errorf("read source usage: %w", err)
	}
	if err := flush(); err != nil {
		return total, err
	}
	return total, nil
}

// sqliteImportQuery builds the SELECT for the columns the source has.
func sqliteImportQuery(ctx context.Context, src *sql.DB) (string, error) {
	rows, err := src.QueryContext(ctx, "SELECT name FROM pragma_table_info('usage')")
	if err != nil {
		return "", fmt.Errorf("read source schema: %w", err)
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", fmt.Errorf("read source schema: %w", err)
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read source schema: %w", err)
	}
	if !present["id"] {
		return "", errors.New("source database has no usage table; is it a GoModel database?")
	}

	exprs := make([]string, len(importColumns))
	for i, col := range importColumns {
		exprs[i] = col.fallback
		if present[col.name] {
			exprs[i] = "COALESCE(" + col.name + ", " + col.fallback + ")"
		}
	}
	return "SELECT " + strings.Join(exprs, ", ") + " FROM usage", nil
}

func scanImportRow(rows *sql.Rows) (*UsageEntry, error) {
	var e UsageEntry
	var ts string
	var labelsJSON, rawDataJSON *string
	if err := rows.Scan(&e.ID, &e.RequestID, &e.ProviderID, &ts, &e.Model, &e.Provider, &e.ProviderName,
		&e.Endpoint, &e.UserPath, &e.SessionID, &e.CacheType, &labelsJSON,
		&e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.RewriteTokensSaved, &e.RewriteCostSaved, &rawDataJSON,
		&e.InputCost, &e.OutputCost, &e.TotalCost, &e.CostSource, &e.CostsCalculationCaveat, &e.AuthKeyID,
		&e.MarkupPercent, &e.MarkupSelector, &e.BilledCost); err != nil {
		return nil, fmt.Errorf("read source usage row: %w", err)
	}
	if e.ID == "" {
		return nil, errors.New("read source usage row: empty id")
	}
	t, ok := sqlutil.ParseSQLiteTimestamp(ts)
	if !ok {
		return nil, fmt.Errorf("usage row %s: unparseable timestamp %q", e.ID, ts)
	}
	e.Timestamp = t
	if labelsJSON != nil {
		e.Labels = sqlutil.StringsFromJSON(*labelsJSON, e.RequestID)
	}
	if rawDataJSON != nil && *rawDataJSON != "" {
		if err := json.Unmarshal([]byte(*rawDataJSON), &e.RawData); err != nil {
			return nil, fmt.Errorf("usage row %s: raw_data: %w", e.ID, err)
		}
	}
	return &e, nil
}

// writeImportBatch writes a batch, treating rows that already exist as done.
// SQLite and PostgreSQL skip them on insert; MongoDB reports each one as a
// duplicate key error.
func writeImportBatch(ctx context.Context, dst UsageStore, batch []*UsageEntry) error {
	err := dst.WriteBatch(ctx, batch)
	if err == nil || onlyDuplicateKeys(err) {
		return nil
	}
	return fmt.Errorf("write usage: %w", err)
}

func onlyDuplicateKeys(err error) bool {
	var partial *PartialWriteError
	if !errors.As(err, &partial) || partial.Cause.WriteConcernError != nil || len(partial.Cause.WriteErrors) == 0 {
		return false
	}
	for _, we := range partial.Cause.WriteErrors {
		if we.Code != mongoDuplicateKeyCode {
			return false
		}
	}
	return true
}
