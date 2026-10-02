package openai

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/providers"
)

// Compatible must expose the adapter the chat-centric surface was built
// with, so providers serving native Responses use the same instance.
func TestChatCompatible_Compatible(t *testing.T) {
	chat := NewChatCompatible("key", providers.ProviderOptions{}, CompatibleProviderConfig{
		ProviderName: "test",
		BaseURL:      "https://example.com/v1",
	})
	require.NotNil(t, chat)
	assert.Same(t, chat.compatible, chat.Compatible())
}
