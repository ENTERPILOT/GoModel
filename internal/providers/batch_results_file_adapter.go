package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
)

type openAICompatibleBatchLine struct {
	CustomID string `json:"custom_id"`
	Response *struct {
		StatusCode int             `json:"status_code"`
		Body       json.RawMessage `json:"body"`
		URL        string          `json:"url"`
	} `json:"response"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// FetchBatchResultsFromOutputFile adapts OpenAI-compatible batch output files to gateway batch results.
func FetchBatchResultsFromOutputFile(ctx context.Context, client *llmclient.Client, providerName, batchID string) (*core.BatchResultsResponse, error) {
	if strings.TrimSpace(batchID) == "" {
		return nil, core.NewInvalidRequestError("batch id is required", nil)
	}
	if client == nil {
		return nil, core.NewInvalidRequestError("provider client is not configured", nil)
	}

	batchRaw, err := client.DoRaw(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/batches/" + url.PathEscape(batchID),
	})
	if err != nil {
		return nil, err
	}

	outputFileID, status, endpoint := parseBatchFileMetadata(batchRaw.Body)
	if outputFileID == "" {
		if isPendingBatchStatus(status) {
			return nil, core.NewInvalidRequestErrorWithStatus(
				http.StatusConflict,
				fmt.Sprintf("batch results are not ready yet (status: %s)", status),
				nil,
			)
		}
		return nil, core.NewProviderError(providerName, http.StatusBadGateway, "provider batch response missing output file id", nil)
	}

	fileResp, err := client.DoPassthrough(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/files/" + url.PathEscape(outputFileID) + "/content",
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = fileResp.Body.Close() }()

	if fileResp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(fileResp.Body)
		if readErr != nil {
			body = []byte("failed to read error response")
		}
		return nil, core.ParseProviderError(providerName, fileResp.StatusCode, body, nil)
	}

	items, err := parseBatchOutputFile(fileResp.Body, endpoint, providerName)
	if err != nil {
		return nil, core.NewProviderError(providerName, http.StatusBadGateway, "failed to parse batch output file", err)
	}

	return &core.BatchResultsResponse{
		Object:  "list",
		BatchID: batchID,
		Data:    items,
	}, nil
}

func parseBatchFileMetadata(raw []byte) (outputFileID, status, endpoint string) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", "", ""
	}

	outputFileID = firstString(payload["output_file_id"], payload["output_file"], payload["result_file_id"])
	status = strings.TrimSpace(strings.ToLower(firstString(payload["status"])))
	endpoint = strings.TrimSpace(firstString(payload["endpoint"]))
	return outputFileID, status, endpoint
}

func isPendingBatchStatus(status string) bool {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "validating", "in_progress", "finalizing", "cancelling", "queued":
		return true
	default:
		return false
	}
}

func parseBatchOutputFile(raw io.Reader, fallbackURL, providerName string) ([]core.BatchResultItem, error) {
	scanner := bufio.NewScanner(raw)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	results := make([]core.BatchResultItem, 0)
	index := 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var row openAICompatibleBatchLine
		if err := json.Unmarshal(line, &row); err != nil {
			continue
		}

		item := core.BatchResultItem{
			Index:    index,
			CustomID: row.CustomID,
			URL:      firstString(row.ResponseURL(), fallbackURL),
			Provider: providerName,
		}
		if item.URL == "" {
			item.URL = "/v1/chat/completions"
		}

		if row.Response != nil {
			item.StatusCode = row.Response.StatusCode
			if item.StatusCode == 0 {
				item.StatusCode = http.StatusOK
			}
			if len(row.Response.Body) > 0 {
				var parsed map[string]any
				if err := json.Unmarshal(row.Response.Body, &parsed); err == nil {
					item.Response = parsed
					if model, ok := parsed["model"].(string); ok {
						item.Model = model
					}
				} else {
					item.Response = string(row.Response.Body)
				}
			}
		}

		if row.Error != nil {
			if item.StatusCode == 0 {
				item.StatusCode = http.StatusBadRequest
			}
			errType := firstString(row.Error.Type, row.Error.Code, "batch_item_error")
			errMessage := firstString(row.Error.Message, "batch item failed")
			item.Error = &core.BatchError{
				Type:    errType,
				Message: errMessage,
			}
		}

		if item.StatusCode == 0 {
			item.StatusCode = http.StatusBadRequest
		}
		results = append(results, item)
		index++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

func (l *openAICompatibleBatchLine) ResponseURL() string {
	if l == nil || l.Response == nil {
		return ""
	}
	return strings.TrimSpace(l.Response.URL)
}

func firstString(values ...any) string {
	for _, value := range values {
		switch v := value.(type) {
		case string:
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
