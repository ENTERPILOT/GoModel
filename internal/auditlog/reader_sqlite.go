package auditlog

import (
	"gomodel/internal/storage/sqlutil"

	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-json"
)

const sqliteTimestampBoundaryLayout = "2006-01-02T15:04:05"

// SQLiteReader implements Reader for SQLite databases.
type SQLiteReader struct {
	db *sql.DB
}

// NewSQLiteReader creates a new SQLite audit log reader.
func NewSQLiteReader(db *sql.DB) (*SQLiteReader, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	return &SQLiteReader{db: db}, nil
}

// GetLogs returns a paginated list of audit log entries.
func (r *SQLiteReader) GetLogs(ctx context.Context, params LogQueryParams) (*LogListResult, error) {
	limit, offset := clampLimitOffset(params.Limit, params.Offset)

	conditions, args := sqliteDateRangeConditions(params.QueryParams)
	userPath, err := normalizeAuditUserPathFilter(params.UserPath)
	if err != nil {
		return nil, err
	}

	if params.RequestedModel != "" {
		conditions = append(conditions, "requested_model LIKE ? ESCAPE '\\'")
		args = append(args, "%"+sqlutil.EscapeLikeWildcards(params.RequestedModel)+"%")
	}
	if params.Provider != "" {
		conditions = append(conditions, "(provider LIKE ? ESCAPE '\\' OR provider_name LIKE ? ESCAPE '\\')")
		args = append(args, "%"+sqlutil.EscapeLikeWildcards(params.Provider)+"%", "%"+sqlutil.EscapeLikeWildcards(params.Provider)+"%")
	}
	if params.Method != "" {
		conditions = append(conditions, "method = ?")
		args = append(args, params.Method)
	}
	if params.Path != "" {
		conditions = append(conditions, "path LIKE ? ESCAPE '\\'")
		args = append(args, "%"+sqlutil.EscapeLikeWildcards(params.Path)+"%")
	}
	if userPath != "" {
		conditions = append(conditions, auditUserPathSQLPredicate(userPath, "user_path = ?", "user_path LIKE ? ESCAPE '\\'"))
		args = append(args, userPath, auditUserPathSubtreePattern(userPath))
	}
	if params.ErrorType != "" {
		conditions = append(conditions, "error_type LIKE ? ESCAPE '\\'")
		args = append(args, "%"+sqlutil.EscapeLikeWildcards(params.ErrorType)+"%")
	}
	if params.StatusCode != nil {
		conditions = append(conditions, "status_code = ?")
		args = append(args, *params.StatusCode)
	}
	if params.Stream != nil {
		conditions = append(conditions, "stream = ?")
		if *params.Stream {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	if params.Search != "" {
		s := "%" + sqlutil.EscapeLikeWildcards(params.Search) + "%"
		conditions = append(conditions, `(request_id LIKE ? ESCAPE '\' OR auth_key_id LIKE ? ESCAPE '\' OR requested_model LIKE ? ESCAPE '\' OR provider LIKE ? ESCAPE '\' OR provider_name LIKE ? ESCAPE '\' OR method LIKE ? ESCAPE '\' OR path LIKE ? ESCAPE '\' OR user_path LIKE ? ESCAPE '\' OR error_type LIKE ? ESCAPE '\' OR json_extract(data, '$.error_message') LIKE ? ESCAPE '\')`)
		args = append(args, s, s, s, s, s, s, s, s, s, s)
	}

	where := sqlutil.BuildWhereClause(conditions)

	// Count total
	var total int
	countQuery := "SELECT COUNT(*) FROM audit_logs" + where
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count audit log entries: %w", err)
	}

	dataQuery := `SELECT id, timestamp, duration_ns, requested_model, resolved_model, provider, provider_name, alias_used, workflow_version_id, cache_type, status_code, request_id, auth_key_id, auth_method,
		client_ip, method, path, user_path, stream, error_type, data
		FROM audit_logs` + where + ` ORDER BY timestamp DESC LIMIT ? OFFSET ?`
	dataArgs := append(append([]any(nil), args...), limit, offset)

	rows, err := r.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit logs: %w", err)
	}
	defer rows.Close()

	entries := make([]LogEntry, 0)
	for rows.Next() {
		var e LogEntry
		var ts string
		var providerName sql.NullString
		var aliasUsedInt int
		var streamInt int
		var dataJSON *string
		var workflowVersionID sql.NullString
		var cacheType sql.NullString
		var authKeyID sql.NullString
		var authMethod sql.NullString
		var userPath sql.NullString
		var errorType sql.NullString

		if err := rows.Scan(&e.ID, &ts, &e.DurationNs, &e.RequestedModel, &e.ResolvedModel, &e.Provider, &providerName, &aliasUsedInt, &workflowVersionID, &cacheType, &e.StatusCode,
			&e.RequestID, &authKeyID, &authMethod, &e.ClientIP, &e.Method, &e.Path, &userPath, &streamInt, &errorType, &dataJSON); err != nil {
			return nil, fmt.Errorf("failed to scan audit log row: %w", err)
		}

		e.AliasUsed = aliasUsedInt == 1
		e.Stream = streamInt == 1
		e.Timestamp = parseSQLTimestamp(ts, e.ID)
		if workflowVersionID.Valid {
			e.WorkflowVersionID = workflowVersionID.String
		}
		if authKeyID.Valid {
			e.AuthKeyID = authKeyID.String
		}
		if authMethod.Valid {
			e.AuthMethod = authMethod.String
		}
		if cacheType.Valid {
			e.CacheType = normalizeCacheType(cacheType.String)
		}
		if providerName.Valid {
			e.ProviderName = displayAuditProviderName(providerName.String, e.Provider)
		} else {
			e.ProviderName = displayAuditProviderName("", e.Provider)
		}
		if userPath.Valid {
			e.UserPath = userPath.String
		}
		if errorType.Valid {
			e.ErrorType = errorType.String
		}

		if dataJSON != nil && *dataJSON != "" {
			var data LogData
			if err := json.Unmarshal([]byte(*dataJSON), &data); err != nil {
				slog.Warn("failed to unmarshal audit data JSON", "id", e.ID, "error", err)
			} else {
				e.Data = &data
			}
		}

		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating audit log rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("failed to close audit log rows: %w", err)
	}
	if err := r.loadAttempts(ctx, entries); err != nil {
		return nil, err
	}

	return &LogListResult{
		Entries: entries,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	}, nil
}

// queryLogEntryWithAttempts runs a single-row audit log query, scans the entry,
// and hydrates its provider attempts. Returns (nil, nil) when no row matches.
func (r *SQLiteReader) queryLogEntryWithAttempts(ctx context.Context, query, arg string) (*LogEntry, error) {
	rows, err := r.db.QueryContext(ctx, query, arg)
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
	entry, err := scanSQLiteLogEntry(rows)
	if err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("failed to close audit log row: %w", err)
	}
	hydrated := []LogEntry{*entry}
	if err := r.loadAttempts(ctx, hydrated); err != nil {
		return nil, err
	}
	*entry = hydrated[0]
	return entry, nil
}

// GetLogByID returns a single audit log entry by ID.
func (r *SQLiteReader) GetLogByID(ctx context.Context, id string) (*LogEntry, error) {
	return r.queryLogEntryWithAttempts(ctx, `SELECT id, timestamp, duration_ns, requested_model, resolved_model, provider, provider_name, alias_used, workflow_version_id, cache_type, status_code, request_id, auth_key_id, auth_method,
		client_ip, method, path, user_path, stream, error_type, data
		FROM audit_logs WHERE id = ? LIMIT 1`, id)
}

// GetConversation returns a linear conversation thread around a seed log entry.
func (r *SQLiteReader) GetConversation(ctx context.Context, logID string, limit int) (*ConversationResult, error) {
	limit = clampConversationLimit(limit)

	anchor, err := r.GetLogByID(ctx, logID)
	if err != nil {
		return nil, err
	}
	if anchor == nil {
		return &ConversationResult{
			AnchorID: logID,
			Entries:  []LogEntry{},
		}, nil
	}

	thread := []*LogEntry{anchor}
	seen := map[string]struct{}{anchor.ID: {}}

	// Walk backwards through previous_response_id links.
	current := anchor
	for len(thread) < limit {
		prevID := extractPreviousResponseID(current)
		if prevID == "" {
			break
		}
		parent, err := r.findByResponseID(ctx, prevID)
		if err != nil {
			return nil, err
		}
		if parent == nil {
			break
		}
		if _, ok := seen[parent.ID]; ok {
			break
		}
		thread = append(thread, parent)
		seen[parent.ID] = struct{}{}
		current = parent
	}

	// Walk forwards via entries whose previous_response_id points to current response id.
	current = anchor
	for len(thread) < limit {
		respID := extractResponseID(current)
		if respID == "" {
			break
		}
		child, err := r.findByPreviousResponseID(ctx, respID)
		if err != nil {
			return nil, err
		}
		if child == nil {
			break
		}
		if _, ok := seen[child.ID]; ok {
			break
		}
		thread = append(thread, child)
		seen[child.ID] = struct{}{}
		current = child
	}

	sort.Slice(thread, func(i, j int) bool {
		return thread[i].Timestamp.Before(thread[j].Timestamp)
	})

	entries := make([]LogEntry, 0, len(thread))
	for _, entry := range thread {
		if entry != nil {
			entries = append(entries, *entry)
		}
	}

	return &ConversationResult{
		AnchorID: anchor.ID,
		Entries:  entries,
	}, nil
}

func sqliteDateRangeConditions(params QueryParams) (conditions []string, args []any) {
	if !params.StartDate.IsZero() {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, sqliteTimestampBoundary(params.StartDate))
	}
	if !params.EndDate.IsZero() {
		conditions = append(conditions, "timestamp < ?")
		args = append(args, sqliteTimestampBoundary(params.EndDate.AddDate(0, 0, 1)))
	}
	return conditions, args
}

func sqliteTimestampBoundary(t time.Time) string {
	return t.UTC().Format(sqliteTimestampBoundaryLayout)
}

func parseSQLTimestamp(ts string, entryID string) time.Time {
	t, ok := sqlutil.ParseSQLiteTimestamp(ts)
	if !ok {
		slog.Warn("failed to parse audit timestamp", "id", entryID, "raw_timestamp", ts)
	}
	return t
}

func (r *SQLiteReader) findByResponseID(ctx context.Context, responseID string) (*LogEntry, error) {
	return r.queryLogEntryWithAttempts(ctx, `SELECT id, timestamp, duration_ns, requested_model, resolved_model, provider, provider_name, alias_used, workflow_version_id, cache_type, status_code, request_id, auth_key_id, auth_method,
		client_ip, method, path, user_path, stream, error_type, data
		FROM audit_logs
		WHERE json_extract(data, '$.response_body.id') = ?
		ORDER BY timestamp ASC
		LIMIT 1`, responseID)
}

func (r *SQLiteReader) findByPreviousResponseID(ctx context.Context, previousResponseID string) (*LogEntry, error) {
	return r.queryLogEntryWithAttempts(ctx, `SELECT id, timestamp, duration_ns, requested_model, resolved_model, provider, provider_name, alias_used, workflow_version_id, cache_type, status_code, request_id, auth_key_id, auth_method,
		client_ip, method, path, user_path, stream, error_type, data
		FROM audit_logs
		WHERE json_extract(data, '$.request_body.previous_response_id') = ?
		ORDER BY timestamp ASC
		LIMIT 1`, previousResponseID)
}

func (r *SQLiteReader) loadAttempts(ctx context.Context, entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// Batch all entries into a single query keyed by audit_log_id to avoid an
	// N+1 read (one query per returned log) when hydrating a page of entries.
	ids := make([]any, len(entries))
	index := make(map[string]int, len(entries))
	for i := range entries {
		ids[i] = entries[i].ID
		index[entries[i].ID] = i
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT audit_log_id, seq, kind, provider_type, provider_name, model, status_code, success,
			error_type, error_code, error_message, response_body, response_headers, started_at, duration_ns
		FROM audit_log_attempts
		WHERE audit_log_id IN (%s)
		ORDER BY audit_log_id ASC, seq ASC
	`, placeholders), ids...)
	if err != nil {
		if isMissingSQLiteAuditAttemptsTable(err) {
			return nil
		}
		return fmt.Errorf("failed to query audit log attempts: %w", err)
	}
	defer rows.Close()

	grouped := make(map[string][]AttemptSnapshot, len(entries))
	for rows.Next() {
		var auditLogID string
		var attempt AttemptSnapshot
		var providerType, providerName, model sql.NullString
		var errorType, errorCode, errorMessage sql.NullString
		var responseBody, responseHeaders sql.NullString
		var startedAt sql.NullString
		var successInt int
		if err := rows.Scan(
			&auditLogID,
			&attempt.Seq,
			&attempt.Kind,
			&providerType,
			&providerName,
			&model,
			&attempt.StatusCode,
			&successInt,
			&errorType,
			&errorCode,
			&errorMessage,
			&responseBody,
			&responseHeaders,
			&startedAt,
			&attempt.DurationNs,
		); err != nil {
			return fmt.Errorf("failed to scan audit log attempt: %w", err)
		}
		attempt.Success = successInt == 1
		if responseBody.Valid {
			attempt.ResponseBody = unmarshalAttemptBody(&responseBody.String)
		}
		if responseHeaders.Valid {
			attempt.ResponseHeaders = unmarshalAttemptHeaders(&responseHeaders.String)
		}
		if providerType.Valid {
			attempt.ProviderType = providerType.String
		}
		if providerName.Valid {
			attempt.ProviderName = providerName.String
		}
		if model.Valid {
			attempt.Model = model.String
		}
		if errorType.Valid {
			attempt.ErrorType = errorType.String
		}
		if errorCode.Valid {
			attempt.ErrorCode = errorCode.String
		}
		if errorMessage.Valid {
			attempt.ErrorMessage = errorMessage.String
		}
		if startedAt.Valid {
			attempt.StartedAt = parseSQLTimestamp(startedAt.String, auditLogID)
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

func scanSQLiteLogEntry(rows *sql.Rows) (*LogEntry, error) {
	var e LogEntry
	var ts string
	var providerName sql.NullString
	var aliasUsedInt int
	var streamInt int
	var dataJSON *string
	var workflowVersionID sql.NullString
	var cacheType sql.NullString
	var authKeyID sql.NullString
	var authMethod sql.NullString
	var userPath sql.NullString
	var errorType sql.NullString

	if err := rows.Scan(&e.ID, &ts, &e.DurationNs, &e.RequestedModel, &e.ResolvedModel, &e.Provider, &providerName, &aliasUsedInt, &workflowVersionID, &cacheType, &e.StatusCode,
		&e.RequestID, &authKeyID, &authMethod, &e.ClientIP, &e.Method, &e.Path, &userPath, &streamInt, &errorType, &dataJSON); err != nil {
		return nil, fmt.Errorf("failed to scan audit log row: %w", err)
	}

	e.AliasUsed = aliasUsedInt == 1
	e.Stream = streamInt == 1
	e.Timestamp = parseSQLTimestamp(ts, e.ID)
	if workflowVersionID.Valid {
		e.WorkflowVersionID = workflowVersionID.String
	}
	if authKeyID.Valid {
		e.AuthKeyID = authKeyID.String
	}
	if authMethod.Valid {
		e.AuthMethod = authMethod.String
	}
	if cacheType.Valid {
		e.CacheType = normalizeCacheType(cacheType.String)
	}
	if providerName.Valid {
		e.ProviderName = displayAuditProviderName(providerName.String, e.Provider)
	} else {
		e.ProviderName = displayAuditProviderName("", e.Provider)
	}
	if userPath.Valid {
		e.UserPath = userPath.String
	}
	if errorType.Valid {
		e.ErrorType = errorType.String
	}

	if dataJSON != nil && *dataJSON != "" {
		var data LogData
		if err := json.Unmarshal([]byte(*dataJSON), &data); err != nil {
			slog.Warn("failed to unmarshal audit data JSON", "id", e.ID, "error", err)
		} else {
			e.Data = &data
		}
	}

	return &e, nil
}

func isMissingSQLiteAuditAttemptsTable(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table: audit_log_attempts")
}
