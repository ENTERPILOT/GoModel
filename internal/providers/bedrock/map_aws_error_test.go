package bedrock

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// An AWS API error carries the service's own message, so the gateway error
// marks it as upstream text for the audit log; other errors keep theirs.
func TestMapAWSErrorMarksServiceMessages(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "api error", err: &smithy.GenericAPIError{Code: "ValidationException", Message: "input is invalid"}, want: true},
		{name: "other error", err: errors.New("dial tcp: connection refused"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, mapAWSError(tt.err), &gatewayErr)
			assert.Equal(t, tt.want, gatewayErr.MessageFromUpstream())
		})
	}
}
