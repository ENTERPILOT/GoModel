package core

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"github.com/goccy/go-json"
)

// ConversationObject is the value of the "object" field on a conversation.
const ConversationObject = "conversation"

// ConversationDeletedObject is the value of the "object" field returned by
// DELETE /v1/conversations/{id}.
const ConversationDeletedObject = "conversation.deleted"

// Conversation limits mirror the OpenAI Conversations API so the gateway keeps
// an OpenAI-compatible public contract.
const (
	// MaxConversationInitialItems caps the items array accepted by
	// POST /v1/conversations.
	MaxConversationInitialItems = 20

	maxConversationMetadataPairs       = 16
	maxConversationMetadataKeyLength   = 64
	maxConversationMetadataValueLength = 512
)

// Conversation is the OpenAI-compatible conversation resource returned by the
// /v1/conversations endpoints.
type Conversation struct {
	ID        string            `json:"id"`
	Object    string            `json:"object"` // "conversation"
	CreatedAt int64             `json:"created_at"`
	Metadata  map[string]string `json:"metadata"`
}

// ConversationDeleteResponse is returned by DELETE /v1/conversations/{id}.
type ConversationDeleteResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"` // "conversation.deleted"
	Deleted bool   `json:"deleted"`
}

// ConversationCreateRequest is the accepted body for POST /v1/conversations.
// Items are stored as opaque JSON so the gateway accepts any item shape the
// client sends without constraining future item-list support.
type ConversationCreateRequest struct {
	Items    []json.RawMessage `json:"items,omitempty" swaggertype:"array,object"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ConversationUpdateRequest is the accepted body for POST /v1/conversations/{id}.
// Metadata is a pointer so the handler can tell an absent field apart from an
// explicit empty object: OpenAI requires metadata on update.
type ConversationUpdateRequest struct {
	Metadata *map[string]string `json:"metadata" binding:"required"`
}

// DecodeConversationCreateRequest parses a conversation create body. An empty
// body is treated as an empty request (a conversation with no items/metadata).
func DecodeConversationCreateRequest(data []byte) (*ConversationCreateRequest, error) {
	req := &ConversationCreateRequest{}
	if len(bytes.TrimSpace(data)) == 0 {
		return req, nil
	}
	if err := json.Unmarshal(data, req); err != nil {
		return nil, err
	}
	return req, nil
}

// DecodeConversationUpdateRequest parses a conversation update body. An empty
// body decodes to a request with no metadata, which the handler rejects.
func DecodeConversationUpdateRequest(data []byte) (*ConversationUpdateRequest, error) {
	req := &ConversationUpdateRequest{}
	if len(bytes.TrimSpace(data)) == 0 {
		return req, nil
	}
	if err := json.Unmarshal(data, req); err != nil {
		return nil, err
	}
	return req, nil
}

// ValidateConversationMetadata enforces the OpenAI metadata limits (at most 16
// pairs, keys up to 64 characters, values up to 512 characters). It returns nil
// when the metadata is acceptable.
func ValidateConversationMetadata(metadata map[string]string) *GatewayError {
	if len(metadata) > maxConversationMetadataPairs {
		return NewInvalidRequestError(
			fmt.Sprintf("metadata supports at most %d key-value pairs", maxConversationMetadataPairs), nil,
		).WithParam("metadata")
	}
	for key, value := range metadata {
		// Limits are character (rune) counts, not byte lengths, so multi-byte
		// keys and values are not rejected prematurely.
		if utf8.RuneCountInString(key) > maxConversationMetadataKeyLength {
			return NewInvalidRequestError(
				fmt.Sprintf("metadata keys support at most %d characters", maxConversationMetadataKeyLength), nil,
			).WithParam("metadata")
		}
		if utf8.RuneCountInString(value) > maxConversationMetadataValueLength {
			return NewInvalidRequestError(
				fmt.Sprintf("metadata values support at most %d characters", maxConversationMetadataValueLength), nil,
			).WithParam("metadata")
		}
	}
	return nil
}
