package providers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsBedrockEndpoint(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{url: "us-east-1", want: true},
		{url: " eu-central-1 ", want: true},
		{url: "us-gov-west-1", want: true},
		{url: "cn-north-1", want: true},
		{url: "https://bedrock.us-east-1.amazonaws.com", want: true},
		{url: "https://bedrock-runtime.us-east-1.amazonaws.com", want: true},
		{url: "https://bedrock-runtime.us-east-1.amazonaws.com/openai/v1", want: true},
		{url: "https://bedrock-runtime-fips.us-gov-west-1.amazonaws.com", want: true},
		{url: "https://bedrock-fips.us-east-1.amazonaws.com", want: true},
		{url: "https://bedrock-runtime.cn-north-1.amazonaws.com.cn", want: true},
		{url: "https://bedrock-runtime.us-east-1.api.aws", want: true},
		{url: "https://bedrock-mantle.us-east-1.api.aws/v1", want: true},
		{url: "https://vpce-0a1b2c3d4e5f67890-abcd1234.bedrock-runtime.us-east-1.vpce.amazonaws.com", want: true},
		{url: "https://vpce-0a1b2c3d4e5f67890-abcd1234-us-east-1a.bedrock.us-east-1.vpce.amazonaws.com", want: true},
		{url: "HTTPS://Bedrock-Runtime.US-East-1.AmazonAWS.com.", want: true},
		{url: " https://bedrock-runtime.us-east-1.amazonaws.com:443 ", want: true},

		{url: "", want: false},
		{url: "s3", want: false},
		{url: "US-EAST-1", want: false},
		{url: "us-east-1.example.com", want: false},
		{url: "us-east-1/x", want: false},
		{url: "bedrock-runtime.us-east-1.amazonaws.com", want: false},
		{url: "http://bedrock-runtime.us-east-1.amazonaws.com", want: false},
		{url: "https://bedrock-runtime.us-east-1.amazonaws.com:8443", want: false},
		{url: "https://bedrock-runtime.us-east-1.amazonaws.com:bad", want: false},
		{url: "https://bedrock-runtime.us-east-1.amazonaws.com.example.com", want: false},
		{url: "https://bedrock-runtime.us-east-1.amazonaws.com@example.com", want: false},
		{url: "https://user@bedrock-runtime.us-east-1.amazonaws.com", want: false},
		{url: "https://example.com/bedrock-runtime.us-east-1.amazonaws.com", want: false},
		{url: "https://example.com/#bedrock-runtime.us-east-1.amazonaws.com", want: false},
		{url: "https://example.com?x=bedrock-runtime.us-east-1.amazonaws.com", want: false},
		{url: "https://bedrock.internal.example.com", want: false},
		{url: "https://127.0.0.1", want: false},
		{url: "https://amazonaws.com", want: false},
		{url: "https://us-east-1.amazonaws.com", want: false},
		// Other amazonaws.com names are not Bedrock, and some are served for an
		// AWS customer: an API Gateway API, an S3 bucket, a PrivateLink service.
		{url: "https://abc123.execute-api.us-east-1.amazonaws.com", want: false},
		{url: "https://bedrock-runtime.execute-api.us-east-1.amazonaws.com", want: false},
		{url: "https://bedrock-runtime.s3.amazonaws.com", want: false},
		{url: "https://bedrock-runtime.s3.us-east-1.amazonaws.com", want: false},
		{url: "https://sts.us-east-1.amazonaws.com", want: false},
		{url: "https://evil.bedrock-runtime.us-east-1.amazonaws.com", want: false},
		{url: "https://evil-bedrock-runtime.us-east-1.amazonaws.com", want: false},
		{url: "https://bedrock-agent.us-east-1.amazonaws.com", want: false},
		{url: "https://bedrock-runtime.us_east_1.amazonaws.com", want: false},
		{url: "https://bedrock-runtime.us-east-1.evil.amazonaws.com", want: false},
		{url: "https://vpce-0a1b-abcd.vpce-svc-0123456789abcdef0.us-east-1.vpce.amazonaws.com", want: false},
		{url: "https://evil.bedrock-runtime.us-east-1.vpce.amazonaws.com", want: false},
		{url: "https://x.vpce-0a1b.bedrock-runtime.us-east-1.vpce.amazonaws.com", want: false},
		{url: "https://vpce-0a1b-abcd.bedrock-runtime.us-east-1.vpce.amazonaws.com.example.com", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			assert.Equal(t, tt.want, IsBedrockEndpoint(tt.url))
		})
	}
}
