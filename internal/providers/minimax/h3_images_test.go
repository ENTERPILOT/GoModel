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
			server, capture := providertest.JSONServer(t, http.StatusOK, `{"created":1792308678,"request_id":"trace","data":[{`+tc.result+`,"size":"2720x1536","revised_prompt":"A lighthouse at dawn"},{"error":{"code":"1027","message":"image withheld"}}],"output_format":"jpeg","usage":{"generated_images":1,"total_tokens":7254,"input_tokens":3174,"output_tokens":4080,"input_tokens_details":{"text_tokens":3174,"image_tokens":0},"output_tokens_details":{"image_tokens":4080}}}`)
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
			assert.Equal(t, 3174, resp.Usage.InputTokens)
			assert.Equal(t, 4080, resp.Usage.OutputTokens)
			assert.Equal(t, 1, resp.GeneratedImageCount())
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
		name    string
		status  int
		body    string
		want    int
		message string
	}{
		{"limited", 429, `{"type":"error","error":{"type":"rate_limit_error","message":"too many requests (1002)"}}`, 429, "too many requests (1002)"},
		{"balance", 402, `{"type":"error","error":{"type":"insufficient_balance_error","message":"insufficient balance (1008)"}}`, 402, "insufficient balance (1008)"},
		// MiniMax answers a batch that failed entirely with an HTTP error
		// that still carries data and usage.
		{"all withheld", 422, `{"type":"error","error":{"type":"unprocessable_entity_error","message":"all images failed content safety check (1027)","http_code":"422"},"data":[{"error":{"code":"1027","message":"output image failed content safety check"}}],"usage":{"generated_images":0,"total_tokens":0},"request_id":"trace"}`, 422, "all images failed content safety check (1027)"},
		{"all failed with HTTP 200", 200, `{"request_id":"trace","data":[{"error":{"code":"1027","message":"output image failed content safety check"}}]}`, 502, "output image failed content safety check (1027), request_id trace"},
		{"malformed", 200, `{"data":[{}]}`, 502, "contains no images"},
		{"empty", 200, `{"data":[]}`, 502, "contains no images"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, tc.status, tc.body)
			p := newTestProvider("test-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})
			_, err := p.CreateImage(t.Context(), &core.ImageGenerationRequest{Model: "MiniMax-H3-Image", Prompt: "A lighthouse"})
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tc.want, gatewayErr.HTTPStatusCode())
			assert.Contains(t, gatewayErr.Message, tc.message)
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

// The images of a partially successful batch are already billed, so an item
// that came back with neither an image nor an error must not discard them.
func TestCreateH3ImageKeepsImagesBesideEmptyItem(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"created":1792308678,"data":[{"url":"https://example.com/image.jpg","size":"2720x1536"},{}]}`)
	p := newTestProvider("test-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})

	resp, err := p.CreateImage(t.Context(), &core.ImageGenerationRequest{Model: "MiniMax-H3-Image", Prompt: "A lighthouse", N: new(2)})

	require.NoError(t, err)
	require.Len(t, resp.Data, 2)
	assert.Equal(t, "https://example.com/image.jpg", resp.Data[0].URL)
	assert.Equal(t, 1, resp.GeneratedImageCount())
}

func TestH3ImageModel(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"MiniMax-H3-Image", "MiniMax-H3-Image"},
		{"MiniMax-H3-Image-Highspeed", "MiniMax-H3-Image-Highspeed"},
		{" minimax-h3-image ", "MiniMax-H3-Image"},
		{"MINIMAX-H3-IMAGE-HIGHSPEED", "MiniMax-H3-Image-Highspeed"},
		{"image-01", ""},
		{"image-01-live", ""},
		{"MiniMax-H3", ""},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got, ok := h3ImageModel(tt.model)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.want != "", ok)
		})
	}
}

// A loosely spelled H3 model is routed to H3, so MiniMax must receive the
// model ID it knows rather than the client's spelling.
func TestCreateH3ImageSendsCanonicalModel(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"created":1792308678,"data":[{"url":"https://example.com/image.jpg"}]}`)
	p := newTestProvider("test-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})
	req := &core.ImageGenerationRequest{Model: " minimax-h3-image-highspeed ", Prompt: "A lighthouse"}

	_, err := p.CreateImage(t.Context(), req)

	require.NoError(t, err)
	sent := capture.Last(t)
	assert.Equal(t, "/v1/images/generations", sent.Path)
	assert.Equal(t, "MiniMax-H3-Image-Highspeed", sent.JSON(t)["model"])
	assert.Equal(t, " minimax-h3-image-highspeed ", req.Model, "the caller's request is not mutated")
}
