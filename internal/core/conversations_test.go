package core

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeConversationCreateRequest(t *testing.T) {
	t.Run("empty body", func(t *testing.T) {
		req, err := DecodeConversationCreateRequest(nil)
		require.NoError(t, err)
		require.NotNil(t, req)
		require.Empty(t, req.Items)
		require.Empty(t, req.Metadata)
	})

	t.Run("items and metadata", func(t *testing.T) {
		req, err := DecodeConversationCreateRequest([]byte(`{"items":[{"type":"message"}],"metadata":{"k":"v"}}`))
		require.NoError(t, err)
		require.Len(t, req.Items, 1)
		require.Equal(t, "v", req.Metadata["k"])
	})

	t.Run("invalid json", func(t *testing.T) {
		_, err := DecodeConversationCreateRequest([]byte(`{`))
		require.Error(t, err)
	})
}

func TestDecodeConversationUpdateRequest(t *testing.T) {
	t.Run("absent metadata", func(t *testing.T) {
		req, err := DecodeConversationUpdateRequest([]byte(`{}`))
		require.NoError(t, err)
		require.Nil(t, req.Metadata)
	})

	t.Run("explicit empty metadata", func(t *testing.T) {
		req, err := DecodeConversationUpdateRequest([]byte(`{"metadata":{}}`))
		require.NoError(t, err)
		require.NotNil(t, req.Metadata)
		require.Empty(t, *req.Metadata)
	})
}

func TestValidateConversationMetadata(t *testing.T) {
	t.Run("nil is valid", func(t *testing.T) {
		err := ValidateConversationMetadata(nil)
		require.Nil(t, err)
	})

	t.Run("too many pairs", func(t *testing.T) {
		metadata := make(map[string]string, MaxConversationMetadataPairs+1)
		for i := 0; i <= MaxConversationMetadataPairs; i++ {
			metadata[string(rune('a'+i))] = "v"
		}
		err := ValidateConversationMetadata(metadata)
		require.NotNil(t, err)
		require.NotNil(t, err.Param)
		require.Equal(t, "metadata", *err.Param)
	})

	t.Run("key too long", func(t *testing.T) {
		err := ValidateConversationMetadata(map[string]string{
			strings.Repeat("k", maxConversationMetadataKeyLength+1): "v",
		})
		require.NotNil(t, err)
	})

	t.Run("value too long", func(t *testing.T) {
		err := ValidateConversationMetadata(map[string]string{
			"k": strings.Repeat("v", maxConversationMetadataValueLength+1),
		})
		require.NotNil(t, err)
	})

	t.Run("multi-byte runes counted as characters not bytes", func(t *testing.T) {
		// "é" is one rune but two UTF-8 bytes: a key/value at the rune limit
		// stays valid even though its byte length exceeds the limit.
		key := strings.Repeat("é", maxConversationMetadataKeyLength)
		value := strings.Repeat("é", maxConversationMetadataValueLength)
		err := ValidateConversationMetadata(map[string]string{key: value})
		require.Nil(t, err)

		tooLongKey := strings.Repeat("é", maxConversationMetadataKeyLength+1)
		err = ValidateConversationMetadata(map[string]string{tooLongKey: "v"})
		require.NotNil(t, err)
	})
}
