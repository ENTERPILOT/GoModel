package auditlog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLReader implements Reader for SQL databases.
type SQLReader struct {
	db      sqlx.DB
	dialect readerDialect
}

// NewSQLReader creates an audit log reader over a SQL database.
func NewSQLReader(db sqlx.DB) (*SQLReader, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	return &SQLReader{db: db, dialect: readerDialectFor(db.Dialect())}, nil
}

// readerDialect holds the handful of spellings the two engines genuinely
// disagree on. Everything else in this reader is one query for both.
type readerDialect struct {
	// like is the case-insensitive match operator. SQLite's LIKE already
	// ignores case for ASCII; PostgreSQL's does not, and needs ILIKE.
	like string

	// idColumn and attemptIDColumn reference the primary key. A PostgreSQL
	// database created before the stores were unified still has UUID columns
	// there — CREATE TABLE IF NOT EXISTS did not reshape it — so both are cast
	// to text before comparing with a string.
	idColumn        string
	attemptIDColumn string

	// errorMessage, responseID and previousResponseID extract JSON fields.
	// The PostgreSQL spellings match the expressions jsonPathIndexes creates,
	// which is what lets the planner use those indexes.
	errorMessage       string
	responseID         string
	previousResponseID string

	// timestampBound converts a date-range boundary. SQLite compares the
	// column as text, so the boundary must be a prefix of the stored RFC3339
	// form: a full RFC3339 boundary would sort *after* a fractional-second
	// timestamp in the same second and pull the next day's first rows in.
	timestampBound func(time.Time) any

	// statsHour buckets a row into its UTC hour. SQLite's strftime also
	// normalises the stored timestamp variants (space separator, fractional
	// seconds, offsets) that its text column may hold.
	statsHour string
}

func readerDialectFor(dialect sqlx.Dialect) readerDialect {
	if dialect == sqlx.PostgreSQL {
		return readerDialect{
			like:               "ILIKE",
			idColumn:           "id::text",
			attemptIDColumn:    "audit_log_id::text",
			errorMessage:       `data->>'error_message'`,
			responseID:         `data #>> '{response_body,id}'`,
			previousResponseID: `data #>> '{request_body,previous_response_id}'`,
			timestampBound:     func(t time.Time) any { return t.UTC() },
			statsHour:          `date_trunc('hour', timestamp AT TIME ZONE 'UTC')`,
		}
	}
	return readerDialect{
		like:               "LIKE",
		idColumn:           "id",
		attemptIDColumn:    "audit_log_id",
		errorMessage:       `json_extract(data, '$.error_message')`,
		responseID:         `json_extract(data, '$.response_body.id')`,
		previousResponseID: `json_extract(data, '$.request_body.previous_response_id')`,
		timestampBound:     func(t time.Time) any { return t.UTC().Format(sqliteTimestampBoundaryLayout) },
		statsHour:          `strftime('%Y-%m-%dT%H', REPLACE(timestamp, ' ', 'T'))`,
	}
}

const sqliteTimestampBoundaryLayout = "2006-01-02T15:04:05"

const selectLogColumns = `SELECT id, timestamp, duration_ns, requested_model, resolved_model,
	provider, provider_name, alias_used, workflow_version_id, cache_type, status_code,
	request_id, auth_key_id, auth_method, client_ip, method, path, user_path, stream,
	error_type, data
	FROM audit_logs`

// GetLogs returns a paginated list of audit log entries.
func (r *SQLReader) GetLogs(ctx context.Context, params LogQueryParams) (*LogListResult, error) {
	limit, offset := clampLimitOffset(params.Limit, params.Offset)

	conditions, args, err := r.logFilters(params)
	if err != nil {
		return nil, err
	}
	where := sqlutil.BuildWhereClause(conditions)

	var total int
	if err := r.db.QueryRow(ctx, "SELECT COUNT(*) FROM audit_logs"+where, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count audit log entries: %w", err)
	}

	rows, err := r.db.Query(ctx,
		selectLogColumns+where+" ORDER BY timestamp DESC LIMIT ? OFFSET ?",
		append(append([]any(nil), args...), limit, offset)...)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit logs: %w", err)
	}
	defer rows.Close()

	entries := make([]LogEntry, 0)
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
	return &LogListResult{Entries: entries, Total: total, Limit: limit, Offset: offset}, nil
}

// logFilters builds the WHERE conditions for a log query. Placeholders are
// written as `?` throughout; the adapter renumbers them for PostgreSQL.
func (r *SQLReader) logFilters(params LogQueryParams) ([]string, []any, error) {
	userPath, err := normalizeAuditUserPathFilter(params.UserPath)
	if err != nil {
		return nil, nil, err
	}

	var conditions []string
	var args []any
	add := func(condition string, values ...any) {
		conditions = append(conditions, condition)
		args = append(args, values...)
	}
	contains := func(value string) string {
		return "%" + sqlutil.EscapeLikeWildcards(value) + "%"
	}

	if !params.StartDate.IsZero() {
		add("timestamp >= ?", r.dialect.timestampBound(params.StartDate))
	}
	if !params.EndDate.IsZero() {
		add("timestamp < ?", r.dialect.timestampBound(params.EndDate.AddDate(0, 0, 1)))
	}
	if params.RequestedModel != "" {
		add(r.likeClause("requested_model"), contains(params.RequestedModel))
	}
	if params.Provider != "" {
		add("("+r.likeClause("provider")+" OR "+r.likeClause("provider_name")+")",
			contains(params.Provider), contains(params.Provider))
	}
	if params.Method != "" {
		add("method = ?", params.Method)
	}
	if params.Path != "" {
		add(r.likeClause("path"), contains(params.Path))
	}
	if userPath != "" {
		add(auditUserPathSQLPredicate(userPath, "user_path = ?", r.likeClause("user_path")),
			userPath, auditUserPathSubtreePattern(userPath))
	}
	if params.ErrorType != "" {
		add(r.likeClause("error_type"), contains(params.ErrorType))
	}
	if params.StatusCode != nil {
		add("status_code = ?", *params.StatusCode)
	}
	if params.Stream != nil {
		add("stream = ?", *params.Stream)
	}
	if params.Search != "" {
		searchColumns := []string{
			"request_id", "auth_key_id", "requested_model", "provider", "provider_name",
			"method", "path", "user_path", "error_type", r.dialect.errorMessage,
		}
		clauses := make([]string, 0, len(searchColumns))
		values := make([]any, 0, len(searchColumns))
		for _, column := range searchColumns {
			clauses = append(clauses, r.likeClause(column))
			values = append(values, contains(params.Search))
		}
		add("("+strings.Join(clauses, " OR ")+")", values...)
	}
	return conditions, args, nil
}

func (r *SQLReader) likeClause(column string) string {
	return column + " " + r.dialect.like + ` ? ESCAPE '\'`
}

// GetLogByID returns a single audit log entry by ID.
func (r *SQLReader) GetLogByID(ctx context.Context, id string) (*LogEntry, error) {
	return r.queryLogEntryWithAttempts(ctx,
		selectLogColumns+" WHERE "+r.dialect.idColumn+" = ? LIMIT 1", id)
}

// GetConversation returns a linear conversation thread around a seed log entry.
func (r *SQLReader) GetConversation(ctx context.Context, logID string, limit int) (*ConversationResult, error) {
	return buildConversationThread(ctx, logID, limit, r.GetLogByID, r.findByResponseID, r.findByPreviousResponseID)
}

func (r *SQLReader) findByResponseID(ctx context.Context, responseID string) (*LogEntry, error) {
	return r.queryLogEntryWithAttempts(ctx,
		selectLogColumns+" WHERE "+r.dialect.responseID+" = ? ORDER BY timestamp ASC LIMIT 1", responseID)
}

func (r *SQLReader) findByPreviousResponseID(ctx context.Context, previousResponseID string) (*LogEntry, error) {
	return r.queryLogEntryWithAttempts(ctx,
		selectLogColumns+" WHERE "+r.dialect.previousResponseID+" = ? ORDER BY timestamp ASC LIMIT 1", previousResponseID)
}

// queryLogEntryWithAttempts runs a single-row audit log query, scans the entry,
// and hydrates its provider attempts. Returns (nil, nil) when no row matches.
func (r *SQLReader) queryLogEntryWithAttempts(ctx context.Context, query, arg string) (*LogEntry, error) {
	rows, err := r.db.Query(ctx, query, arg)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit log: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("failed to read audit log row: %w", err)
		}
		return nil, nil
	}
	entry, err := scanSQLLogEntry(rows)
	if err != nil {
		return nil, err
	}
	rows.Close()

	hydrated := []LogEntry{*entry}
	if err := r.loadAttempts(ctx, hydrated); err != nil {
		return nil, err
	}
	*entry = hydrated[0]
	return entry, nil
}

func (r *SQLReader) loadAttempts(ctx context.Context, entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// Batch all entries into a single query keyed by audit_log_id to avoid an
	// N+1 read (one query per returned log) when hydrating a page of entries.
	// A page is capped at 100 rows, well inside SQLite's parameter limit.
	ids := make([]any, len(entries))
	index := make(map[string]int, len(entries))
	for i := range entries {
		ids[i] = entries[i].ID
		index[entries[i].ID] = i
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")

	rows, err := r.db.Query(ctx, fmt.Sprintf(`
		SELECT %s, seq, kind, provider_type, provider_name, model, status_code, success,
			error_type, error_code, error_message, response_body, response_headers, started_at, duration_ns
		FROM audit_log_attempts
		WHERE %s IN (%s)
		ORDER BY audit_log_id ASC, seq ASC
	`, r.dialect.attemptIDColumn, r.dialect.attemptIDColumn, placeholders), ids...)
	if err != nil {
		// A database written before attempts existed has no such table; its
		// logs simply carry no attempts.
		if isMissingAuditAttemptsTable(err) {
			return nil
		}
		return fmt.Errorf("failed to query audit log attempts: %w", err)
	}
	defer rows.Close()

	grouped := make(map[string][]AttemptSnapshot, len(entries))
	for rows.Next() {
		auditLogID, attempt, err := scanSQLAttempt(rows)
		if err != nil {
			return err
		}
		grouped[auditLogID] = append(grouped[auditLogID], attempt)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating audit log attempts: %w", err)
	}

	for id, attempts := range grouped {
		if i, ok := index[id]; ok && len(attempts) > 0 {
			ensureLogData(&entries[i]).Attempts = normalizeAttemptSnapshots(attempts)
		}
	}
	return nil
}

func scanSQLLogEntry(scanner sqlx.Row) (*LogEntry, error) {
	var (
		entry             LogEntry
		timestamp         sqlx.Timestamp
		providerName      *string
		workflowVersionID *string
		cacheType         *string
		authKeyID         *string
		authMethod        *string
		userPath          *string
		errorType         *string
		dataJSON          *string
	)

	if err := scanner.Scan(
		&entry.ID, &timestamp, &entry.DurationNs, &entry.RequestedModel, &entry.ResolvedModel,
		&entry.Provider, &providerName, &entry.AliasUsed, &workflowVersionID, &cacheType,
		&entry.StatusCode, &entry.RequestID, &authKeyID, &authMethod, &entry.ClientIP,
		&entry.Method, &entry.Path, &userPath, &entry.Stream, &errorType, &dataJSON,
	); err != nil {
		return nil, fmt.Errorf("failed to scan audit log row: %w", err)
	}

	if !timestamp.Valid && timestamp.Raw != "" {
		slog.Warn("failed to parse audit timestamp", "id", entry.ID, "raw_timestamp", timestamp.Raw)
	}
	entry.Timestamp = timestamp.Time
	entry.WorkflowVersionID = sqlutil.DerefTrimmed(workflowVersionID)
	entry.AuthKeyID = derefString(authKeyID)
	entry.AuthMethod = derefString(authMethod)
	entry.UserPath = derefString(userPath)
	entry.ErrorType = derefString(errorType)
	entry.CacheType = normalizeCacheType(derefString(cacheType))
	entry.ProviderName = displayAuditProviderName(derefString(providerName), entry.Provider)

	if dataJSON != nil && *dataJSON != "" {
		var data LogData
		if err := json.Unmarshal([]byte(*dataJSON), &data); err != nil {
			slog.Warn("failed to unmarshal audit data JSON", "id", entry.ID, "error", err)
		} else {
			entry.Data = &data
		}
	}
	return &entry, nil
}

func scanSQLAttempt(scanner sqlx.Row) (string, AttemptSnapshot, error) {
	var (
		auditLogID      string
		attempt         AttemptSnapshot
		providerType    *string
		providerName    *string
		model           *string
		errorType       *string
		errorCode       *string
		errorMessage    *string
		responseBody    *string
		responseHeaders *string
		startedAt       sqlx.Timestamp
	)

	if err := scanner.Scan(
		&auditLogID, &attempt.Seq, &attempt.Kind, &providerType, &providerName, &model,
		&attempt.StatusCode, &attempt.Success, &errorType, &errorCode, &errorMessage,
		&responseBody, &responseHeaders, &startedAt, &attempt.DurationNs,
	); err != nil {
		return "", AttemptSnapshot{}, fmt.Errorf("failed to scan audit log attempt: %w", err)
	}

	attempt.ProviderType = derefString(providerType)
	attempt.ProviderName = derefString(providerName)
	attempt.Model = derefString(model)
	attempt.ErrorType = derefString(errorType)
	attempt.ErrorCode = derefString(errorCode)
	attempt.ErrorMessage = derefString(errorMessage)
	attempt.ResponseBody = unmarshalAttemptBody(responseBody)
	attempt.ResponseHeaders = unmarshalAttemptHeaders(responseHeaders)
	if !startedAt.Valid && startedAt.Raw != "" {
		slog.Warn("failed to parse audit attempt timestamp", "id", auditLogID, "raw_timestamp", startedAt.Raw)
	}
	attempt.StartedAt = startedAt.Time
	return auditLogID, attempt, nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func isMissingAuditAttemptsTable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "audit_log_attempts") &&
		(strings.Contains(message, "no such table") || strings.Contains(message, "does not exist"))
}
