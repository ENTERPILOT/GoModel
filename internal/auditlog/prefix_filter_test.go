package auditlog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func TestAuditPrefixUpperBound(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   string
		wantOK bool
	}{
		{name: "ascii increments the last byte", prefix: "/voxina", want: "/voxinb", wantOK: true},
		{name: "slash becomes zero", prefix: "/team/", want: "/team0", wantOK: true},
		{name: "multibyte rune increments as a rune", prefix: "/zespół", want: "/zespóŃ", wantOK: true},
		{name: "skips the surrogate range", prefix: "a퟿", want: "a", wantOK: true},
		{name: "drops trailing maximum runes", prefix: "a\U0010FFFF", want: "b", wantOK: true},
		{name: "no bound for only maximum runes", prefix: "\U0010FFFF", want: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := auditPrefixUpperBound(tt.prefix)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
			if ok {
				assert.Greater(t, got, tt.prefix+"\U0010FFFF\U0010FFFF")
			}
		})
	}
}

func TestNormalizeAuditUserPathPrefix(t *testing.T) {
	tests := []struct {
		raw, want string
		wantErr   bool
	}{
		{raw: "", want: ""},
		{raw: "  ", want: ""},
		{raw: "voxina", want: "/voxina"},
		{raw: " /team/al ", want: "/team/al"},
		{raw: "/team/", want: "/team/"},
		{raw: "bad\xff", wantErr: true},
	}
	for _, tt := range tests {
		got, err := normalizeAuditUserPathPrefix(tt.raw)
		if tt.wantErr {
			require.Error(t, err, tt.raw)
			continue
		}
		require.NoError(t, err, tt.raw)
		assert.Equal(t, tt.want, got, tt.raw)
	}
}

func TestAuditPrefixSQLPredicate(t *testing.T) {
	condition, args := auditPrefixSQLPredicate("user_path", "/te")
	assert.Equal(t, "(user_path >= ? AND user_path < ?)", condition)
	assert.Equal(t, []any{"/te", "/tf"}, args)

	condition, args = auditPrefixSQLPredicate("user_path", "\U0010FFFF")
	assert.Equal(t, "user_path >= ?", condition)
	assert.Equal(t, []any{"\U0010FFFF"}, args)
}

func TestAuditPrefixRegexQuotesMetacharacters(t *testing.T) {
	assert.Equal(t, `^openai/gpt-4\.1`, auditPrefixRegex("openai/gpt-4.1"))
}

func TestReaderGetLogs_FieldFilters(t *testing.T) {
	runReaderSuite(t, func(t *testing.T, store LogStore, reader Reader) {
		ctx := context.Background()
		at := time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC)
		err := store.WriteBatch(ctx, []*LogEntry{
			{ID: "vox", Timestamp: at, RequestID: "req-vox", RequestedModel: "gpt-5", ResolvedModel: "openai/gpt-5", UserPath: "/voxinate/events/97gn9a9e"},
			{ID: "alias", Timestamp: at.Add(-time.Minute), RequestID: "req-alias", RequestedModel: "smart", ResolvedModel: "anthropic/claude-opus-4-1", UserPath: "/team/alpha"},
			{ID: "qualified", Timestamp: at.Add(-2 * time.Minute), RequestID: "req-qualified", RequestedModel: "openai/gpt-4.1", ResolvedModel: "openai/gpt-4.1", UserPath: "/team/alphabet"},
			{ID: "other", Timestamp: at.Add(-3 * time.Minute), RequestID: "req-other", RequestedModel: "GPT-5", ResolvedModel: "azure/GPT-5", UserPath: "/Team/beta"},
		})
		require.NoError(t, err)

		tests := []struct {
			name   string
			params LogQueryParams
			want   []string
		}{
			{name: "partial user path without slash", params: LogQueryParams{UserPathPrefix: "voxina"}, want: []string{"vox"}},
			{name: "user path prefix crosses segment boundaries", params: LogQueryParams{UserPathPrefix: "/team/alpha"}, want: []string{"alias", "qualified"}},
			{name: "user path prefix is case-sensitive", params: LogQueryParams{UserPathPrefix: "/team"}, want: []string{"alias", "qualified"}},
			{name: "model prefix matches the requested model", params: LogQueryParams{ModelPrefix: "gpt-"}, want: []string{"vox"}},
			{name: "model prefix matches the resolved model", params: LogQueryParams{ModelPrefix: "anthropic/claude"}, want: []string{"alias"}},
			{name: "model prefix matches both columns once", params: LogQueryParams{ModelPrefix: "openai/"}, want: []string{"vox", "qualified"}},
			{name: "model prefix treats dots literally", params: LogQueryParams{ModelPrefix: "openai/gpt-4x"}, want: []string{}},
			{name: "exact request id", params: LogQueryParams{RequestID: "req-alias"}, want: []string{"alias"}},
			{name: "request id is not a prefix", params: LogQueryParams{RequestID: "req-"}, want: []string{}},
			{name: "filters combine", params: LogQueryParams{UserPathPrefix: "/team", ModelPrefix: "openai/"}, want: []string{"qualified"}},
			{name: "prefix within a user path subtree", params: LogQueryParams{UserPath: "/team", UserPathPrefix: "/team/alphab"}, want: []string{"qualified"}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				tt.params.Limit = 10
				result, err := reader.GetLogs(ctx, tt.params)
				require.NoError(t, err)
				ids := make([]string, 0, len(result.Entries))
				for _, entry := range result.Entries {
					ids = append(ids, entry.ID)
				}
				assert.Equal(t, tt.want, ids)
				assert.Equal(t, len(tt.want), result.Total)

				sessions, err := reader.GetSessions(ctx, tt.params)
				require.NoError(t, err)
				assert.Equal(t, len(tt.want), sessions.Total)
			})
		}
	})
}

func TestReaderGetLogs_InvalidPrefixIsInvalidRequest(t *testing.T) {
	runReaderSuite(t, func(t *testing.T, _ LogStore, reader Reader) {
		_, err := reader.GetLogs(context.Background(), LogQueryParams{ModelPrefix: "gpt\xff"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model_prefix")
	})
}

// The prefix filters must reach the indexes created for them: the range has
// to be on the same byte-wise expression the index is declared on.
func TestSQLReader_PrefixFiltersUseIndexes(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)
		defer store.Close()
		store.indexBuild.Wait()
		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		tests := []struct {
			name    string
			params  LogQueryParams
			indexes []string
		}{
			{name: "user path prefix", params: LogQueryParams{UserPathPrefix: "/team"}, indexes: []string{"idx_audit_user_path_timestamp"}},
			{name: "model prefix", params: LogQueryParams{ModelPrefix: "gpt"}, indexes: []string{"idx_audit_requested_model_timestamp", "idx_audit_resolved_model_timestamp"}},
			{name: "request id", params: LogQueryParams{RequestID: "req-1"}, indexes: []string{"idx_audit_request_id"}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				conditions, args, err := reader.logFilters(ctx, tt.params)
				require.NoError(t, err)
				plan := explainAuditQuery(t, db, "SELECT COUNT(*) FROM audit_logs WHERE "+strings.Join(conditions, " AND "), args)
				for _, index := range tt.indexes {
					assert.Contains(t, plan, index)
				}
			})
		}
	})
}

// explainAuditQuery returns the query plan. PostgreSQL prefers a sequential
// scan on an empty table, so it is disabled for the check: an expression the
// index cannot serve would still scan.
func explainAuditQuery(t *testing.T, db sqlx.DB, query string, args []any) string {
	t.Helper()
	ctx := context.Background()
	explain, column := "EXPLAIN ", 0
	if db.Dialect() == sqlx.SQLite {
		explain, column = "EXPLAIN QUERY PLAN ", 3
	}
	var plan []string
	err := db.InTx(ctx, func(q sqlx.Querier) error {
		if db.Dialect() == sqlx.PostgreSQL {
			if _, err := q.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
				return err
			}
		}
		rows, err := q.Query(ctx, explain+query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if column == 0 {
				var line string
				if err := rows.Scan(&line); err != nil {
					return err
				}
				plan = append(plan, line)
				continue
			}
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				return err
			}
			plan = append(plan, detail)
		}
		return rows.Err()
	})
	require.NoError(t, err)
	return strings.Join(plan, "\n")
}

// Documents written before the requested/resolved split carry the requested
// model in the legacy "model" field, and the dashboard shows it from there.
func TestMongoDBReader_ModelPrefixMatchesLegacyModelField(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		store, err := NewMongoDBStore(db, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		_, err = db.Collection("audit_logs").InsertOne(ctx, bson.D{
			{Key: "_id", Value: "legacy"},
			{Key: "timestamp", Value: time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC)},
			{Key: "model", Value: "smart"},
			{Key: "resolved_model", Value: "openai/gpt-5"},
		})
		require.NoError(t, err)
		reader, err := NewMongoDBReader(db)
		require.NoError(t, err)

		result, err := reader.GetLogs(ctx, LogQueryParams{ModelPrefix: "sma", Limit: 10})
		require.NoError(t, err)
		require.Len(t, result.Entries, 1)
		assert.Equal(t, "smart", result.Entries[0].RequestedModel)
	})
}
