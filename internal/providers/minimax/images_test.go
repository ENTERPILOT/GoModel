package minimax

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateImage(t *testing.T) {
	for _, model := range []string{"image-01", "image-01-live"} {
		for _, format := range []string{"", "url", "b64_json", "base64"} {
			t.Run(model+"/"+format, func(t *testing.T) {
				server, capture := providertest.JSONServer(t, http.StatusOK, `{"data":{"image_urls":["https://example.com/image.png"],"image_base64":["aW1hZ2U="]},"metadata":{"success_count":"1","failed_count":"1"},"base_resp":{"status_code":0}}`)
				provider := NewWithHTTPClient("test-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})
				var req core.ImageGenerationRequest
				require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{"model":%q,"prompt":"A lighthouse","n":2,"size":"1024x1024","response_format":%q,"provider":"minimax","user":"local-user","seed":42,"prompt_optimizer":false}`, model, format)), &req))
				before, err := json.Marshal(req)
				require.NoError(t, err)
				resp, err := provider.CreateImage(context.Background(), &req)
				require.NoError(t, err)
				require.Len(t, resp.Data, 1)
				assert.Positive(t, resp.Created)
				upstream := capture.Last(t)
				assert.Equal(t, "/v1/image_generation", upstream.Path)
				assert.Equal(t, http.MethodPost, upstream.Method)
				assert.Equal(t, "Bearer test-key", upstream.Header.Get("Authorization"))
				fields := upstream.JSON(t)
				assert.Equal(t, model, fields["model"])
				assert.Equal(t, float64(2), fields["n"])
				assert.Equal(t, float64(1024), fields["width"])
				assert.Equal(t, float64(1024), fields["height"])
				assert.Equal(t, float64(42), fields["seed"])
				assert.Equal(t, false, fields["prompt_optimizer"])
				for _, key := range []string{"size", "provider", "user"} {
					assert.NotContains(t, fields, key)
				}
				if format == "" || format == "url" {
					assert.Equal(t, "url", fields["response_format"])
					assert.Equal(t, "https://example.com/image.png", resp.Data[0].URL)
				} else {
					assert.Equal(t, "base64", fields["response_format"])
					assert.Equal(t, "aW1hZ2U=", resp.Data[0].B64JSON)
				}
				after, err := json.Marshal(req)
				require.NoError(t, err)
				assert.JSONEq(t, string(before), string(after))
			})
		}
	}
}

func TestCreateImageNativeDimensions(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"data":{"image_urls":["https://example.com/image.png"]},"base_resp":{"status_code":0}}`)
	p := NewWithHTTPClient("test-key", server.URL, server.Client(), llmclient.Hooks{})
	var req core.ImageGenerationRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"image-01","prompt":"A lighthouse","aspect_ratio":"16:9"}`), &req))
	_, err := p.CreateImage(context.Background(), &req)
	require.NoError(t, err)
	fields := capture.Last(t).JSON(t)
	assert.Equal(t, "16:9", fields["aspect_ratio"])
	assert.NotContains(t, fields, "width")
}

func TestCreateImageErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		status, want int
	}{
		{"rate limit", `{"base_resp":{"status_code":1002}}`, 200, 429},
		{"authentication", `{"base_resp":{"status_code":1004}}`, 200, 401},
		{"invalid key", `{"base_resp":{"status_code":2049}}`, 200, 401},
		{"balance", `{"base_resp":{"status_code":1008}}`, 200, 402},
		{"sensitive", `{"base_resp":{"status_code":1026}}`, 200, 400},
		{"parameters", `{"base_resp":{"status_code":2013}}`, 200, 400},
		{"unknown", `{"base_resp":{"status_code":9999}}`, 200, 502},
		{"empty", `{"data":{"image_urls":[]},"base_resp":{"status_code":0}}`, 200, 502},
		{"missing status", `{"data":{"image_urls":["https://example.com/image.png"]}}`, 200, 502},
		{"malformed", `{`, 200, 502},
		{"http error", `{"error":{"message":"invalid request"}}`, 400, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := providertest.JSONServer(t, tc.status, tc.body)
			p := NewWithHTTPClient("test-key", server.URL, server.Client(), llmclient.Hooks{})
			_, err := p.CreateImage(context.Background(), &core.ImageGenerationRequest{Model: "image-01", Prompt: "A lighthouse"})
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tc.want, gatewayErr.HTTPStatusCode())
		})
	}
}

func TestCreateImageValidation(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, `{"model":"image-01"}`,
		`{"model":"image-01","prompt":"A lighthouse","stream":true}`,
		`{"model":"image-01","prompt":"A lighthouse","response_format":"invalid"}`,
		`{"model":"image-01","prompt":"A lighthouse","quality":"high"}`,
		`{"model":"image-01","prompt":"A lighthouse","size":"large"}`,
		`{"model":"image-01","prompt":"A lighthouse","size":"513x1024"}`,
		`{"model":"image-01","prompt":"A lighthouse","size":"1024x1024","aspect_ratio":"1:1"}`,
	} {
		t.Run(body, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, 200, `{}`)
			p := NewWithHTTPClient("test-key", server.URL, server.Client(), llmclient.Hooks{})
			var req *core.ImageGenerationRequest
			require.NoError(t, json.Unmarshal([]byte(body), &req))
			_, err := p.CreateImage(context.Background(), req)
			require.Error(t, err)
			assert.Zero(t, capture.Count())
		})
	}
}
