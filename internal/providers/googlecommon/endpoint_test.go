package googlecommon

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
		{url: "https://us_central1-aiplatform.googleapis.com", want: false},
		{url: "https://aiplatform.evil.example.rep.googleapis.com", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			assert.Equal(t, tt.want, IsVertexEndpoint(tt.url))
		})
	}
}

func TestUsesHostCredentials(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "default is ADC", cfg: Config{}, want: true},
		{name: "explicit ADC", cfg: Config{AuthType: "gcp_adc"}, want: true},
		{name: "explicit ADC beside own JSON", cfg: Config{AuthType: "adc", ServiceAccountJSON: "{}"}, want: true},
		{name: "key file on the host", cfg: Config{AuthType: "gcp_service_account", ServiceAccountFile: "/etc/sa.json"}, want: true},
		{name: "inferred key file on the host", cfg: Config{ServiceAccountFile: "/etc/sa.json"}, want: true},
		{name: "own JSON", cfg: Config{AuthType: "gcp_service_account", ServiceAccountJSON: "{}"}, want: false},
		{name: "own base64 JSON", cfg: Config{AuthType: "service_account", ServiceAccountJSONBase64: "e30="}, want: false},
		{name: "inferred own JSON", cfg: Config{ServiceAccountJSON: "{}"}, want: false},
		{name: "own JSON wins over key file", cfg: Config{ServiceAccountJSON: "{}", ServiceAccountFile: "/etc/sa.json"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, UsesHostCredentials(tt.cfg))
		})
	}
}
