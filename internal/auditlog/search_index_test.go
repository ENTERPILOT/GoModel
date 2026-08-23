package auditlog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// requireTrigramIndex opens a store and reader on a PostgreSQL database that
// managed to build the trigram index, skipping where pg_trgm cannot be installed.
func requireTrigramIndex(t *testing.T, db sqlx.DB) (*SQLStore, *SQLReader) {
	t.Helper()
	if db.Dialect() != sqlx.PostgreSQL {
		t.Skip("trigram search index is PostgreSQL only")
	}
	ctx := context.Background()
	store, err := newSQLStoreForTest(t, db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	store.indexBuild.Wait()
	if !hasTrigramSearchIndex(ctx, db) {
		t.Skip("pg_trgm could not be installed on the test server")
	}
	reader, err := NewSQLReader(db)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}
	if !reader.searchIsIndexed(ctx) {
		t.Fatal("reader did not detect the trigram search index")
	}
	return store, reader
}

func TestSQLReader_SearchUsesTrigramIndex(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, reader := requireTrigramIndex(t, db)
		defer store.Close()
		ctx := context.Background()

		// The predicate must be the indexed expression, verbatim, or the
		// planner cannot use the index: prove it by reading the plan. On an
		// empty table a sequential scan is always cheaper, so it is disabled
		// for the check — a mismatched expression would still seq-scan.
		condition, args := reader.searchFilter("timeout awaiting", true)
		var plan []string
		err := db.InTx(ctx, func(q sqlx.Querier) error {
			if _, err := q.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
				return err
			}
			rows, err := q.Query(ctx, "EXPLAIN "+selectLogColumns+" WHERE "+condition, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					return err
				}
				plan = append(plan, line)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatalf("EXPLAIN: %v", err)
		}
		if joined := strings.Join(plan, "\n"); !strings.Contains(joined, trigramSearchIndex) {
			t.Fatalf("plan does not use %s:\n%s", trigramSearchIndex, joined)
		}
	})
}

func TestSQLReader_SearchViaTrigramIndexKeepsSemantics(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, reader := requireTrigramIndex(t, db)
		defer store.Close()
		ctx := context.Background()

		at := time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC)
		if err := store.WriteBatch(ctx, []*LogEntry{
			{ID: "path-hit", Timestamp: at, RequestedModel: "gpt-5", UserPath: "/team/alpha"},
			{ID: "error-hit", Timestamp: at, RequestedModel: "gpt-5", ErrorType: "provider_error",
				Data: &LogData{ErrorMessage: "http2: timeout awaiting response headers"}},
			// An error message on a row without an error type is not searched,
			// exactly as the column sweep gates it.
			{ID: "ungated", Timestamp: at, RequestedModel: "gpt-5",
				Data: &LogData{ErrorMessage: "timeout awaiting nothing"}},
			{ID: "wild", Timestamp: at, RequestedModel: "gpt-5", Path: "/v1/100%_done"},
		}); err != nil {
			t.Fatalf("WriteBatch: %v", err)
		}

		expect := func(search string, want ...string) {
			t.Helper()
			result, err := reader.GetLogs(ctx, LogQueryParams{Search: search, Limit: 10})
			if err != nil {
				t.Fatalf("GetLogs(%q): %v", search, err)
			}
			got := make([]string, 0, len(result.Entries))
			for _, entry := range result.Entries {
				got = append(got, entry.ID)
			}
			if strings.Join(got, ",") != strings.Join(want, ",") || result.Total != len(want) {
				t.Fatalf("search %q = %v (total %d), want %v", search, got, result.Total, want)
			}
		}
		expect("team/alpha", "path-hit")
		expect("TEAM/ALPHA", "path-hit")
		expect("timeout awaiting", "error-hit")
		expect("100%_done", "wild")
		expect("nothing")
	})
}

func TestSQLReader_ShortSearchKeepsColumnSweep(t *testing.T) {
	reader := &SQLReader{dialect: readerDialectFor(sqlx.PostgreSQL)}
	if condition, _ := reader.searchFilter("ab", true); !strings.Contains(condition, "request_id ILIKE") {
		t.Fatalf("two-character search should sweep columns, got %s", condition)
	}
	if condition, _ := reader.searchFilter("abc", true); strings.Contains(condition, "request_id ILIKE") {
		t.Fatalf("three-character search should use the indexed expression, got %s", condition)
	}
	// Characters, not bytes: one CJK character is three bytes but yields no
	// trigram, while three of them do.
	if condition, _ := reader.searchFilter("中", true); !strings.Contains(condition, "request_id ILIKE") {
		t.Fatalf("single multibyte character should sweep columns, got %s", condition)
	}
	if condition, _ := reader.searchFilter("中文字", true); strings.Contains(condition, "request_id ILIKE") {
		t.Fatalf("three multibyte characters should use the indexed expression, got %s", condition)
	}
	if condition, _ := reader.searchFilter("abc", false); !strings.Contains(condition, "request_id ILIKE") {
		t.Fatalf("without the index the search should sweep columns, got %s", condition)
	}
}
