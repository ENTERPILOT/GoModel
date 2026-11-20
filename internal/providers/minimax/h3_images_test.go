package minimax

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateH3Image(t *testing.T) {
	for _, tc := range []struct{ name, model, options, result string }{
		{"standard", "MiniMax-H3-Image", `"size":"2720x1536"`, `"url":"https://example.com/image.jpg"`},
		{"highspeed", "MiniMax-H3-Image-Highspeed", `"response_format":"base64","area":"2K","aspect_ratio":"16:9","aigc_watermark":true`, `"b64_json":"aW1hZ2U="`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{"created":1792308678,"request_id":"trace","data":[{`+tc.result+`,"size":"2720x1536","revised_prompt":"A lighthouse at dawn"},{"error":{"code":"1027","message":"image withheld"}}],"output_format":"jpeg","usage":{"generated_images":1,"total_tokens":7254}}`)
			p := newTestProvider("test-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})
			var req core.ImageGenerationRequest
			require.NoError(t, json.Unmarshal([]byte(`{"model":"`+tc.model+`","provider":"minimax","user":"local","prompt":"A lighthouse","n":2,"output_format":"jpeg","disable_prompt_enhanced":true,`+tc.options+`}`), &req))
			resp, err := p.CreateImage(t.Context(), &req)
			require.NoError(t, err)
			sent := capture.Last(t)
			assert.Equal(t, http.MethodPost, sent.Method)
			assert.Equal(t, "/v1/images/generations", sent.Path)
			assert.Equal(t, "Bearer test-key", sent.Header.Get("Authorization"))
			fields := sent.JSON(t)
			assert.Equal(t, tc.model, fields["model"])
			assert.Equal(t, "A lighthouse", fields["prompt"])
			assert.Equal(t, 2.0, fields["n"])
			assert.Equal(t, true, fields["disable_prompt_enhanced"])
			assert.Equal(t, "jpeg", fields["output_format"])
			assert.NotContains(t, fields, "user")
			assert.NotContains(t, fields, "provider")
			assert.Equal(t, "local", req.User)
			assert.Equal(t, int64(1792308678), resp.Created)
			assert.Equal(t, "trace", resp.RequestID)
			assert.Equal(t, "jpeg", resp.OutputFormat)
			require.NotNil(t, resp.Usage)
			assert.Equal(t, 7254, resp.Usage.TotalTokens)
			require.Len(t, resp.Data, 2)
			assert.Equal(t, "2720x1536", resp.Data[0].Size)
			assert.Equal(t, "A lighthouse at dawn", resp.Data[0].RevisedPrompt)
			assert.Equal(t, &core.ImageItemError{Code: "1027", Message: "image withheld"}, resp.Data[1].Error)
			if tc.name == "standard" {
				assert.Equal(t, "https://example.com/image.jpg", resp.Data[0].URL)
				assert.Equal(t, "2720x1536", fields["size"])
				assert.NotContains(t, fields, "width")
				assert.NotContains(t, fields, "aigc_watermark")
			} else {
				assert.Equal(t, "aW1hZ2U=", resp.Data[0].B64JSON)
				assert.Equal(t, "b64_json", fields["response_format"])
				assert.Equal(t, "base64", req.ResponseFormat)
				assert.Equal(t, "2K", fields["area"])
				assert.Equal(t, "16:9", fields["aspect_ratio"])
				assert.Equal(t, true, fields["aigc_watermark"])
			}
		})
	}
}

func TestCreateH3ImageErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"limited", 429, `{"type":"error","error":{"type":"rate_limit_error","message":"too many requests (1002)"}}`, 429},
		{"balance", 402, `{"type":"error","error":{"type":"insufficient_balance_error","message":"insufficient balance (1008)"}}`, 402},
		{"malformed", 200, `{"data":[{}]}`, 502},
		{"empty", 200, `{"data":[]}`, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, tc.status, tc.body)
			p := newTestProvider("test-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})
			_, err := p.CreateImage(t.Context(), &core.ImageGenerationRequest{Model: "MiniMax-H3-Image", Prompt: "A lighthouse"})
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tc.want, gatewayErr.HTTPStatusCode())
			assert.Equal(t, 1, capture.Count(), "a billed generation must never be retried")
		})
	}
}

func TestCreateH3ImageValidation(t *testing.T) {
	server, capture := providertest.JSONServer(t, 200, `{}`)
	p := newTestProvider("test-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})
	for _, req := range []core.ImageGenerationRequest{
		{Model: "MiniMax-H3-Image", Prompt: "A lighthouse", N: new(11)},
		{Model: "MiniMax-H3-Image", Prompt: "A lighthouse", ResponseFormat: "other"},
	} {
		_, err := p.CreateImage(t.Context(), &req)
		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr)
		assert.Equal(t, 400, gatewayErr.HTTPStatusCode())
	}
	assert.Zero(t, capture.Count())
}
