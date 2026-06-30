package auditlog

import (
	"gomodel/internal/storage/sqlutil"

	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/goccy/go-json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type postgreSQLQueryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PostgreSQLReader implements Reader for PostgreSQL databases.
type PostgreSQLReader struct {
	pool postgreSQLQueryer
}

// NewPostgreSQLReader creates a new PostgreSQL audit log reader.
func NewPostgreSQLReader(pool *pgxpool.Pool) (*PostgreSQLReader, error) {
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}
	return &PostgreSQLReader{pool: pool}, nil
}

// GetLogs returns a paginated list of audit log entries.
func (r *PostgreSQLReader) GetLogs(ctx context.Context, params LogQueryParams) (*LogListResult, error) {
	limit, offset := clampLimitOffset(params.Limit, params.Offset)

	conditions, args, argIdx := pgDateRangeConditions(params.QueryParams, 1)
	userPath, err := normalizeAuditUserPathFilter(params.UserPath)
	if err != nil {
		return nil, err
	}

	if params.RequestedModel != "" {
		conditions = append(conditions, fmt.Sprintf("requested_model ILIKE $%d ESCAPE '\\'", argIdx))
		args = append(args, "%"+sqlutil.EscapeLikeWildcards(params.RequestedModel)+"%")
		argIdx++
	}
	if params.Provider != "" {
		conditions = append(conditions, fmt.Sprintf("(provider ILIKE $%d ESCAPE '\\' OR provider_name ILIKE $%d ESCAPE '\\')", argIdx, argIdx+1))
		args = append(args, "%"+sqlutil.EscapeLikeWildcards(params.Provider)+"%", "%"+sqlutil.EscapeLikeWildcards(params.Provider)+"%")
		argIdx += 2
	}
	if params.Method != "" {
		conditions = append(conditions, fmt.Sprintf("method = $%d", argIdx))
		args = append(args, params.Method)
		argIdx++
	}
	if params.Path != "" {
		conditions = append(conditions, fmt.Sprintf("path ILIKE $%d ESCAPE '\\'", argIdx))
		args = append(args, "%"+sqlutil.EscapeLikeWildcards(params.Path)+"%")
		argIdx++
	}
	if userPath != "" {
		conditions = append(conditions, auditUserPathSQLPredicate(
			userPath,
			fmt.Sprintf("user_path = $%d", argIdx),
			fmt.Sprintf("user_path LIKE $%d ESCAPE '\\'", argIdx+1),
		))
		args = append(args, userPath, auditUserPathSubtreePattern(userPath))
		argIdx += 2
	}
	if params.ErrorType != "" {
		conditions = append(conditions, fmt.Sprintf("error_type ILIKE $%d ESCAPE '\\'", argIdx))
		args = append(args, "%"+sqlutil.EscapeLikeWildcards(params.ErrorType)+"%")
		argIdx++
	}
	if params.StatusCode != nil {
		conditions = append(conditions, fmt.Sprintf("status_code = $%d", argIdx))
		args = append(args, *params.StatusCode)
		argIdx++
	}
	if params.Stream != nil {
		conditions = append(conditions, fmt.Sprintf("stream = $%d", argIdx))
		args = append(args, *params.Stream)
		argIdx++
	}
	if params.Search != "" {
		s := "%" + sqlutil.EscapeLikeWildcards(params.Search) + "%"
		conditions = append(conditions, fmt.Sprintf("(request_id ILIKE $%d ESCAPE '\\' OR auth_key_id ILIKE $%d ESCAPE '\\' OR requested_model ILIKE $%d ESCAPE '\\' OR provider ILIKE $%d ESCAPE '\\' OR provider_name ILIKE $%d ESCAPE '\\' OR method ILIKE $%d ESCAPE '\\' OR path ILIKE $%d ESCAPE '\\' OR user_path ILIKE $%d ESCAPE '\\' OR error_type ILIKE $%d ESCAPE '\\' OR data->>'error_message' ILIKE $%d ESCAPE '\\')", argIdx, argIdx, argIdx, argIdx, argIdx, argIdx, argIdx, argIdx, argIdx, argIdx))
		args = append(args, s)
		argIdx++
	}

	where := sqlutil.BuildWhereClause(conditions)

	var total int
	countQuery := `SELECT COUNT(*) FROM audit_logs` + where
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count audit log entries: %w", err)
	}

	dataQuery := fmt.Sprintf(`SELECT id, timestamp, duration_ns, requested_model, resolved_model, provider, provider_name, alias_used, workflow_version_id, cache_type, status_code, request_id, auth_key_id, auth_method,
		client_ip, method, path, user_path, stream, error_type, data
		FROM audit_logs%s ORDER BY timestamp DESC LIMIT $%d OFFSET $%d`, where, argIdx, argIdx+1)
	dataArgs := append(append([]any(nil), args...), limit, offset)

	rows, err := r.pool.Query(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit logs: %w", err)
	}
	defer rows.Close()

	entries := make([]LogEntry, 0)
	for rows.Next() {
		var e LogEntry
		var dataJSON *string
		var providerName *string
		var workflowVersionID *string
		var cacheType *string
		var authKeyID *string
		var authMethod *string
		var userPath *string
		var errorType *string

		if err := rows.Scan(&e.ID, &e.Timestamp, &e.DurationNs, &e.RequestedModel, &e.ResolvedModel, &e.Provider, &providerName, &e.AliasUsed, &workflowVersionID, &cacheType, &e.StatusCode,
			&e.RequestID, &authKeyID, &authMethod, &e.ClientIP, &e.Method, &e.Path, &userPath, &e.Stream, &errorType, &dataJSON); err != nil {
			return nil, fmt.Errorf("failed to scan audit log row: %w", err)
		}
		if workflowVersionID != nil {
			e.WorkflowVersionID = *workflowVersionID
		}
		if authKeyID != nil {
			e.AuthKeyID = *authKeyID
		}
		if authMethod != nil {
			e.AuthMethod = *authMethod
		}
		if cacheType != nil {
			e.CacheType = normalizeCacheType(*cacheType)
		}
		if providerName != nil {
			e.ProviderName = displayAuditProviderName(*providerName, e.Provider)
		} else {
			e.ProviderName = displayAuditProviderName("", e.Provider)
		}
		if userPath != nil {
			e.UserPath = *userPath
		}
		if errorType != nil {
			e.ErrorType = *errorType
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
	rows.Close()
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
func (r *PostgreSQLReader) queryLogEntryWithAttempts(ctx context.Context, query, arg string) (*LogEntry, error) {
	rows, err := r.pool.Query(ctx, query, arg)
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
	entry, err := scanPostgreSQLLogEntry(rows)
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

// GetLogByID returns a single audit log entry by ID.
func (r *PostgreSQLReader) GetLogByID(ctx context.Context, id string) (*LogEntry, error) {
	return r.queryLogEntryWithAttempts(ctx, `SELECT id, timestamp, duration_ns, requested_model, resolved_model, provider, provider_name, alias_used, workflow_version_id, cache_type, status_code, request_id, auth_key_id, auth_method,
		client_ip, method, path, user_path, stream, error_type, data
		FROM audit_logs WHERE id::text = $1 LIMIT 1`, id)
}

// GetConversation returns a linear conversation thread around a seed log entry.
func (r *PostgreSQLReader) GetConversation(ctx context.Context, logID string, limit int) (*ConversationResult, error) {
	return buildConversationThread(ctx, logID, limit, r.GetLogByID, r.findByResponseID, r.findByPreviousResponseID)
}

func pgDateRangeConditions(params QueryParams, argIdx int) (conditions []string, args []any, nextIdx int) {
	nextIdx = argIdx
	if !params.StartDate.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp >= $%d", nextIdx))
		args = append(args, params.StartDate.UTC())
		nextIdx++
	}
	if !params.EndDate.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp < $%d", nextIdx))
		args = append(args, params.EndDate.AddDate(0, 0, 1).UTC())
		nextIdx++
	}
	return conditions, args, nextIdx
}

func (r *PostgreSQLReader) findByResponseID(ctx context.Context, responseID string) (*LogEntry, error) {
	return r.queryLogEntryWithAttempts(ctx, `SELECT id, timestamp, duration_ns, requested_model, resolved_model, provider, provider_name, alias_used, workflow_version_id, cache_type, status_code, request_id, auth_key_id, auth_method,
		client_ip, method, path, user_path, stream, error_type, data
		FROM audit_logs
		WHERE data->'response_body'->>'id' = $1
		ORDER BY timestamp ASC
		LIMIT 1`, responseID)
}

func (r *PostgreSQLReader) findByPreviousResponseID(ctx context.Context, previousResponseID string) (*LogEntry, error) {
	return r.queryLogEntryWithAttempts(ctx, `SELECT id, timestamp, duration_ns, requested_model, resolved_model, provider, provider_name, alias_used, workflow_version_id, cache_type, status_code, request_id, auth_key_id, auth_method,
		client_ip, method, path, user_path, stream, error_type, data
		FROM audit_logs
		WHERE data->'request_body'->>'previous_response_id' = $1
		ORDER BY timestamp ASC
		LIMIT 1`, previousResponseID)
}

func (r *PostgreSQLReader) loadAttempts(ctx context.Context, entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// Batch all entries into a single query keyed by audit_log_id to avoid an
	// N+1 read (one query per returned log) when hydrating a page of entries.
	ids := make([]string, len(entries))
	index := make(map[string]int, len(entries))
	for i := range entries {
		ids[i] = entries[i].ID
		index[entries[i].ID] = i
	}
	rows, err := r.pool.Query(ctx, `
		SELECT audit_log_id::text, seq, kind, provider_type, provider_name, model, status_code, success,
			error_type, error_code, error_message, response_body, response_headers, started_at, duration_ns
		FROM audit_log_attempts
		WHERE audit_log_id::text = ANY($1)
		ORDER BY audit_log_id ASC, seq ASC
	`, ids)
	if err != nil {
		return fmt.Errorf("failed to query audit log attempts: %w", err)
	}
	defer rows.Close()

	grouped := make(map[string][]AttemptSnapshot, len(entries))
	for rows.Next() {
		var auditLogID string
		var attempt AttemptSnapshot
		var providerType, providerName, model *string
		var errorType, errorCode, errorMessage *string
		var responseBody, responseHeaders *string
		var startedAt *time.Time
		if err := rows.Scan(
			&auditLogID,
			&attempt.Seq,
			&attempt.Kind,
			&providerType,
			&providerName,
			&model,
			&attempt.StatusCode,
			&attempt.Success,
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
		attempt.ResponseBody = unmarshalAttemptBody(responseBody)
		attempt.ResponseHeaders = unmarshalAttemptHeaders(responseHeaders)
		if providerType != nil {
			attempt.ProviderType = *providerType
		}
		if providerName != nil {
			attempt.ProviderName = *providerName
		}
		if model != nil {
			attempt.Model = *model
		}
		if errorType != nil {
			attempt.ErrorType = *errorType
		}
		if errorCode != nil {
			attempt.ErrorCode = *errorCode
		}
		if errorMessage != nil {
			attempt.ErrorMessage = *errorMessage
		}
		if startedAt != nil {
			attempt.StartedAt = *startedAt
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

func scanPostgreSQLLogEntry(rows interface {
	Scan(dest ...any) error
}) (*LogEntry, error) {
	var e LogEntry
	var dataJSON *string
	var providerName *string
	var workflowVersionID *string
	var cacheType *string
	var authKeyID *string
	var authMethod *string
	var userPath *string
	var errorType *string

	if err := rows.Scan(&e.ID, &e.Timestamp, &e.DurationNs, &e.RequestedModel, &e.ResolvedModel, &e.Provider, &providerName, &e.AliasUsed, &workflowVersionID, &cacheType, &e.StatusCode,
		&e.RequestID, &authKeyID, &authMethod, &e.ClientIP, &e.Method, &e.Path, &userPath, &e.Stream, &errorType, &dataJSON); err != nil {
		return nil, fmt.Errorf("failed to scan audit log row: %w", err)
	}
	if workflowVersionID != nil {
		e.WorkflowVersionID = *workflowVersionID
	}
	if authKeyID != nil {
		e.AuthKeyID = *authKeyID
	}
	if authMethod != nil {
		e.AuthMethod = *authMethod
	}
	if cacheType != nil {
		e.CacheType = normalizeCacheType(*cacheType)
	}
	if providerName != nil {
		e.ProviderName = displayAuditProviderName(*providerName, e.Provider)
	} else {
		e.ProviderName = displayAuditProviderName("", e.Provider)
	}
	if userPath != nil {
		e.UserPath = *userPath
	}
	if errorType != nil {
		e.ErrorType = *errorType
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
