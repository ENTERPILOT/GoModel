package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedactSensitiveURLQuery(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "no query", raw: "/v1/models", want: "/v1/models"},
		{name: "ordinary query", raw: "/admin/usage?days=30&interval=daily", want: "/admin/usage?days=30&interval=daily"},
		{name: "oauth transaction", raw: "/sso/callback?code=c&state=s", want: "/sso/callback?code=REDACTED&state=REDACTED"},
		{name: "google api key", raw: "/v1beta/models?key=secret&pageSize=5", want: "/v1beta/models?key=REDACTED&pageSize=5"},
		{name: "api key spellings", raw: "/x?api_key=a&apikey=b&api-key=c", want: "/x?api-key=REDACTED&api_key=REDACTED&apikey=REDACTED"},
		{name: "sas signature", raw: "https://acct.blob.example.com/c/b?sv=2024&sig=secret", want: "https://acct.blob.example.com/c/b?sig=REDACTED&sv=2024"},
		{name: "signature", raw: "/file?expires=1&signature=secret", want: "/file?expires=1&signature=REDACTED"},
		{
			name: "aws presigned url",
			raw:  "https://bucket.example.com/o?X-Amz-Credential=id%2Fscope&X-Amz-Date=20260101T000000Z&X-Amz-Security-Token=tok&X-Amz-Signature=sig",
			want: "https://bucket.example.com/o?X-Amz-Credential=REDACTED&X-Amz-Date=20260101T000000Z&X-Amz-Security-Token=REDACTED&X-Amz-Signature=REDACTED",
		},
		{name: "case insensitive", raw: "/x?API_KEY=secret", want: "/x?API_KEY=REDACTED"},
		{name: "key is matched whole", raw: "/x?keyword=go&monkey=1", want: "/x?keyword=go&monkey=1"},
		{name: "malformed sensitive query", raw: "/x?key=secret;broken", want: "/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, RedactSensitiveURLQuery(tt.raw))
		})
	}
}
