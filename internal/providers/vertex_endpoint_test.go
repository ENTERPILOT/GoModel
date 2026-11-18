package providers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsVertexEndpoint(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{url: "https://aiplatform.googleapis.com", want: true},
		{url: "https://aiplatform.googleapis.com/v1/projects/p/locations/global/publishers/google", want: true},
		{url: "https://us-central1-aiplatform.googleapis.com/v1/projects/p/locations/us-central1/endpoints/openapi", want: true},
		{url: "https://northamerica-northeast1-aiplatform.googleapis.com", want: true},
		{url: "https://aiplatform.us.rep.googleapis.com", want: true},
		{url: "https://aiplatform.eu.rep.googleapis.com/v1", want: true},
		{url: "https://aiplatform.us-central1.rep.googleapis.com", want: true},
		{url: "HTTPS://AIPlatform.GoogleAPIs.com.", want: true},
		{url: " https://aiplatform.googleapis.com:443 ", want: true},

		{url: "", want: false},
		{url: "aiplatform.googleapis.com", want: false},
		{url: "http://aiplatform.googleapis.com", want: false},
		{url: "https://aiplatform.googleapis.com:8443", want: false},
		{url: "https://aiplatform.googleapis.com:bad", want: false},
		{url: "https://aiplatform.googleapis.com.example.com", want: false},
		{url: "https://aiplatform.googleapis.com@example.com", want: false},
		{url: "https://user@aiplatform.googleapis.com", want: false},
		{url: "https://example.com/aiplatform.googleapis.com", want: false},
		{url: "https://example.com/#aiplatform.googleapis.com", want: false},
		{url: "https://example.com?x=aiplatform.googleapis.com", want: false},
		{url: "https://vertex.internal.example.com", want: false},
		{url: "https://127.0.0.1", want: false},
		{url: "https://googleapis.com", want: false},
		// Other googleapis.com names are not Vertex AI, and Private Service
		// Connect names resolve through DNS the customer controls.
		{url: "https://storage.googleapis.com", want: false},
		{url: "https://generativelanguage.googleapis.com", want: false},
		{url: "https://aiplatform-myendpoint.p.googleapis.com", want: false},
		{url: "https://us-central1-aiplatform-myendpoint.p.googleapis.com", want: false},
		{url: "https://evil.aiplatform.googleapis.com", want: false},
		{url: "https://evil-aiplatform.googleapis.com", want: false},
		{url: "https://us-aiplatform.googleapis.com", want: false},
		{url: "https://aiplatform.evil.rep.googleapis.com", want: false},
		{url: "https://us_central1-aiplatform.googleapis.com", want: false},
		{url: "https://aiplatform.evil.example.rep.googleapis.com", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			assert.Equal(t, tt.want, IsVertexEndpoint(tt.url))
		})
	}
}
