package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
)

func parseOptionalUnix(ts string) *int64 {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return nil
	}
	u := t.Unix()
	return &u
}

func mapAnthropicBatchResponse(resp *anthropicBatchResponse) *core.BatchResponse {
	if resp == nil {
		return nil
	}

	total := resp.RequestCounts.Processing + resp.RequestCounts.Succeeded + resp.RequestCounts.Errored + resp.RequestCounts.Canceled + resp.RequestCounts.Expired
	failed := resp.RequestCounts.Errored + resp.RequestCounts.Canceled + resp.RequestCounts.Expired

	status := "in_progress"
	switch resp.ProcessingStatus {
	case "canceling":
		status = "cancelling"
	case "ended":
		switch {
		case resp.RequestCounts.Canceled > 0 && resp.RequestCounts.Succeeded == 0 && resp.RequestCounts.Errored == 0:
			status = "cancelled"
		case resp.RequestCounts.Errored > 0 && resp.RequestCounts.Succeeded == 0:
			status = "failed"
		default:
			status = "completed"
		}
	}

	return &core.BatchResponse{
		ID:           resp.ID,
		Object:       "batch",
		Status:       status,
		CreatedAt:    parseCreatedAt(resp.CreatedAt),
		CompletedAt:  parseOptionalUnix(resp.EndedAt),
		CancellingAt: parseOptionalUnix(resp.CancelInitiatedAt),
		RequestCounts: core.BatchRequestCounts{
			Total:     total,
			Completed: resp.RequestCounts.Succeeded,
			Failed:    failed,
		},
	}
}

func buildAnthropicBatchCreateRequest(req *core.BatchRequest) (*anthropicBatchCreateRequest, map[string]string, error) {
	const maxAnthropicBatchRequests = 10000

	if req == nil {
		return nil, nil, core.NewInvalidRequestError("request is required for anthropic batch processing", nil)
	}
	if len(req.Requests) == 0 {
		return nil, nil, core.NewInvalidRequestError("requests is required for anthropic batch processing", nil)
	}
	if len(req.Requests) > maxAnthropicBatchRequests {
		return nil, nil, core.NewInvalidRequestError("too many requests for anthropic batch processing", nil)
	}

	out := &anthropicBatchCreateRequest{
		Requests: make([]anthropicBatchRequest, 0, len(req.Requests)),
	}
	endpointByCustomID := make(map[string]string, len(req.Requests))
	seenCustomIDs := make(map[string]int, len(req.Requests))

	for i, item := range req.Requests {
		decoded, err := core.DecodeKnownBatchItemRequest(req.Endpoint, item)
		if err != nil {
			return nil, nil, core.NewInvalidRequestError(fmt.Sprintf("batch item %d: %s", i, err.Error()), err)
		}

		params, err := convertDecodedBatchItemToAnthropic(decoded)
		if err != nil {
			return nil, nil, prefixAnthropicBatchItemError(i, err)
		}

		customID := strings.TrimSpace(item.CustomID)
		if customID == "" {
			customID = fmt.Sprintf("req-%d", i)
		}
		if previousIndex, exists := seenCustomIDs[customID]; exists {
			return nil, nil, core.NewInvalidRequestError(
				fmt.Sprintf("batch item %d: duplicate custom_id %q (already used by batch item %d)", i, customID, previousIndex),
				nil,
			)
		}
		seenCustomIDs[customID] = i
		out.Requests = append(out.Requests, anthropicBatchRequest{
			CustomID: customID,
			Params:   *params,
		})
		endpointByCustomID[customID] = decoded.Endpoint
	}

	return out, endpointByCustomID, nil
}

func (p *Provider) createBatch(ctx context.Context, req *core.BatchRequest) (*core.BatchResponse, map[string]string, error) {
	anthropicReq, endpointByCustomID, err := buildAnthropicBatchCreateRequest(req)
	if err != nil {
		return nil, nil, err
	}

	var resp anthropicBatchResponse
	err = p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/messages/batches",
		Body:     anthropicReq,
	}, &resp)
	if err != nil {
		return nil, nil, err
	}

	mapped := mapAnthropicBatchResponse(&resp)
	if mapped == nil {
		return nil, nil, core.NewProviderError("anthropic", http.StatusBadGateway, "failed to map anthropic batch response", nil)
	}
	mapped.ProviderBatchID = mapped.ID
	p.setBatchResultEndpoints(mapped.ProviderBatchID, endpointByCustomID)
	return mapped, cloneBatchResultEndpoints(endpointByCustomID), nil
}

// CreateBatch creates an Anthropic native message batch.
func (p *Provider) CreateBatch(ctx context.Context, req *core.BatchRequest) (*core.BatchResponse, error) {
	mapped, _, err := p.createBatch(ctx, req)
	return mapped, err
}

// CreateBatchWithHints creates an Anthropic native message batch and returns
// persisted per-item endpoint hints for later result shaping.
func (p *Provider) CreateBatchWithHints(ctx context.Context, req *core.BatchRequest) (*core.BatchResponse, map[string]string, error) {
	return p.createBatch(ctx, req)
}

// GetBatch retrieves an Anthropic native message batch.
func (p *Provider) GetBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	var resp anthropicBatchResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/messages/batches/" + url.PathEscape(id),
	}, &resp)
	if err != nil {
		return nil, err
	}
	mapped := mapAnthropicBatchResponse(&resp)
	if mapped == nil {
		return nil, core.NewProviderError("anthropic", http.StatusBadGateway, "failed to map anthropic batch response", nil)
	}
	mapped.ProviderBatchID = mapped.ID
	return mapped, nil
}

// ListBatches lists Anthropic native message batches.
func (p *Provider) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	// Anthropic uses before_id for reverse-chronological pagination.
	// Gateway `after` is mapped directly to before_id for provider-native paging.
	if after != "" {
		values.Set("before_id", after)
	}
	endpoint := "/messages/batches"
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	var resp anthropicBatchListResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: endpoint,
	}, &resp)
	if err != nil {
		return nil, err
	}

	data := make([]core.BatchResponse, 0, len(resp.Data))
	for _, row := range resp.Data {
		mapped := mapAnthropicBatchResponse(&row)
		if mapped == nil {
			continue
		}
		mapped.ProviderBatchID = mapped.ID
		data = append(data, *mapped)
	}

	return &core.BatchListResponse{
		Object:  "list",
		Data:    data,
		HasMore: resp.HasMore,
		FirstID: resp.FirstID,
		LastID:  resp.LastID,
	}, nil
}

// CancelBatch cancels an Anthropic native message batch.
func (p *Provider) CancelBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	var resp anthropicBatchResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/messages/batches/" + url.PathEscape(id) + "/cancel",
	}, &resp)
	if err != nil {
		return nil, err
	}
	mapped := mapAnthropicBatchResponse(&resp)
	if mapped == nil {
		return nil, core.NewProviderError("anthropic", http.StatusBadGateway, "failed to map anthropic batch response", nil)
	}
	mapped.ProviderBatchID = mapped.ID
	return mapped, nil
}

func (p *Provider) getBatchResults(ctx context.Context, id string, endpointByCustomID map[string]string) (*core.BatchResultsResponse, error) {
	resp, err := p.client.DoPassthrough(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/messages/batches/" + url.PathEscape(id) + "/results",
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			body = []byte("failed to read error response")
		}
		return nil, core.ParseProviderError("anthropic", resp.StatusCode, body, nil)
	}

	scanner := bufio.NewScanner(resp.Body)
	// Allow larger result lines than Scanner's default 64K.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	if endpointByCustomID == nil {
		endpointByCustomID = p.getBatchResultEndpoints(id)
	} else {
		endpointByCustomID = cloneBatchResultEndpoints(endpointByCustomID)
	}

	results := make([]core.BatchResultItem, 0)
	index := 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var row anthropicBatchResultLine
		if err := json.Unmarshal(line, &row); err != nil {
			slog.Warn(
				"failed to decode anthropic batch result line",
				"error", err,
				"batch_id", id,
				"line_index", index,
				"line_bytes", len(line),
			)
			continue
		}
		itemEndpoint := "/v1/chat/completions"
		if endpointByCustomID != nil {
			if endpoint := strings.TrimSpace(endpointByCustomID[row.CustomID]); endpoint != "" {
				itemEndpoint = endpoint
			}
		}

		item := core.BatchResultItem{
			Index:    index,
			CustomID: row.CustomID,
			URL:      itemEndpoint,
			Provider: "anthropic",
		}
		switch row.Result.Type {
		case "succeeded":
			item.StatusCode = http.StatusOK
			if len(row.Result.Message) > 0 {
				var anthropicPayload anthropicResponse
				if err := json.Unmarshal(row.Result.Message, &anthropicPayload); err == nil {
					switch itemEndpoint {
					case "/v1/responses":
						mapped := convertAnthropicResponseToResponses(&anthropicPayload, anthropicPayload.Model)
						item.Response = mapped
						item.Model = mapped.Model
					default:
						mapped := convertFromAnthropicResponse(&anthropicPayload)
						item.Response = mapped
						item.Model = mapped.Model
					}
				} else {
					item.Response = string(row.Result.Message)
				}
			}
		default:
			item.StatusCode = http.StatusBadRequest
			errType := row.Result.Type
			errMsg := "batch item failed"
			if row.Result.Error != nil {
				if row.Result.Error.Type != "" {
					errType = row.Result.Error.Type
				}
				if row.Result.Error.Message != "" {
					errMsg = row.Result.Error.Message
				}
			}
			item.Error = &core.BatchError{
				Type:    errType,
				Message: errMsg,
			}
		}

		results = append(results, item)
		index++
	}
	if err := scanner.Err(); err != nil {
		return nil, core.NewProviderError("anthropic", http.StatusBadGateway, "failed to parse anthropic batch results", err)
	}

	return &core.BatchResultsResponse{
		Object:  "list",
		BatchID: id,
		Data:    results,
	}, nil
}

// GetBatchResults retrieves Anthropic native message batch results.
func (p *Provider) GetBatchResults(ctx context.Context, id string) (*core.BatchResultsResponse, error) {
	return p.getBatchResults(ctx, id, nil)
}

// GetBatchResultsWithHints retrieves Anthropic native batch results using
// persisted per-item endpoint hints instead of transient in-memory state.
func (p *Provider) GetBatchResultsWithHints(ctx context.Context, id string, endpointByCustomID map[string]string) (*core.BatchResultsResponse, error) {
	return p.getBatchResults(ctx, id, endpointByCustomID)
}

// ClearBatchResultHints clears transient per-batch endpoint hints once they
// have been persisted by the gateway.
func (p *Provider) ClearBatchResultHints(batchID string) {
	p.clearBatchResultEndpoints(batchID)
}
