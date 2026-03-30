package auditlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"
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

	if params.Model != "" {
		conditions = append(conditions, "model LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLikeWildcards(params.Model)+"%")
	}
	if params.Provider != "" {
		conditions = append(conditions, "provider LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLikeWildcards(params.Provider)+"%")
	}
	if params.Method != "" {
		conditions = append(conditions, "method = ?")
		args = append(args, params.Method)
	}
	if params.Path != "" {
		conditions = append(conditions, "path LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLikeWildcards(params.Path)+"%")
	}
	if userPath != "" {
		conditions = append(conditions, "(user_path = ? OR user_path LIKE ? ESCAPE '\\')")
		args = append(args, userPath, auditUserPathSubtreePattern(userPath))
	}
	if params.ErrorType != "" {
		conditions = append(conditions, "error_type LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLikeWildcards(params.ErrorType)+"%")
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
		s := "%" + escapeLikeWildcards(params.Search) + "%"
		conditions = append(conditions, `(request_id LIKE ? ESCAPE '\' OR model LIKE ? ESCAPE '\' OR provider LIKE ? ESCAPE '\' OR method LIKE ? ESCAPE '\' OR path LIKE ? ESCAPE '\' OR error_type LIKE ? ESCAPE '\')`)
		args = append(args, s, s, s, s, s, s)
	}

	where := buildWhereClause(conditions)

	// Count total
	var total int
	countQuery := "SELECT COUNT(*) FROM audit_logs" + where
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count audit log entries: %w", err)
	}

	dataQuery := `SELECT id, timestamp, duration_ns, model, resolved_model, provider, alias_used, execution_plan_version_id, status_code, request_id,
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
		var aliasUsedInt int
		var streamInt int
		var dataJSON *string
		var executionPlanVersionID sql.NullString
		var userPath sql.NullString

		if err := rows.Scan(&e.ID, &ts, &e.DurationNs, &e.Model, &e.ResolvedModel, &e.Provider, &aliasUsedInt, &executionPlanVersionID, &e.StatusCode,
			&e.RequestID, &e.ClientIP, &e.Method, &e.Path, &userPath, &streamInt, &e.ErrorType, &dataJSON); err != nil {
			return nil, fmt.Errorf("failed to scan audit log row: %w", err)
		}

		e.AliasUsed = aliasUsedInt == 1
		e.Stream = streamInt == 1
		e.Timestamp = parseSQLTimestamp(ts, e.ID)
		if executionPlanVersionID.Valid {
			e.ExecutionPlanVersionID = executionPlanVersionID.String
		}
		if userPath.Valid {
			e.UserPath = userPath.String
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

	return &LogListResult{
		Entries: entries,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	}, nil
}

// GetLogByID returns a single audit log entry by ID.
func (r *SQLiteReader) GetLogByID(ctx context.Context, id string) (*LogEntry, error) {
	query := `SELECT id, timestamp, duration_ns, model, resolved_model, provider, alias_used, execution_plan_version_id, status_code, request_id,
		client_ip, method, path, user_path, stream, error_type, data
		FROM audit_logs WHERE id = ? LIMIT 1`

	rows, err := r.db.QueryContext(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit log by id: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, nil
	}

	entry, err := scanSQLiteLogEntry(rows)
	if err != nil {
		return nil, err
	}
	return entry, nil
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
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05.999999999-07:00", ts); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z", ts); err == nil {
		return t
	}

	slog.Warn("failed to parse audit timestamp", "id", entryID, "raw_timestamp", ts)
	return time.Time{}
}

func (r *SQLiteReader) findByResponseID(ctx context.Context, responseID string) (*LogEntry, error) {
	query := `SELECT id, timestamp, duration_ns, model, resolved_model, provider, alias_used, execution_plan_version_id, status_code, request_id,
		client_ip, method, path, user_path, stream, error_type, data
		FROM audit_logs
		WHERE json_extract(data, '$.response_body.id') = ?
		ORDER BY timestamp ASC
		LIMIT 1`

	rows, err := r.db.QueryContext(ctx, query, responseID)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit log by response id: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	return scanSQLiteLogEntry(rows)
}

func (r *SQLiteReader) findByPreviousResponseID(ctx context.Context, previousResponseID string) (*LogEntry, error) {
	query := `SELECT id, timestamp, duration_ns, model, resolved_model, provider, alias_used, execution_plan_version_id, status_code, request_id,
		client_ip, method, path, user_path, stream, error_type, data
		FROM audit_logs
		WHERE json_extract(data, '$.request_body.previous_response_id') = ?
		ORDER BY timestamp ASC
		LIMIT 1`

	rows, err := r.db.QueryContext(ctx, query, previousResponseID)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit log by previous_response_id: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	return scanSQLiteLogEntry(rows)
}

func scanSQLiteLogEntry(rows *sql.Rows) (*LogEntry, error) {
	var e LogEntry
	var ts string
	var aliasUsedInt int
	var streamInt int
	var dataJSON *string
	var executionPlanVersionID sql.NullString
	var userPath sql.NullString

	if err := rows.Scan(&e.ID, &ts, &e.DurationNs, &e.Model, &e.ResolvedModel, &e.Provider, &aliasUsedInt, &executionPlanVersionID, &e.StatusCode,
		&e.RequestID, &e.ClientIP, &e.Method, &e.Path, &userPath, &streamInt, &e.ErrorType, &dataJSON); err != nil {
		return nil, fmt.Errorf("failed to scan audit log row: %w", err)
	}

	e.AliasUsed = aliasUsedInt == 1
	e.Stream = streamInt == 1
	e.Timestamp = parseSQLTimestamp(ts, e.ID)
	if executionPlanVersionID.Valid {
		e.ExecutionPlanVersionID = executionPlanVersionID.String
	}
	if userPath.Valid {
		e.UserPath = userPath.String
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
