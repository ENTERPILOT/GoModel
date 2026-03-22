package core

import "encoding/json"

// BatchRequest is OpenAI-compatible for core fields and extends with inline requests.
//
// OpenAI-compatible fields:
//   - input_file_id
//   - endpoint
//   - completion_window
//   - metadata
//
// Gateway extension:
//   - requests (inline payloads for providers that support native inline batch bodies)
type BatchRequest struct {
	InputFileID      string             `json:"input_file_id,omitempty"`
	Endpoint         string             `json:"endpoint,omitempty"`
	CompletionWindow string             `json:"completion_window,omitempty"`
	Metadata         map[string]string  `json:"metadata,omitempty"`
	Requests         []BatchRequestItem `json:"requests,omitempty"`
	ExtraFields      UnknownJSONFields  `json:"-" swaggerignore:"true"`
}

const (
	// BatchActionCreate represents POST /v1/batches.
	BatchActionCreate = "create"
	// BatchActionList represents GET /v1/batches.
	BatchActionList = "list"
	// BatchActionGet represents GET /v1/batches/{id}.
	BatchActionGet = "get"
	// BatchActionCancel represents POST /v1/batches/{id}/cancel.
	BatchActionCancel = "cancel"
	// BatchActionResults represents GET /v1/batches/{id}/results.
	BatchActionResults = "results"
)

// BatchRouteInfo is sparse canonical metadata the gateway can derive for /v1/batches* routes.
// The full create payload remains in BatchRequest when the gateway lazily decodes JSON bodies.
type BatchRouteInfo struct {
	Action   string
	BatchID  string
	After    string
	LimitRaw string
	Limit    int
	HasLimit bool
}

func (req *BatchRouteInfo) ensureParsedLimit() error {
	if req == nil || req.LimitRaw == "" || req.HasLimit {
		return nil
	}
	parsed, err := parseRouteLimit(req.LimitRaw)
	if err != nil {
		return err
	}
	req.Limit = parsed
	req.HasLimit = true
	return nil
}

// BatchRequestItem represents one sub-request in an inline batch.
type BatchRequestItem struct {
	CustomID    string            `json:"custom_id,omitempty"`
	Method      string            `json:"method,omitempty"`
	URL         string            `json:"url"`
	Body        json.RawMessage   `json:"body" swaggertype:"object"`
	ExtraFields UnknownJSONFields `json:"-" swaggerignore:"true"`
}

// BatchResponse uses OpenAI-compatible batch fields and includes provider mapping plus optional cached results.
type BatchResponse struct {
	ID               string             `json:"id"`
	Object           string             `json:"object"`
	Provider         string             `json:"provider,omitempty"`
	ProviderBatchID  string             `json:"provider_batch_id,omitempty"`
	Endpoint         string             `json:"endpoint"`
	InputFileID      string             `json:"input_file_id,omitempty"`
	CompletionWindow string             `json:"completion_window,omitempty"`
	Status           string             `json:"status"`
	CreatedAt        int64              `json:"created_at"`
	InProgressAt     *int64             `json:"in_progress_at,omitempty"`
	CompletedAt      *int64             `json:"completed_at,omitempty"`
	FailedAt         *int64             `json:"failed_at,omitempty"`
	CancellingAt     *int64             `json:"cancelling_at,omitempty"`
	CancelledAt      *int64             `json:"cancelled_at,omitempty"`
	RequestCounts    BatchRequestCounts `json:"request_counts"`
	Metadata         map[string]string  `json:"metadata,omitempty"`

	// Gateway extension: optional usage/result snapshots persisted by the gateway.
	Usage   BatchUsageSummary `json:"usage"`
	Results []BatchResultItem `json:"results,omitempty"`
}

// BatchListResponse is returned by GET /v1/batches.
type BatchListResponse struct {
	Object  string          `json:"object"`
	Data    []BatchResponse `json:"data"`
	HasMore bool            `json:"has_more"`
	FirstID string          `json:"first_id,omitempty"`
	LastID  string          `json:"last_id,omitempty"`
}

// BatchResultsResponse is returned by GET /v1/batches/{id}/results.
type BatchResultsResponse struct {
	Object  string            `json:"object"`
	BatchID string            `json:"batch_id"`
	Data    []BatchResultItem `json:"data"`
}

// BatchRequestCounts is OpenAI-compatible aggregate batch status.
type BatchRequestCounts struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

// BatchResultItem represents one sub-response in a batch.
type BatchResultItem struct {
	Index      int         `json:"index"`
	CustomID   string      `json:"custom_id,omitempty"`
	URL        string      `json:"url"`
	StatusCode int         `json:"status_code"`
	Model      string      `json:"model,omitempty"`
	Provider   string      `json:"provider,omitempty"`
	Response   any         `json:"response,omitempty"`
	Error      *BatchError `json:"error,omitempty"`
}

// BatchError represents a normalized error for a failed batch item.
type BatchError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// BatchUsageSummary aggregates usage and cost for successful batch items.
type BatchUsageSummary struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`

	InputCost  *float64 `json:"input_cost,omitempty"`
	OutputCost *float64 `json:"output_cost,omitempty"`
	TotalCost  *float64 `json:"total_cost,omitempty"`
}
