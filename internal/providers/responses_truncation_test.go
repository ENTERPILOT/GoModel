package providers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// "disabled" is OpenAI's documented default and asks for nothing, so a client
// that always sets truncation can still reach a chat-translated provider. Only
// "auto" asks for behaviour translation cannot provide.
func TestConvertResponsesRequestToChatTruncation(t *testing.T) {
	tests := []struct {
		name       string
		truncation string
		wantErr    bool
	}{
		{name: "absent"},
		{name: "disabled", truncation: "disabled"},
		{name: "disabled with surrounding space", truncation: " disabled "},
		{name: "auto", truncation: "auto", wantErr: true},
		{name: "unknown value", truncation: "sometimes", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ResponsesRequest{Model: "claude-haiku-4-5", Input: "hi", Truncation: tt.truncation}

			chatReq, err := ConvertResponsesRequestToChat(req)
			if !tt.wantErr {
				require.NoError(t, err)
				assert.NotNil(t, chatReq)
				return
			}

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			require.NotNil(t, gatewayErr.Param)
			assert.Equal(t, "truncation", *gatewayErr.Param)
		})
	}
}
