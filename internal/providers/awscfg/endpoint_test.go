package awscfg

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsBedrockEndpoint(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{url: "https://bedrock-runtime.us-east-1.amazonaws.com", want: true},
		{url: "https://bedrock.us-east-1.amazonaws.com/", want: true},
		{url: "https://bedrock-runtime-fips.us-gov-west-1.amazonaws.com", want: true},
		{url: "https://bedrock-fips.us-east-1.amazonaws.com", want: true},
		{url: "https://bedrock-runtime.us-east-1.api.aws", want: true},
		{url: "https://bedrock-mantle.us-west-2.api.aws/v1", want: true},
		{url: "https://vpce-0abc123-xyz.bedrock-runtime.us-east-1.vpce.amazonaws.com", want: true},
		{url: "https://vpce-0abc123-xyz-us-east-1a.bedrock-runtime.us-east-1.vpce.amazonaws.com", want: true},
		{url: "https://bedrock-runtime.cn-north-1.amazonaws.com.cn", want: true},
		{url: "https://bedrock-runtime.eusc-de-east-1.amazonaws.eu", want: true},
		{url: "HTTPS://Bedrock-Runtime.us-east-1.AmazonAWS.com.", want: true},
		{url: " https://bedrock-runtime.us-east-1.amazonaws.com:443 ", want: true},

		{url: "", want: false},
		{url: "us-east-1", want: false},
		{url: "http://bedrock-runtime.us-east-1.amazonaws.com", want: false},
		{url: "https://bedrock.internal.example.com", want: false},
		{url: "https://amazonaws.com", want: false},
		{url: "https://bedrock-runtime.us-east-1.notamazonaws.com", want: false},
		{url: "https://bedrock-runtime.us-east-1.amazonaws.com.example.com", want: false},
		{url: "https://bedrock-runtime.us-east-1.amazonaws.com@example.com", want: false},
		{url: "https://user@bedrock-runtime.us-east-1.amazonaws.com", want: false},
		{url: "https://example.com/bedrock-runtime.us-east-1.amazonaws.com", want: false},
		{url: "https://example.com/#bedrock-runtime.us-east-1.amazonaws.com", want: false},
		{url: "https://example.com?x=bedrock-runtime.us-east-1.amazonaws.com", want: false},
		{url: "https://127.0.0.1:8443", want: false},
		{url: "https://bedrock-runtime.us-east-1.amazonaws.com:bad", want: false},
		// Other names under AWS's suffixes belong to AWS customers.
		{url: "https://abc123.execute-api.us-east-1.amazonaws.com", want: false},
		{url: "https://bedrock.execute-api.us-east-1.amazonaws.com", want: false},
		{url: "https://bedrock-runtime.s3.amazonaws.com", want: false},
		{url: "https://bedrock-runtime.s3.us-east-1.amazonaws.com", want: false},
		{url: "https://bedrock-lb-123.us-east-1.elb.amazonaws.com", want: false},
		{url: "https://ec2-203-0-113-7.us-west-2.compute.amazonaws.com", want: false},
		{url: "https://vpce-0abc-xyz.vpce-svc-0123.us-east-1.vpce.amazonaws.com", want: false},
		{url: "https://sts.us-east-1.amazonaws.com", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			assert.Equal(t, tt.want, IsBedrockEndpoint(tt.url))
		})
	}
}
