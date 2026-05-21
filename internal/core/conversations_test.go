package core

import (
	"strings"
	"testing"
)

func TestDecodeConversationCreateRequest(t *testing.T) {
	t.Run("empty body", func(t *testing.T) {
		req, err := DecodeConversationCreateRequest(nil)
		if err != nil {
			t.Fatalf("DecodeConversationCreateRequest() error = %v", err)
		}
		if req == nil || len(req.Items) != 0 || len(req.Metadata) != 0 {
			t.Fatalf("got %+v, want empty request", req)
		}
	})

	t.Run("items and metadata", func(t *testing.T) {
		req, err := DecodeConversationCreateRequest([]byte(`{"items":[{"type":"message"}],"metadata":{"k":"v"}}`))
		if err != nil {
			t.Fatalf("DecodeConversationCreateRequest() error = %v", err)
		}
		if len(req.Items) != 1 {
			t.Fatalf("items = %d, want 1", len(req.Items))
		}
		if req.Metadata["k"] != "v" {
			t.Fatalf("metadata[k] = %q, want v", req.Metadata["k"])
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		if _, err := DecodeConversationCreateRequest([]byte(`{`)); err == nil {
			t.Fatal("DecodeConversationCreateRequest() error = nil, want error")
		}
	})
}

func TestDecodeConversationUpdateRequest(t *testing.T) {
	t.Run("absent metadata", func(t *testing.T) {
		req, err := DecodeConversationUpdateRequest([]byte(`{}`))
		if err != nil {
			t.Fatalf("DecodeConversationUpdateRequest() error = %v", err)
		}
		if req.Metadata != nil {
			t.Fatalf("metadata = %v, want nil", req.Metadata)
		}
	})

	t.Run("explicit empty metadata", func(t *testing.T) {
		req, err := DecodeConversationUpdateRequest([]byte(`{"metadata":{}}`))
		if err != nil {
			t.Fatalf("DecodeConversationUpdateRequest() error = %v", err)
		}
		if req.Metadata == nil {
			t.Fatal("metadata = nil, want non-nil empty map")
		}
		if len(*req.Metadata) != 0 {
			t.Fatalf("metadata = %v, want empty", *req.Metadata)
		}
	})
}

func TestValidateConversationMetadata(t *testing.T) {
	t.Run("nil is valid", func(t *testing.T) {
		if err := ValidateConversationMetadata(nil); err != nil {
			t.Fatalf("ValidateConversationMetadata(nil) = %v, want nil", err)
		}
	})

	t.Run("too many pairs", func(t *testing.T) {
		metadata := make(map[string]string, maxConversationMetadataPairs+1)
		for i := 0; i <= maxConversationMetadataPairs; i++ {
			metadata[string(rune('a'+i))] = "v"
		}
		err := ValidateConversationMetadata(metadata)
		if err == nil {
			t.Fatal("ValidateConversationMetadata() = nil, want error")
		}
		if err.Param == nil || *err.Param != "metadata" {
			t.Fatalf("param = %v, want metadata", err.Param)
		}
	})

	t.Run("key too long", func(t *testing.T) {
		err := ValidateConversationMetadata(map[string]string{
			strings.Repeat("k", maxConversationMetadataKeyLength+1): "v",
		})
		if err == nil {
			t.Fatal("ValidateConversationMetadata() = nil, want error")
		}
	})

	t.Run("value too long", func(t *testing.T) {
		err := ValidateConversationMetadata(map[string]string{
			"k": strings.Repeat("v", maxConversationMetadataValueLength+1),
		})
		if err == nil {
			t.Fatal("ValidateConversationMetadata() = nil, want error")
		}
	})

	t.Run("multi-byte runes counted as characters not bytes", func(t *testing.T) {
		// "é" is one rune but two UTF-8 bytes: a key/value at the rune limit
		// stays valid even though its byte length exceeds the limit.
		key := strings.Repeat("é", maxConversationMetadataKeyLength)
		value := strings.Repeat("é", maxConversationMetadataValueLength)
		if err := ValidateConversationMetadata(map[string]string{key: value}); err != nil {
			t.Fatalf("ValidateConversationMetadata() = %v, want nil", err)
		}

		tooLongKey := strings.Repeat("é", maxConversationMetadataKeyLength+1)
		if err := ValidateConversationMetadata(map[string]string{tooLongKey: "v"}); err == nil {
			t.Fatal("ValidateConversationMetadata() = nil, want error for over-limit rune count")
		}
	})
}
