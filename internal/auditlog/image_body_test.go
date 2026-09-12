package auditlog

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

func TestBuildImageUploadBody(t *testing.T) {
	images := []core.ImageFile{{Filename: "cat.png", ContentType: "image/png; charset=binary", Data: []byte("cat")}}
	mask := &core.ImageFile{Filename: "mask.png", ContentType: "image/png", Data: []byte("mask")}
	meta := map[string]any{"model": "gpt-image-1", "prompt": "add a hat"}

	t.Run("stores base64 when enabled", func(t *testing.T) {
		body := BuildImageUploadBody(images, mask, true, meta, nil)
		require.True(t, body.Images)
		require.Len(t, body.Items, 2)
		require.Equal(t, "add a hat", body.Meta["prompt"], "body = %+v", body)

		src, msk := body.Items[0], body.Items[1]
		assert.Equal(t, "input", src.Role)
		assert.Equal(t, "cat.png", src.Filename)
		assert.Equal(t, "image/png", src.ContentType)
		assert.Equal(t, 3, src.Bytes, "input item = %+v", src)
		assert.True(t, src.Stored)
		assert.Equal(t, "base64", src.Encoding, "input item should be stored: %+v", src)
		decoded, err := base64.StdEncoding.DecodeString(src.Data)
		assert.NoError(t, err)
		assert.Equal(t, "cat", string(decoded), "base64 did not round-trip: %q %v", decoded, err)
		assert.Equal(t, "mask", msk.Role)
		assert.True(t, msk.Stored)
		assert.Equal(t, 4, msk.Bytes, "mask item = %+v", msk)
	})

	t.Run("keeps metadata only when disabled", func(t *testing.T) {
		body := BuildImageUploadBody(images, mask, false, meta, nil)
		for _, item := range body.Items {
			assert.False(t, item.Stored)
			assert.Empty(t, item.Data)
			assert.False(t, item.TooLarge, "item should be a placeholder: %+v", item)
			assert.NotEqual(t, 0, item.Bytes)
			assert.NotEmpty(t, item.Filename, "placeholder must keep size and filename: %+v", item)
		}
		assert.Equal(t, "gpt-image-1", body.Meta["model"], "meta should be kept on placeholders: %+v", body.Meta)
	})

	t.Run("no mask", func(t *testing.T) {
		body := BuildImageUploadBody(images, nil, true, nil, nil)
		require.Len(t, body.Items, 1)
	})
}

func TestBuildImageResponseBody(t *testing.T) {
	png := []byte("generated-png-bytes")
	resp := &core.ImageGenerationResponse{
		Created:      1713833628,
		OutputFormat: "jpeg",
		Quality:      "high",
		Size:         "1024x1024",
		Provider:     "openai",
		Usage:        &core.ImageUsage{InputTokens: 10, OutputTokens: 272, TotalTokens: 282},
		Data: []core.ImageData{
			{B64JSON: base64.StdEncoding.EncodeToString(png), RevisedPrompt: "a fluffy cat"},
			{URL: "https://img/1.png"},
		},
	}

	t.Run("stores base64 outputs and keeps urls", func(t *testing.T) {
		body := BuildImageResponseBody(resp, true, nil)
		require.True(t, body.Images)
		require.Len(t, body.Items, 2, "body = %+v", body)

		b64 := body.Items[0]
		assert.Equal(t, "output", b64.Role)
		assert.Equal(t, "image/jpeg", b64.ContentType)
		assert.Equal(t, len(png), b64.Bytes)
		assert.Equal(t, "a fluffy cat", b64.RevisedPrompt, "base64 item = %+v", b64)
		assert.True(t, b64.Stored)
		assert.Equal(t, "base64", b64.Encoding)
		assert.Equal(t, resp.Data[0].B64JSON, b64.Data, "base64 item should embed the payload verbatim: %+v", b64)

		hosted := body.Items[1]
		assert.Equal(t, "https://img/1.png", hosted.URL)
		assert.False(t, hosted.Stored)
		assert.Equal(t, 0, hosted.Bytes, "url item = %+v", hosted)
		assert.Equal(t, int64(1713833628), body.Meta["created"])
		assert.Equal(t, "1024x1024", body.Meta["size"])
		assert.Equal(t, "high", body.Meta["quality"])
		assert.Equal(t, "openai", body.Meta["provider"], "meta = %+v", body.Meta)
		usage, _ := body.Meta["usage"].(map[string]any)
		require.NotNil(t, usage)
		assert.Equal(t, 282, usage["total_tokens"])
		_, present := body.Meta["background"]
		assert.False(t, present, "empty envelope fields must be omitted: %+v", body.Meta)
	})

	t.Run("placeholder keeps envelope and urls when disabled", func(t *testing.T) {
		body := BuildImageResponseBody(resp, false, nil)
		assert.False(t, body.Items[0].Stored)
		assert.Empty(t, body.Items[0].Data)
		assert.Equal(t, len(png), body.Items[0].Bytes)
		assert.Equal(t, "image/jpeg", body.Items[0].ContentType, "base64 item should be a sized placeholder: %+v", body.Items[0])
		assert.Equal(t, "https://img/1.png", body.Items[1].URL, "url must be kept without image storage: %+v", body.Items[1])
		assert.Equal(t, "1024x1024", body.Meta["size"], "meta = %+v", body.Meta)
	})

	t.Run("nil response", func(t *testing.T) {
		body := BuildImageResponseBody(nil, true, nil)
		assert.True(t, body.Images)
		assert.Empty(t, body.Items)
		assert.Nil(t, body.Meta, "body = %+v", body)
	})
}

func TestBuildImageResponseBody_BudgetAcrossImages(t *testing.T) {
	big := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xab}, imageBodyMaxBytes/2+1))
	resp := &core.ImageGenerationResponse{Data: []core.ImageData{{B64JSON: big}, {B64JSON: big}, {B64JSON: "aGk="}}}

	body := BuildImageResponseBody(resp, true, nil)

	assert.True(t, body.Items[0].Stored, "first image fits the budget and should be stored: %+v", body.Items[0].Bytes)
	assert.False(t, body.Items[1].Stored)
	assert.True(t, body.Items[1].TooLarge)
	assert.NotEqual(t, 0, body.Items[1].Bytes)
	assert.True(t, body.Items[2].Stored, "small third image still fits: %+v", body.Items[2])
}

// TestImageBodyBudget_SharedAcrossRequestAndResponse verifies the budget is
// entry-wide: an edit whose uploads consume most of the allowance leaves only
// the remainder for the response, so one entry can never hold more than
// imageBodyMaxBytes of raw image data across both bodies.
func TestImageBodyBudget_SharedAcrossRequestAndResponse(t *testing.T) {
	budget := NewImageBodyBudget()
	// 5.9 MB raw encodes to ~7.87 MB of base64 — within the 8 MiB encoded
	// budget, leaving ~0.5 MB for the response side.
	bigUpload := core.ImageFile{Filename: "big.png", Data: bytes.Repeat([]byte{0x01}, 5_900_000)}

	reqBody := BuildImageUploadBody([]core.ImageFile{bigUpload}, nil, true, nil, budget)
	require.True(t, reqBody.Items[0].Stored, "upload within budget should be stored: %+v", reqBody.Items[0].Bytes)

	small := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x02}, 60))
	tooBig := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x03}, 600_000))
	respBody := BuildImageResponseBody(&core.ImageGenerationResponse{
		Data: []core.ImageData{{B64JSON: tooBig}, {B64JSON: small}},
	}, true, budget)

	assert.False(t, respBody.Items[0].Stored)
	assert.True(t, respBody.Items[0].TooLarge, "output exceeding the shared remainder must become a placeholder: %+v", respBody.Items[0].Bytes)
	assert.True(t, respBody.Items[1].Stored, "output within the shared remainder should be stored: %+v", respBody.Items[1].Bytes)

	total := 0
	for _, item := range append(reqBody.Items, respBody.Items...) {
		if item.Stored {
			total += len(item.Data)
		}
	}
	assert.LessOrEqual(t, total, imageBodyMaxBytes)
}

func TestBase64DecodedLen(t *testing.T) {
	for _, raw := range []string{"", "a", "ab", "abc", "abcd", "hello world!"} {
		assert.Equal(t, len(raw), base64DecodedLen(base64.StdEncoding.EncodeToString([]byte(raw))), "base64DecodedLen(%q)", raw)
	}
	// Malformed base64 must never yield a negative length: a negative size
	// handed to the budget would increase it instead of reserving from it.
	for _, malformed := range []string{"=", "==", "==="} {
		assert.GreaterOrEqual(t, base64DecodedLen(malformed), 0, "base64DecodedLen(%q)", malformed)
	}
}

// TestBuildImageResponseBody_MalformedBase64DoesNotGrowBudget feeds a
// padding-only b64_json through a shared budget and verifies the budget is
// left intact for later images instead of being inflated.
func TestBuildImageResponseBody_MalformedBase64DoesNotGrowBudget(t *testing.T) {
	budget := NewImageBodyBudget()
	// 6 MiB raw encodes to exactly 8 MiB of base64 — the whole encoded budget.
	nearLimit := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, imageBodyMaxBytes/4*3))

	body := BuildImageResponseBody(&core.ImageGenerationResponse{
		Data: []core.ImageData{{B64JSON: "=="}, {B64JSON: nearLimit}},
	}, true, budget)

	assert.False(t, body.Items[0].Stored)
	assert.Equal(t, 0, body.Items[0].Bytes, "malformed payload must not be stored or sized: %+v", body.Items[0])
	assert.True(t, body.Items[1].Stored, "full-budget image should still fit — the malformed entry must not shrink the budget: bytes=%d remaining=%d", body.Items[1].Bytes, budget.remaining)
	assert.Equal(t, 0, budget.remaining)
}

func TestImageOutputContentType(t *testing.T) {
	for format, want := range map[string]string{"": "image/png", "png": "image/png", "JPEG": "image/jpeg", "jpg": "image/jpeg", "webp": "image/webp"} {
		assert.Equal(t, want, imageOutputContentType(format))
	}
}

// TestMiddleware_HandlerCapturedResponseBodyIsKept verifies that a JSON body
// stored by the handler via EnrichEntryWithResponseBody survives the
// middleware's generic capture and its truncation flag.
func TestMiddleware_HandlerCapturedResponseBodyIsKept(t *testing.T) {
	logger := &capturingLogger{cfg: Config{Enabled: true, LogBodies: true}}

	c, _ := echotest.Post(t, "/v1/images/generations", `{"model":"gpt-image-1","prompt":"a cat"}`)

	oversized := `{"created":1,"data":[{"b64_json":"` + strings.Repeat("A", int(MaxBodyCapture)+16) + `"}]}`
	handler := Middleware(logger)(func(c *echo.Context) error {
		EnrichEntryWithResponseBody(c, ImageBodyLog{Images: true, Items: []ImageItemLog{{Role: "output", Bytes: 12}}})
		return c.JSONBlob(http.StatusOK, []byte(oversized))
	})
	err := handler(c)
	require.NoError(t, err)
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.NotNil(t, entry.Data)

	body, ok := entry.Data.ResponseBody.(ImageBodyLog)
	require.True(t, ok)
	require.True(t, body.Images)
	require.Len(t, body.Items, 1)
	assert.False(t, entry.Data.ResponseBodyTooBigToHandle)
}

func TestBuildLoggerConfig_ImageBodies(t *testing.T) {
	tests := []struct {
		name            string
		enabled         bool
		scope           config.ImageBodyScope
		wantIn, wantOut bool
	}{
		{"disabled", false, config.ImageBodyScopeAll, false, false},
		{"all", true, config.ImageBodyScopeAll, true, true},
		{"unset scope defaults to all", true, "", true, true},
		{"input", true, config.ImageBodyScopeInput, true, false},
		{"output", true, config.ImageBodyScopeOutput, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := buildLoggerConfig(config.LogConfig{LogBodies: true, LogImageBodies: tt.enabled, LogImageBodiesScope: tt.scope})
			require.Equal(t, tt.wantIn, cfg.LogImageInputs)
			require.Equal(t, tt.wantOut, cfg.LogImageOutputs)
		})
	}
}

// TestBuildImageUploadBody_CapsClientMeta verifies a multi-megabyte prompt
// cannot ride the meta into the audit store unbounded: total meta string
// bytes are capped, the cut is flagged, and the images are unaffected.
func TestBuildImageUploadBody_CapsClientMeta(t *testing.T) {
	hugePrompt := strings.Repeat("p", imageMetaMaxBytes+4096)
	meta := map[string]any{"model": "gpt-image-1", "prompt": hugePrompt, "size": "1024x1024"}
	images := []core.ImageFile{{Filename: "cat.png", Data: []byte("cat")}}

	body := BuildImageUploadBody(images, nil, true, meta, nil)

	total := 0
	for _, value := range body.Meta {
		if s, ok := value.(string); ok {
			total += len(s)
		}
	}
	assert.LessOrEqual(t, total, imageMetaMaxBytes)
	assert.Equal(t, true, body.Meta["meta_truncated"])
	assert.Equal(t, "gpt-image-1", body.Meta["model"])
	kept, _ := body.Meta["prompt"].(string)
	assert.NotEmpty(t, kept)
	assert.Less(t, len(kept), len(hugePrompt))
	assert.True(t, body.Items[0].Stored, "image storage must be unaffected by meta capping: %+v", body.Items[0])
}

// TestBuildImageResponseBody_CapsRevisedPrompt bounds the provider-returned
// revised_prompt so a misbehaving upstream cannot bloat the entry, and
// verifies truncation never splits a multi-byte rune.
func TestBuildImageResponseBody_CapsRevisedPrompt(t *testing.T) {
	long := strings.Repeat("é", imageRevisedPromptMaxBytes) // 2 bytes per rune
	body := BuildImageResponseBody(&core.ImageGenerationResponse{
		Data: []core.ImageData{{URL: "https://img/1.png", RevisedPrompt: long}},
	}, false, nil)

	kept := body.Items[0].RevisedPrompt
	assert.LessOrEqual(t, len(kept), imageRevisedPromptMaxBytes)
	assert.True(t, utf8.ValidString(kept))
}
