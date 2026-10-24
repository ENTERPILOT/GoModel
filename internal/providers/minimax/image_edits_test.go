package minimax

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func portraitRequest() *core.ImageEditRequest {
	return &core.ImageEditRequest{Model: "image-01", Prompt: "A portrait in a garden", Images: []core.ImageFile{{Data: []byte("\x89PNG\r\n\x1a\nportrait")}}}
}

func TestCreateImageEdit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields []core.FormField
		assert func(t *testing.T, data []core.ImageData)
	}{
		{
			name:   "default response format is url",
			fields: nil,
			assert: func(t *testing.T, data []core.ImageData) {
				assert.Equal(t, "https://example.com/portrait.png", data[0].URL)
				assert.Empty(t, data[0].B64JSON)
			},
		},
		{
			name:   "url",
			fields: []core.FormField{{Name: "response_format", Value: "url"}},
			assert: func(t *testing.T, data []core.ImageData) {
				assert.Equal(t, "https://example.com/portrait.png", data[0].URL)
			},
		},
		{
			name:   "b64_json",
			fields: []core.FormField{{Name: "response_format", Value: "b64_json"}},
			assert: func(t *testing.T, data []core.ImageData) {
				assert.Equal(t, "cG9ydHJhaXQ=", data[0].B64JSON)
				assert.Empty(t, data[0].URL)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{"data":{"image_urls":["https://example.com/portrait.png"],"image_base64":["cG9ydHJhaXQ="]},"base_resp":{"status_code":0}}`)
			provider := newTestProvider("minimax-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})
			req := portraitRequest()
			req.Fields = tc.fields
			response, err := provider.CreateImageEdit(context.Background(), req)
			require.NoError(t, err)
			require.Len(t, response.Data, 1)
			tc.assert(t, response.Data)
			recorded := capture.Last(t)
			assert.Equal(t, http.MethodPost, recorded.Method)
			assert.Equal(t, "/v1/image_generation", recorded.Path)
			assert.Equal(t, "Bearer minimax-key", recorded.Header.Get("Authorization"))
			body := recorded.JSON(t)
			assert.Equal(t, "image-01", body["model"])
			references, ok := body["subject_reference"].([]any)
			require.True(t, ok)
			assert.Equal(t, map[string]any{"type": "character", "image_file": "data:image/png;base64," + base64.StdEncoding.EncodeToString(req.Images[0].Data)}, references[0])
		})
	}
}

func TestCreateImageEditMapsNativeOptions(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"data":{"image_urls":["https://example.com/portrait.png"]},"base_resp":{"status_code":0}}`)
	provider := newTestProvider("minimax-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})
	req := portraitRequest()
	req.Fields = []core.FormField{{Name: "size", Value: "1024x768"}, {Name: "n", Value: "2"}, {Name: "seed", Value: "42"}, {Name: "prompt_optimizer", Value: "true"}}

	_, err := provider.CreateImageEdit(context.Background(), req)

	require.NoError(t, err)
	body := capture.Last(t).JSON(t)
	assert.Equal(t, float64(1024), body["width"])
	assert.Equal(t, float64(768), body["height"])
	assert.Equal(t, float64(2), body["n"])
	assert.Equal(t, float64(42), body["seed"])
	assert.Equal(t, true, body["prompt_optimizer"])
}

// TestCreateImageEditNativeStatusErrors pins the MiniMax HTTP-200 failure
// mapping: callers must see the real meaning of a native status code, plus the
// upstream diagnostic, instead of a blanket 502.
func TestCreateImageEditNativeStatusErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statusCode int
		errorType  core.ErrorType
		status     int
	}{
		{name: "rate limit", statusCode: 1002, errorType: core.ErrorTypeRateLimit, status: http.StatusTooManyRequests},
		{name: "token limit", statusCode: 1039, errorType: core.ErrorTypeRateLimit, status: http.StatusTooManyRequests},
		{name: "not authorized", statusCode: 1004, errorType: core.ErrorTypeAuthentication, status: http.StatusUnauthorized},
		{name: "invalid api key", statusCode: 2049, errorType: core.ErrorTypeAuthentication, status: http.StatusUnauthorized},
		{name: "insufficient balance", statusCode: 1008, errorType: core.ErrorTypeProvider, status: http.StatusPaymentRequired},
		{name: "sensitive input", statusCode: 1026, errorType: core.ErrorTypeInvalidRequest, status: http.StatusBadRequest},
		{name: "invisible characters", statusCode: 1042, errorType: core.ErrorTypeInvalidRequest, status: http.StatusBadRequest},
		{name: "invalid params", statusCode: 2013, errorType: core.ErrorTypeInvalidRequest, status: http.StatusBadRequest},
		{name: "unknown", statusCode: 9999, errorType: core.ErrorTypeProvider, status: http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"base_resp":{"status_code":%d,"status_msg":%q}}`, tc.statusCode, tc.name)
			server, _ := providertest.JSONServer(t, http.StatusOK, body)
			provider := newTestProvider("minimax-key", server.URL, server.Client(), llmclient.Hooks{})

			_, err := provider.CreateImageEdit(context.Background(), portraitRequest())

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tc.errorType, gatewayErr.Type)
			assert.Equal(t, tc.status, gatewayErr.StatusCode)
			assert.Equal(t, "minimax", gatewayErr.Provider)
			assert.Contains(t, gatewayErr.Message, strconv.Itoa(tc.statusCode))
			assert.Contains(t, gatewayErr.Message, tc.name)
			assert.JSONEq(t, body, string(gatewayErr.ResponseBody))
		})
	}
}

func TestCreateImageEditErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "no images", body: `{"data":{},"base_resp":{"status_code":0}}`},
		{name: "malformed json", body: `invalid`},
		{name: "missing base_resp", body: `{"data":{"image_urls":["https://example.com/image.png"]}}`},
		{name: "missing status_code", body: `{"data":{"image_urls":["https://example.com/image.png"]},"base_resp":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := providertest.JSONServer(t, http.StatusOK, tc.body)
			provider := newTestProvider("minimax-key", server.URL, server.Client(), llmclient.Hooks{})
			_, err := provider.CreateImageEdit(context.Background(), portraitRequest())
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadGateway, gatewayErr.StatusCode)
		})
	}
}

// TestCreateImageEditRejectsLiveDimensions pins that image-01-live rejects
// pixel dimensions, which MiniMax honours only for image-01.
func TestCreateImageEditRejectsLiveDimensions(t *testing.T) {
	for _, field := range []core.FormField{{Name: "width", Value: "1024"}, {Name: "height", Value: "1024"}, {Name: "size", Value: "1024x1024"}} {
		t.Run(field.Name, func(t *testing.T) {
			provider := newTestProvider("minimax-key", "http://unused.invalid", nil, llmclient.Hooks{})
			req := portraitRequest()
			req.Model = "image-01-live"
			req.Fields = []core.FormField{field}

			_, err := provider.CreateImageEdit(context.Background(), req)

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadRequest, gatewayErr.StatusCode)
			assert.Contains(t, gatewayErr.Message, "image-01")
		})
	}
}

// TestCreateImageEditLiveAllowsAspectRatio keeps aspect_ratio working for the
// live model, which is how it is sized.
func TestCreateImageEditLiveAllowsAspectRatio(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"data":{"image_urls":["https://example.com/portrait.png"]},"base_resp":{"status_code":0}}`)
	provider := newTestProvider("minimax-key", server.URL, server.Client(), llmclient.Hooks{})
	req := portraitRequest()
	req.Model = "image-01-live"
	req.Fields = []core.FormField{{Name: "aspect_ratio", Value: "16:9"}}

	_, err := provider.CreateImageEdit(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "16:9", capture.Last(t).JSON(t)["aspect_ratio"])
}

func TestCreateImageEditValidation(t *testing.T) {
	for _, mutate := range []func(*core.ImageEditRequest){
		func(req *core.ImageEditRequest) { req.Mask = &core.ImageFile{Data: []byte("mask")} },
		func(req *core.ImageEditRequest) { req.Images[0].Data = []byte("not an image") },
		func(req *core.ImageEditRequest) { req.Fields = []core.FormField{{Name: "quality", Value: "high"}} },
		func(req *core.ImageEditRequest) { req.Fields = []core.FormField{{Name: "size", Value: "auto"}} },
	} {
		provider := newTestProvider("minimax-key", "http://unused.invalid", nil, llmclient.Hooks{})
		req := portraitRequest()
		mutate(req)
		_, err := provider.CreateImageEdit(context.Background(), req)
		require.Error(t, err)
	}
}

// TestCreateImageEditRejectsUnsupportedDimensions pins that edits apply the
// same dimension rule as image generation. Both post to /image_generation, so
// values the endpoint cannot satisfy are rejected here instead of upstream.
func TestCreateImageEditRejectsUnsupportedDimensions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field core.FormField
	}{
		{name: "size below minimum", field: core.FormField{Name: "size", Value: "10x10"}},
		{name: "size above maximum", field: core.FormField{Name: "size", Value: "4096x4096"}},
		{name: "size not divisible by eight", field: core.FormField{Name: "size", Value: "1020x1024"}},
		{name: "width below minimum", field: core.FormField{Name: "width", Value: "10"}},
		{name: "height not divisible by eight", field: core.FormField{Name: "height", Value: "1020"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := newTestProvider("minimax-key", "http://unused.invalid", nil, llmclient.Hooks{})
			req := portraitRequest()
			req.Fields = []core.FormField{tc.field}

			_, err := provider.CreateImageEdit(context.Background(), req)

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadRequest, gatewayErr.StatusCode)
			assert.Equal(t, imageDimensionError, gatewayErr.Message)
		})
	}
}

// TestCreateImageEditForwardsSupportedDimensions keeps valid dimensions
// flowing through as width and height.
func TestCreateImageEditForwardsSupportedDimensions(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"data":{"image_urls":["https://example.com/portrait.png"]},"base_resp":{"status_code":0}}`)
	provider := newTestProvider("minimax-key", server.URL, server.Client(), llmclient.Hooks{})
	req := portraitRequest()
	req.Fields = []core.FormField{{Name: "size", Value: "1024x512"}}

	_, err := provider.CreateImageEdit(context.Background(), req)

	require.NoError(t, err)
	body := capture.Last(t).JSON(t)
	assert.Equal(t, float64(1024), body["width"])
	assert.Equal(t, float64(512), body["height"])
}
