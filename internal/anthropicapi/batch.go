package anthropicapi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// Anthropic Message Batches dialect. Requests are translated at the edge into
// the canonical batch type and served by the same native-batch pipeline as
// /v1/batches; responses are rendered back in the Anthropic message_batch
// shape. Gateway batch IDs ("batch_<uuid>") are exposed with the Anthropic
// "msgbatch_" prefix — the two are pure prefix aliases of the same resource.

const (
	gatewayBatchIDPrefix = "batch_"
	messageBatchIDPrefix = "msgbatch_"

	// messageBatchItemEndpoint is the canonical per-item endpoint recorded for
	// Message Batches items. Items are translated to canonical chat requests,
	// so results come back chat-shaped from every provider and are converted
	// to the Anthropic Messages shape at egress.
	messageBatchItemEndpoint = "/v1/chat/completions"

	// defaultBatchExpiry mirrors the Anthropic contract: a Message Batch
	// expires 24 hours after creation.
	defaultBatchExpiry = 24 * time.Hour
)

// BatchCreateRequest is the Anthropic POST /v1/messages/batches body.
type BatchCreateRequest struct {
	Requests []BatchCreateItem `json:"requests"`
}

// BatchCreateItem is one request of an Anthropic Message Batch. Params is a
// full Messages API request body.
type BatchCreateItem struct {
	CustomID string          `json:"custom_id"`
	Params   json.RawMessage `json:"params" swaggertype:"object"`
}

// MessageBatch is the Anthropic message_batch object.
type MessageBatch struct {
	ID                string                    `json:"id"`
	Type              string                    `json:"type"`
	ProcessingStatus  string                    `json:"processing_status"`
	RequestCounts     MessageBatchRequestCounts `json:"request_counts"`
	EndedAt           *string                   `json:"ended_at"`
	CreatedAt         string                    `json:"created_at"`
	ExpiresAt         string                    `json:"expires_at"`
	ArchivedAt        *string                   `json:"archived_at"`
	CancelInitiatedAt *string                   `json:"cancel_initiated_at"`
	ResultsURL        *string                   `json:"results_url"`
}

// MessageBatchRequestCounts tallies batch requests by status.
type MessageBatchRequestCounts struct {
	Processing int `json:"processing"`
	Succeeded  int `json:"succeeded"`
	Errored    int `json:"errored"`
	Canceled   int `json:"canceled"`
	Expired    int `json:"expired"`
}

// MessageBatchList is the Anthropic GET /v1/messages/batches response body.
type MessageBatchList struct {
	Data    []MessageBatch `json:"data"`
	HasMore bool           `json:"has_more"`
	FirstID *string        `json:"first_id"`
	LastID  *string        `json:"last_id"`
}

// DeletedMessageBatch is the Anthropic DELETE /v1/messages/batches/{id}
// response body.
type DeletedMessageBatch struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// DecodeBatchCreateRequest parses an Anthropic Message Batches create body,
// rejecting trailing bytes with the same discipline as DecodeMessagesRequest.
func DecodeBatchCreateRequest(body []byte) (*BatchCreateRequest, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("request body is empty")
	}
	var req BatchCreateRequest
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if err := dec.Decode(&req); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, fmt.Errorf("request body must contain a single JSON object")
	}
	return &req, nil
}

// ToBatchRequest translates an Anthropic Message Batches create body into the
// canonical batch request. Each item's Messages params are translated to a
// canonical chat request, so the batch routes to any provider with native
// batch support, not only Anthropic.
func ToBatchRequest(req *BatchCreateRequest) (*core.BatchRequest, error) {
	if req == nil || len(req.Requests) == 0 {
		return nil, core.NewInvalidRequestError("requests must not be empty", nil).WithParam("requests")
	}

	items := make([]core.BatchRequestItem, 0, len(req.Requests))
	seen := make(map[string]struct{}, len(req.Requests))
	for i, item := range req.Requests {
		// custom_id is the caller's correlation key: validate blankness on a
		// trimmed copy but carry the original verbatim so results match it.
		customID := item.CustomID
		if strings.TrimSpace(customID) == "" {
			return nil, core.NewInvalidRequestError(
				fmt.Sprintf("requests[%d].custom_id is required", i), nil)
		}
		if _, dup := seen[customID]; dup {
			return nil, core.NewInvalidRequestError(
				fmt.Sprintf("requests[%d].custom_id %q is not unique", i, customID), nil)
		}
		seen[customID] = struct{}{}

		messages, err := DecodeMessagesRequest(item.Params)
		if err != nil {
			return nil, core.NewInvalidRequestError(
				fmt.Sprintf("requests[%d].params: %v", i, err), err)
		}
		chat, err := ToChatRequest(messages)
		if err != nil {
			return nil, wrapBatchItemError(i, err)
		}
		body, err := json.Marshal(chat)
		if err != nil {
			return nil, core.NewInvalidRequestError(
				fmt.Sprintf("requests[%d].params: %v", i, err), err)
		}
		items = append(items, core.BatchRequestItem{
			CustomID: customID,
			Method:   http.MethodPost,
			URL:      messageBatchItemEndpoint,
			Body:     body,
		})
	}

	return &core.BatchRequest{
		Endpoint:         messageBatchItemEndpoint,
		CompletionWindow: "24h",
		Requests:         items,
	}, nil
}

// wrapBatchItemError prefixes a per-item translation error with its index,
// preserving the gateway error type.
func wrapBatchItemError(index int, err error) error {
	gatewayErr, ok := errors.AsType[*core.GatewayError](err)
	if !ok {
		return core.NewInvalidRequestError(fmt.Sprintf("requests[%d].params: %v", index, err), err)
	}
	wrapped := *gatewayErr
	wrapped.Message = fmt.Sprintf("requests[%d].params: %s", index, gatewayErr.Message)
	return &wrapped
}

// MessageBatchID renders a gateway batch ID in the Anthropic msgbatch_ form.
func MessageBatchID(gatewayID string) string {
	if rest, ok := strings.CutPrefix(gatewayID, gatewayBatchIDPrefix); ok {
		return messageBatchIDPrefix + rest
	}
	return gatewayID
}

// GatewayBatchID maps an Anthropic msgbatch_ ID back to the gateway form.
func GatewayBatchID(messageBatchID string) string {
	if rest, ok := strings.CutPrefix(messageBatchID, messageBatchIDPrefix); ok {
		return gatewayBatchIDPrefix + rest
	}
	return messageBatchID
}

// FromBatchResponse renders a canonical batch in the Anthropic message_batch
// shape.
func FromBatchResponse(batch *core.BatchResponse) *MessageBatch {
	if batch == nil {
		return nil
	}
	out := &MessageBatch{
		ID:               MessageBatchID(batch.ID),
		Type:             "message_batch",
		ProcessingStatus: processingStatusFromBatch(batch.Status),
		CreatedAt:        rfc3339FromUnix(batch.CreatedAt),
		ExpiresAt:        rfc3339FromUnix(batch.CreatedAt + int64(batchExpiry(batch.CompletionWindow).Seconds())),
	}
	out.RequestCounts = messageBatchCounts(batch, out.ProcessingStatus)
	out.CancelInitiatedAt = rfc3339FromUnixPtr(batch.CancellingAt)
	// ended_at stays null when the provider reported no transition timestamp:
	// fabricating one at render time would make repeated retrievals disagree.
	out.EndedAt = rfc3339FromUnixPtr(firstUnix(batch.CompletedAt, batch.FailedAt, batch.CancelledAt))
	if out.ProcessingStatus == "ended" {
		resultsURL := "/v1/messages/batches/" + out.ID + "/results"
		out.ResultsURL = &resultsURL
	}
	return out
}

// FromBatchList renders a canonical batch list in the Anthropic list shape.
func FromBatchList(list *core.BatchListResponse) *MessageBatchList {
	out := &MessageBatchList{Data: []MessageBatch{}}
	if list == nil {
		return out
	}
	out.HasMore = list.HasMore
	for i := range list.Data {
		if mapped := FromBatchResponse(&list.Data[i]); mapped != nil {
			out.Data = append(out.Data, *mapped)
		}
	}
	if len(out.Data) > 0 {
		out.FirstID = &out.Data[0].ID
		out.LastID = &out.Data[len(out.Data)-1].ID
	}
	return out
}

// processingStatusFromBatch maps an OpenAI-style batch status onto the
// Anthropic processing_status.
func processingStatusFromBatch(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "cancelling", "canceling":
		return "canceling"
	case "completed", "failed", "cancelled", "canceled", "expired":
		return "ended"
	default:
		// validating, queued, in_progress, finalizing, unknown
		return "in_progress"
	}
}

// messageBatchCounts maps the OpenAI-style aggregate counts onto the Anthropic
// per-status tallies. The canonical type only distinguishes total/completed/
// failed, so the remainder is attributed by the batch status: still processing
// while the batch runs, canceled/expired/errored once it ended.
func messageBatchCounts(batch *core.BatchResponse, processingStatus string) MessageBatchRequestCounts {
	counts := MessageBatchRequestCounts{
		Succeeded: batch.RequestCounts.Completed,
		Errored:   batch.RequestCounts.Failed,
	}
	remainder := batch.RequestCounts.Total - batch.RequestCounts.Completed - batch.RequestCounts.Failed
	if remainder <= 0 {
		return counts
	}
	if processingStatus != "ended" {
		counts.Processing = remainder
		return counts
	}
	switch strings.ToLower(strings.TrimSpace(batch.Status)) {
	case "cancelled", "canceled":
		counts.Canceled = remainder
	case "expired":
		counts.Expired = remainder
	default:
		counts.Errored += remainder
	}
	return counts
}

func batchExpiry(completionWindow string) time.Duration {
	if window, err := time.ParseDuration(strings.TrimSpace(completionWindow)); err == nil && window > 0 {
		return window
	}
	return defaultBatchExpiry
}

func rfc3339FromUnix(ts int64) string {
	return time.Unix(ts, 0).UTC().Format(time.RFC3339)
}

func rfc3339FromUnixPtr(ts *int64) *string {
	if ts == nil {
		return nil
	}
	formatted := rfc3339FromUnix(*ts)
	return &formatted
}

func firstUnix(values ...*int64) *int64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// batchResultLine is one JSONL line of GET /v1/messages/batches/{id}/results.
type batchResultLine struct {
	CustomID string           `json:"custom_id"`
	Result   batchResultValue `json:"result"`
}

type batchResultValue struct {
	Type    string            `json:"type"`
	Message *MessagesResponse `json:"message,omitempty"`
	Error   *ErrorResponse    `json:"error,omitempty"`
}

// EncodeBatchResults renders canonical batch results as the Anthropic
// results JSONL stream. Successful items are converted from the chat shape to
// the Anthropic Messages shape; failed items carry the Anthropic error
// envelope; canceled/expired items map to their dedicated result types.
func EncodeBatchResults(results *core.BatchResultsResponse) ([]byte, error) {
	var buf bytes.Buffer
	if results == nil {
		return buf.Bytes(), nil
	}
	for _, item := range results.Data {
		line := batchResultLine{CustomID: item.CustomID, Result: resultValueFromItem(item)}
		encoded, err := json.Marshal(line)
		if err != nil {
			return nil, fmt.Errorf("encode batch result for custom_id %q: %w", item.CustomID, err)
		}
		buf.Write(encoded)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

func resultValueFromItem(item core.BatchResultItem) batchResultValue {
	if item.Error == nil && item.StatusCode >= 200 && item.StatusCode < 300 && item.Response != nil {
		if message := messageFromBatchItemResponse(item.Response); message != nil {
			return batchResultValue{Type: "succeeded", Message: message}
		}
	}

	errType := "api_error"
	errMessage := "batch item failed"
	if item.Error != nil {
		switch item.Error.Type {
		// Provider-native canceled/expired item outcomes surface as their
		// dedicated Anthropic result types, not as errors.
		case "canceled", "cancelled":
			return batchResultValue{Type: "canceled"}
		case "expired":
			return batchResultValue{Type: "expired"}
		}
		if item.Error.Type != "" {
			errType = item.Error.Type
		}
		if item.Error.Message != "" {
			errMessage = item.Error.Message
		}
	}
	envelope := newErrorResponse(errType, errMessage)
	return batchResultValue{Type: "errored", Error: &envelope}
}

// messageFromBatchItemResponse converts a stored chat-shaped batch item
// response (a typed *core.ChatResponse from the anthropic provider or a
// decoded map from OpenAI-compatible providers) into the Anthropic Messages
// shape. It returns nil when the payload does not look like a chat response.
func messageFromBatchItemResponse(response any) *MessagesResponse {
	chat, ok := response.(*core.ChatResponse)
	if !ok {
		raw, err := json.Marshal(response)
		if err != nil {
			return nil
		}
		decoded := &core.ChatResponse{}
		if err := json.Unmarshal(raw, decoded); err != nil {
			return nil
		}
		chat = decoded
	}
	if chat == nil || len(chat.Choices) == 0 {
		return nil
	}
	return FromChatResponse(chat)
}
