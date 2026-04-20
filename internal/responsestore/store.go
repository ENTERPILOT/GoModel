// Package responsestore provides persistence for OpenAI-compatible Responses
// lifecycle endpoints.
package responsestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gomodel/internal/core"
)

// ErrNotFound indicates a requested response was not found.
var ErrNotFound = errors.New("response not found")

// StoredResponse keeps the public response snapshot separate from gateway-only
// routing and input item metadata.
type StoredResponse struct {
	Response           *core.ResponsesResponse `json:"response"`
	InputItems         []json.RawMessage       `json:"input_items,omitempty"`
	Provider           string                  `json:"provider,omitempty"`
	ProviderName       string                  `json:"provider_name,omitempty"`
	ProviderResponseID string                  `json:"provider_response_id,omitempty"`
	RequestID          string                  `json:"request_id,omitempty"`
	UserPath           string                  `json:"user_path,omitempty"`
	WorkflowVersionID  string                  `json:"workflow_version_id,omitempty"`
	StoredAt           time.Time               `json:"stored_at,omitempty"`
	ExpiresAt          time.Time               `json:"expires_at,omitempty"`
}

// Store defines persistence operations for Responses lifecycle APIs.
type Store interface {
	Create(ctx context.Context, response *StoredResponse) error
	Get(ctx context.Context, id string) (*StoredResponse, error)
	Update(ctx context.Context, response *StoredResponse) error
	Delete(ctx context.Context, id string) error
	Close() error
}

func cloneResponse(src *StoredResponse) (*StoredResponse, error) {
	if src == nil {
		return nil, fmt.Errorf("response is nil")
	}
	normalized := normalizeStoredResponse(src)
	b, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}
	var dst StoredResponse
	if err := json.Unmarshal(b, &dst); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return &dst, nil
}

func normalizeStoredResponse(src *StoredResponse) *StoredResponse {
	if src == nil {
		return nil
	}

	normalized := *src
	normalized.Provider = strings.TrimSpace(normalized.Provider)
	normalized.ProviderName = strings.TrimSpace(normalized.ProviderName)
	normalized.ProviderResponseID = strings.TrimSpace(normalized.ProviderResponseID)
	normalized.RequestID = strings.TrimSpace(normalized.RequestID)
	normalized.UserPath = strings.TrimSpace(normalized.UserPath)
	normalized.WorkflowVersionID = strings.TrimSpace(normalized.WorkflowVersionID)

	if src.Response != nil {
		responseCopy := *src.Response
		if responseCopy.Provider == "" {
			responseCopy.Provider = normalized.Provider
		}
		if normalized.Provider == "" {
			normalized.Provider = strings.TrimSpace(responseCopy.Provider)
		}
		if normalized.ProviderResponseID == "" {
			normalized.ProviderResponseID = strings.TrimSpace(responseCopy.ID)
		}
		normalized.Response = &responseCopy
	}

	if len(src.InputItems) > 0 {
		normalized.InputItems = make([]json.RawMessage, 0, len(src.InputItems))
		for _, item := range src.InputItems {
			normalized.InputItems = append(normalized.InputItems, core.CloneRawJSON(item))
		}
	}

	return &normalized
}
