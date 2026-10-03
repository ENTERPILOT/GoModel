package usage

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

func (d usageDialect) usageConditions(params UsageQueryParams) ([]string, []any, error) {
	return d.usageConditionsWithUserPathExpr(params, "user_path")
}

func (d usageDialect) usageConditionsWithUserPathExpr(params UsageQueryParams, userPathExpr string) ([]string, []any, error) {
	conditions, args := d.dateRangeConditions(params)
	userPath, err := normalizeUsageUserPathFilter(params.UserPath)
	if err != nil {
		return nil, nil, err
	}
	if userPath != "" {
		conditions = append(conditions, "("+userPathExpr+" = ? OR "+userPathExpr+" LIKE ? ESCAPE '\\')")
		args = append(args, userPath, usageUserPathSubtreePattern(userPath))
	}
	if params.Model != "" {
		conditions = append(conditions, "model = ?")
		args = append(args, params.Model)
	}
	if params.SessionID != "" {
		conditions = append(conditions, "session_id = ?")
		args = append(args, params.SessionID)
	}
	if params.Provider != "" {
		conditions = append(conditions, "(provider = ? OR provider_name = ?)")
		args = append(args, params.Provider, params.Provider)
	}
	if params.Label != "" {
		conditions = append(conditions, d.labelMatch)
		args = append(args, params.Label)
	}
	if condition := cacheModeCondition(params.CacheMode); condition != "" {
		conditions = append(conditions, condition)
	}
	return conditions, args, nil
}

// dateRangeConditions returns WHERE conditions and args for a date range.
func (d usageDialect) dateRangeConditions(params UsageQueryParams) (conditions []string, args []any) {
	if !params.StartDate.IsZero() {
		conditions = append(conditions, d.timeExpr+" >= ?")
		args = append(args, d.timeArg(params.StartDate))
	}
	if !params.EndDate.IsZero() {
		conditions = append(conditions, d.timeExpr+" < ?")
		args = append(args, d.timeArg(usageEndExclusive(params)))
	}
	return conditions, args
}

func cacheModeCondition(mode string) string {
	switch normalizeCacheMode(mode) {
	case CacheModeCached:
		return "(cache_type = '" + CacheTypeExact + "' OR cache_type = '" + CacheTypeSemantic + "')"
	case CacheModeAll:
		return ""
	default:
		return "(cache_type IS NULL OR cache_type = '')"
	}
}

// usageTimestamp scans the timestamp column from either engine: PostgreSQL
// hands back a time.Time, SQLite the stored text (or a time its driver
// already parsed). Unreadable text leaves Valid false instead of failing the
// row, and keeps Raw for the warning.
type usageTimestamp struct {
	Time  time.Time
	Valid bool
	Raw   string
}

func (t *usageTimestamp) Scan(src any) error {
	*t = usageTimestamp{}
	switch value := src.(type) {
	case nil:
	case time.Time:
		t.Time, t.Valid = value, true
	case string:
		t.Raw = value
		t.Time, t.Valid = sqlutil.ParseSQLiteTimestamp(value)
	case []byte:
		t.Raw = string(value)
		t.Time, t.Valid = sqlutil.ParseSQLiteTimestamp(t.Raw)
	default:
		return fmt.Errorf("cannot scan %T into a usage timestamp", src)
	}
	return nil
}

func scanUsageLogEntries(rows sqlx.Rows) ([]UsageLogEntry, error) {
	entries := make([]UsageLogEntry, 0)
	for rows.Next() {
		var e UsageLogEntry
		var ts usageTimestamp
		var providerName, userPath, sessionID, cacheType, labelsJSON, rawDataJSON *string
		if err := rows.Scan(&e.ID, &e.RequestID, &e.ProviderID, &ts, &e.Model, &e.Provider, &providerName, &e.Endpoint, &userPath, &sessionID, &cacheType, &labelsJSON,
			&e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.InputCost, &e.OutputCost, &e.TotalCost, &e.CostSource, &rawDataJSON, &e.CostsCalculationCaveat,
			&e.RewriteTokensSaved, &e.RewriteCostSaved); err != nil {
			return nil, fmt.Errorf("failed to scan usage log row: %w", err)
		}
		if ts.Valid {
			e.Timestamp = ts.Time
		} else {
			slog.Warn("failed to parse timestamp", "request_id", e.RequestID, "raw_timestamp", ts.Raw)
		}
		if labelsJSON != nil {
			e.Labels = sqlutil.StringsFromJSON(*labelsJSON, e.RequestID)
		}
		if rawDataJSON != nil && *rawDataJSON != "" {
			if err := json.Unmarshal([]byte(*rawDataJSON), &e.RawData); err != nil {
				slog.Warn("failed to unmarshal raw_data JSON", "request_id", e.RequestID, "error", err)
			}
		}
		if userPath != nil {
			e.UserPath = *userPath
		}
		if sessionID != nil {
			e.SessionID = *sessionID
		}
		providerNameValue := ""
		if providerName != nil {
			providerNameValue = *providerName
		}
		e.ProviderName = displayUsageProviderName(providerNameValue, e.Provider)
		if cacheType != nil {
			e.CacheType = normalizeCacheType(*cacheType)
		}
		entries = append(entries, e)
	}
	return entries, nil
}
