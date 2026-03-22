package auditlog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLReader implements Reader for PostgreSQL databases.
type PostgreSQLReader struct {
	pool *pgxpool.Pool
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

	if params.Model != "" {
		conditions = append(conditions, fmt.Sprintf("model ILIKE $%d ESCAPE '\\'", argIdx))
		args = append(args, "%"+escapeLikeWildcards(params.Model)+"%")
		argIdx++
	}
	if params.Provider != "" {
		conditions = append(conditions, fmt.Sprintf("provider ILIKE $%d ESCAPE '\\'", argIdx))
		args = append(args, "%"+escapeLikeWildcards(params.Provider)+"%")
		argIdx++
	}
	if params.Method != "" {
		conditions = append(conditions, fmt.Sprintf("method = $%d", argIdx))
		args = append(args, params.Method)
		argIdx++
	}
	if params.Path != "" {
		conditions = append(conditions, fmt.Sprintf("path ILIKE $%d ESCAPE '\\'", argIdx))
		args = append(args, "%"+escapeLikeWildcards(params.Path)+"%")
		argIdx++
	}
	if params.ErrorType != "" {
		conditions = append(conditions, fmt.Sprintf("error_type ILIKE $%d ESCAPE '\\'", argIdx))
		args = append(args, "%"+escapeLikeWildcards(params.ErrorType)+"%")
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
		s := "%" + escapeLikeWildcards(params.Search) + "%"
		conditions = append(conditions, fmt.Sprintf("(request_id ILIKE $%d ESCAPE '\\' OR model ILIKE $%d ESCAPE '\\' OR provider ILIKE $%d ESCAPE '\\' OR method ILIKE $%d ESCAPE '\\' OR path ILIKE $%d ESCAPE '\\' OR error_type ILIKE $%d ESCAPE '\\')", argIdx, argIdx, argIdx, argIdx, argIdx, argIdx))
		args = append(args, s)
		argIdx++
	}

	where := buildWhereClause(conditions)

	var total int
	countQuery := `SELECT COUNT(*) FROM audit_logs` + where
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count audit log entries: %w", err)
	}

	dataQuery := fmt.Sprintf(`SELECT id, timestamp, duration_ns, model, resolved_model, provider, alias_used, status_code, request_id,
		client_ip, method, path, stream, error_type, data
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

		if err := rows.Scan(&e.ID, &e.Timestamp, &e.DurationNs, &e.Model, &e.ResolvedModel, &e.Provider, &e.AliasUsed, &e.StatusCode,
			&e.RequestID, &e.ClientIP, &e.Method, &e.Path, &e.Stream, &e.ErrorType, &dataJSON); err != nil {
			return nil, fmt.Errorf("failed to scan audit log row: %w", err)
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
func (r *PostgreSQLReader) GetLogByID(ctx context.Context, id string) (*LogEntry, error) {
	query := `SELECT id, timestamp, duration_ns, model, resolved_model, provider, alias_used, status_code, request_id,
		client_ip, method, path, stream, error_type, data
		FROM audit_logs WHERE id::text = $1 LIMIT 1`

	rows, err := r.pool.Query(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit log by id: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, nil
	}

	entry, err := scanPostgreSQLLogEntry(rows)
	if err != nil {
		return nil, err
	}
	return entry, nil
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
	query := `SELECT id, timestamp, duration_ns, model, resolved_model, provider, alias_used, status_code, request_id,
		client_ip, method, path, stream, error_type, data
		FROM audit_logs
		WHERE data->'response_body'->>'id' = $1
		ORDER BY timestamp ASC
		LIMIT 1`

	rows, err := r.pool.Query(ctx, query, responseID)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit log by response id: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	return scanPostgreSQLLogEntry(rows)
}

func (r *PostgreSQLReader) findByPreviousResponseID(ctx context.Context, previousResponseID string) (*LogEntry, error) {
	query := `SELECT id, timestamp, duration_ns, model, resolved_model, provider, alias_used, status_code, request_id,
		client_ip, method, path, stream, error_type, data
		FROM audit_logs
		WHERE data->'request_body'->>'previous_response_id' = $1
		ORDER BY timestamp ASC
		LIMIT 1`

	rows, err := r.pool.Query(ctx, query, previousResponseID)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit log by previous_response_id: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	return scanPostgreSQLLogEntry(rows)
}

func scanPostgreSQLLogEntry(rows interface {
	Scan(dest ...any) error
}) (*LogEntry, error) {
	var e LogEntry
	var dataJSON *string

	if err := rows.Scan(&e.ID, &e.Timestamp, &e.DurationNs, &e.Model, &e.ResolvedModel, &e.Provider, &e.AliasUsed, &e.StatusCode,
		&e.RequestID, &e.ClientIP, &e.Method, &e.Path, &e.Stream, &e.ErrorType, &dataJSON); err != nil {
		return nil, fmt.Errorf("failed to scan audit log row: %w", err)
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
