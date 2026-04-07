package auditlog

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"gomodel/internal/core"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoDBReader implements Reader for MongoDB.
type MongoDBReader struct {
	collection *mongo.Collection
}

type mongoLogRow struct {
	ID                     string    `bson:"_id"`
	Timestamp              time.Time `bson:"timestamp"`
	DurationNs             int64     `bson:"duration_ns"`
	Model                  string    `bson:"model"`
	ResolvedModel          string    `bson:"resolved_model"`
	Provider               string    `bson:"provider"`
	ProviderName           string    `bson:"provider_name"`
	AliasUsed              bool      `bson:"alias_used"`
	ExecutionPlanVersionID string    `bson:"execution_plan_version_id"`
	CacheType              string    `bson:"cache_type"`
	StatusCode             int       `bson:"status_code"`
	RequestID              string    `bson:"request_id"`
	AuthKeyID              string    `bson:"auth_key_id"`
	AuthMethod             string    `bson:"auth_method"`
	ClientIP               string    `bson:"client_ip"`
	Method                 string    `bson:"method"`
	Path                   string    `bson:"path"`
	UserPath               string    `bson:"user_path"`
	Stream                 bool      `bson:"stream"`
	ErrorType              string    `bson:"error_type"`
	Data                   *LogData  `bson:"data"`
}

func (r mongoLogRow) toLogEntry() *LogEntry {
	return &LogEntry{
		ID:                     r.ID,
		Timestamp:              r.Timestamp,
		DurationNs:             r.DurationNs,
		Model:                  r.Model,
		ResolvedModel:          r.ResolvedModel,
		Provider:               r.Provider,
		ProviderName:           displayAuditProviderName(r.ProviderName, r.Provider),
		AliasUsed:              r.AliasUsed,
		ExecutionPlanVersionID: r.ExecutionPlanVersionID,
		CacheType:              normalizeCacheType(r.CacheType),
		StatusCode:             r.StatusCode,
		RequestID:              r.RequestID,
		AuthKeyID:              r.AuthKeyID,
		AuthMethod:             r.AuthMethod,
		ClientIP:               r.ClientIP,
		Method:                 r.Method,
		Path:                   r.Path,
		UserPath:               r.UserPath,
		Stream:                 r.Stream,
		ErrorType:              r.ErrorType,
		Data:                   sanitizeLogData(r.Data),
	}
}

func sanitizeLogData(data *LogData) *LogData {
	if data == nil {
		return nil
	}
	clean := *data
	clean.RequestHeaders = RedactHeaders(data.RequestHeaders)
	clean.ResponseHeaders = RedactHeaders(data.ResponseHeaders)
	return &clean
}

// NewMongoDBReader creates a new MongoDB audit log reader.
func NewMongoDBReader(database *mongo.Database) (*MongoDBReader, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	return &MongoDBReader{collection: database.Collection("audit_logs")}, nil
}

func mongoUserPathMatchFilter(userPath string) bson.E {
	regexFilter := bson.D{{
		Key: "user_path",
		Value: bson.D{
			{Key: "$regex", Value: auditUserPathSubtreeRegex(userPath)},
		},
	}}
	if userPath != "/" {
		return regexFilter[0]
	}
	return bson.E{
		Key: "$or",
		Value: bson.A{
			regexFilter,
			bson.D{{Key: "user_path", Value: bson.D{{Key: "$exists", Value: false}}}},
			bson.D{{Key: "user_path", Value: nil}},
		},
	}
}

// GetLogs returns a paginated list of audit log entries.
func (r *MongoDBReader) GetLogs(ctx context.Context, params LogQueryParams) (*LogListResult, error) {
	limit, offset := clampLimitOffset(params.Limit, params.Offset)

	matchFilters := bson.D{}

	if tsFilter := mongoDateRangeFilter(params.QueryParams); tsFilter != nil {
		matchFilters = append(matchFilters, bson.E{Key: "timestamp", Value: tsFilter})
	}
	if params.Model != "" {
		matchFilters = append(matchFilters, bson.E{
			Key: "model",
			Value: bson.D{
				{Key: "$regex", Value: regexp.QuoteMeta(params.Model)},
				{Key: "$options", Value: "i"},
			},
		})
	}
	if params.Provider != "" {
		regex := bson.D{
			{Key: "$regex", Value: regexp.QuoteMeta(params.Provider)},
			{Key: "$options", Value: "i"},
		}
		matchFilters = append(matchFilters, bson.E{Key: "$or", Value: bson.A{
			bson.D{{Key: "provider", Value: regex}},
			bson.D{{Key: "provider_name", Value: regex}},
		}})
	}
	if params.Method != "" {
		matchFilters = append(matchFilters, bson.E{Key: "method", Value: params.Method})
	}
	if params.Path != "" {
		matchFilters = append(matchFilters, bson.E{
			Key: "path",
			Value: bson.D{
				{Key: "$regex", Value: regexp.QuoteMeta(params.Path)},
				{Key: "$options", Value: "i"},
			},
		})
	}
	if userPath, err := normalizeAuditUserPathFilter(params.UserPath); err != nil {
		return nil, core.NewInvalidRequestError(err.Error(), err)
	} else if userPath != "" {
		matchFilters = append(matchFilters, mongoUserPathMatchFilter(userPath))
	}
	if params.ErrorType != "" {
		matchFilters = append(matchFilters, bson.E{
			Key: "error_type",
			Value: bson.D{
				{Key: "$regex", Value: regexp.QuoteMeta(params.ErrorType)},
				{Key: "$options", Value: "i"},
			},
		})
	}
	if params.StatusCode != nil {
		matchFilters = append(matchFilters, bson.E{Key: "status_code", Value: *params.StatusCode})
	}
	if params.Stream != nil {
		matchFilters = append(matchFilters, bson.E{Key: "stream", Value: *params.Stream})
	}
	if params.Search != "" {
		pattern := regexp.QuoteMeta(params.Search)
		regex := bson.D{{Key: "$regex", Value: pattern}, {Key: "$options", Value: "i"}}
		matchFilters = append(matchFilters, bson.E{Key: "$or", Value: bson.A{
			bson.D{{Key: "request_id", Value: regex}},
			bson.D{{Key: "auth_key_id", Value: regex}},
			bson.D{{Key: "model", Value: regex}},
			bson.D{{Key: "provider", Value: regex}},
			bson.D{{Key: "provider_name", Value: regex}},
			bson.D{{Key: "method", Value: regex}},
			bson.D{{Key: "path", Value: regex}},
			bson.D{{Key: "user_path", Value: regex}},
			bson.D{{Key: "error_type", Value: regex}},
			bson.D{{Key: "data.error_message", Value: regex}},
		}})
	}

	pipeline := bson.A{}
	if len(matchFilters) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: matchFilters}})
	}

	pipeline = append(pipeline, bson.D{{Key: "$facet", Value: bson.D{
		{Key: "data", Value: bson.A{
			bson.D{{Key: "$sort", Value: bson.D{{Key: "timestamp", Value: -1}}}},
			bson.D{{Key: "$skip", Value: offset}},
			bson.D{{Key: "$limit", Value: limit}},
		}},
		{Key: "total", Value: bson.A{
			bson.D{{Key: "$count", Value: "count"}},
		}},
	}}})

	cursor, err := r.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate audit logs: %w", err)
	}
	defer cursor.Close(ctx)

	var facetResult struct {
		Data  []mongoLogRow `bson:"data"`
		Total []struct {
			Count int `bson:"count"`
		} `bson:"total"`
	}

	if cursor.Next(ctx) {
		if err := cursor.Decode(&facetResult); err != nil {
			return nil, fmt.Errorf("failed to decode audit log facet result: %w", err)
		}
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating audit log cursor: %w", err)
	}

	total := 0
	if len(facetResult.Total) > 0 {
		total = facetResult.Total[0].Count
	}

	entries := make([]LogEntry, 0, len(facetResult.Data))
	for _, row := range facetResult.Data {
		entry := row.toLogEntry()
		if entry != nil {
			entries = append(entries, *entry)
		}
	}

	return &LogListResult{
		Entries: entries,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	}, nil
}

// GetLogByID returns a single audit log entry by ID.
func (r *MongoDBReader) GetLogByID(ctx context.Context, id string) (*LogEntry, error) {
	var row mongoLogRow

	err := r.collection.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&row)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query audit log by id: %w", err)
	}

	return row.toLogEntry(), nil
}

// GetConversation returns a linear conversation thread around a seed log entry.
func (r *MongoDBReader) GetConversation(ctx context.Context, logID string, limit int) (*ConversationResult, error) {
	return buildConversationThread(ctx, logID, limit, r.GetLogByID, r.findByResponseID, r.findByPreviousResponseID)
}

func mongoDateRangeFilter(params QueryParams) bson.D {
	startZero := params.StartDate.IsZero()
	endZero := params.EndDate.IsZero()

	if !startZero && !endZero {
		return bson.D{{Key: "$gte", Value: params.StartDate.UTC()}, {Key: "$lt", Value: params.EndDate.AddDate(0, 0, 1).UTC()}}
	}
	if !startZero {
		return bson.D{{Key: "$gte", Value: params.StartDate.UTC()}}
	}
	if !endZero {
		return bson.D{{Key: "$lt", Value: params.EndDate.AddDate(0, 0, 1).UTC()}}
	}
	return nil
}

func (r *MongoDBReader) findByResponseID(ctx context.Context, responseID string) (*LogEntry, error) {
	return r.findFirstByField(ctx, "data.response_body.id", responseID, "response_id")
}

func (r *MongoDBReader) findByPreviousResponseID(ctx context.Context, previousResponseID string) (*LogEntry, error) {
	return r.findFirstByField(ctx, "data.request_body.previous_response_id", previousResponseID, "previous_response_id")
}

func (r *MongoDBReader) findFirstByField(ctx context.Context, field string, value any, label string) (*LogEntry, error) {
	filter := bson.D{{Key: field, Value: value}}
	opts := options.FindOne().SetSort(bson.D{{Key: "timestamp", Value: 1}})

	var row mongoLogRow

	if err := r.collection.FindOne(ctx, filter, opts).Decode(&row); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query audit log by %s: %w", label, err)
	}

	return row.toLogEntry(), nil
}
