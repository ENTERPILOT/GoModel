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
				provider := newTestProvider("test-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})
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
	p := newTestProvider("test-key", server.URL, server.Client(), llmclient.Hooks{})
	var req core.ImageGenerationRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"image-01","prompt":"A lighthouse","aspect_ratio":"16:9"}`), &req))
	_, err := p.CreateImage(context.Background(), &req)
	require.NoError(t, err)
	fields := capture.Last(t).JSON(t)
	assert.Equal(t, "16:9", fields["aspect_ratio"])
	assert.NotContains(t, fields, "width")
}

func TestCreateImageErrors(t *testing.T) {
	// nativeMessage is MiniMax's base_resp.status_msg; when set, the gateway
	// error must carry it along with the raw upstream body for auditing.
	for _, tc := range []struct {
		name, body    string
		status, want  int
		nativeMessage string
	}{
		{"rate limit", `{"base_resp":{"status_code":1002,"status_msg":"rate limit triggered"}}`, 200, 429, "rate limit triggered"},
		{"token limit", `{"base_resp":{"status_code":1039,"status_msg":"token limit"}}`, 200, 429, "token limit"},
		{"rate growth limit", `{"base_resp":{"status_code":2045,"status_msg":"rate growth limit"}}`, 200, 429, "rate growth limit"},
		{"usage limit", `{"base_resp":{"status_code":2056,"status_msg":"usage limit exceeded"}}`, 200, 429, "usage limit exceeded"},
		{"authentication", `{"base_resp":{"status_code":1004,"status_msg":"not authorized"}}`, 200, 401, "not authorized"},
		{"invalid key", `{"base_resp":{"status_code":2049,"status_msg":"invalid API Key"}}`, 200, 401, "invalid API Key"},
		{"balance", `{"base_resp":{"status_code":1008,"status_msg":"insufficient balance"}}`, 200, 402, "insufficient balance"},
		{"sensitive", `{"base_resp":{"status_code":1026,"status_msg":"sensitive content"}}`, 200, 400, "sensitive content"},
		{"invisible characters", `{"base_resp":{"status_code":1042,"status_msg":"invisible character ratio limit"}}`, 200, 400, "invisible character ratio limit"},
		{"parameters", `{"base_resp":{"status_code":2013,"status_msg":"invalid params"}}`, 200, 400, "invalid params"},
		{"unknown", `{"base_resp":{"status_code":9999,"status_msg":"unknown error"}}`, 200, 502, "unknown error"},
		{"missing native message", `{"base_resp":{"status_code":2013}}`, 200, 400, ""},
		{"empty", `{"data":{"image_urls":[]},"base_resp":{"status_code":0}}`, 200, 502, ""},
		{"missing status", `{"data":{"image_urls":["https://example.com/image.png"]}}`, 200, 502, ""},
		{"malformed", `{`, 200, 502, ""},
		{"http error", `{"error":{"message":"invalid request"}}`, 400, 400, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := providertest.JSONServer(t, tc.status, tc.body)
			p := newTestProvider("test-key", server.URL, server.Client(), llmclient.Hooks{})
			_, err := p.CreateImage(context.Background(), &core.ImageGenerationRequest{Model: "image-01", Prompt: "A lighthouse"})
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tc.want, gatewayErr.HTTPStatusCode())
			if tc.nativeMessage == "" {
				return
			}
			assert.Equal(t, "minimax", gatewayErr.Provider)
			assert.Contains(t, gatewayErr.Message, tc.nativeMessage)
			assert.JSONEq(t, tc.body, string(gatewayErr.ResponseBody))
		})
	}
}

func TestCreateImageOmitsBlankNativeMessage(t *testing.T) {
	const body = `{"base_resp":{"status_code":2013,"status_msg":"  "}}`
	server, _ := providertest.JSONServer(t, http.StatusOK, body)
	p := newTestProvider("test-key", server.URL, server.Client(), llmclient.Hooks{})

	_, err := p.CreateImage(context.Background(), &core.ImageGenerationRequest{Model: "image-01", Prompt: "A lighthouse"})

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, "minimax image request failed (status 2013)", gatewayErr.Message)
	assert.JSONEq(t, body, string(gatewayErr.ResponseBody))
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
			p := newTestProvider("test-key", server.URL, server.Client(), llmclient.Hooks{})
			var req *core.ImageGenerationRequest
			require.NoError(t, json.Unmarshal([]byte(body), &req))
			_, err := p.CreateImage(context.Background(), req)
			require.Error(t, err)
			assert.Zero(t, capture.Count())
		})
	}
}
