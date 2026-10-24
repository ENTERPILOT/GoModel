package auditlog

import (
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
	"github.com/enterpilot/gomodel/internal/mediastore"
)

func TestBuildImageUploadBody(t *testing.T) {
	images := []core.ImageFile{{Filename: "cat.png", ContentType: "image/png; charset=binary", Data: []byte("cat")}}
	mask := &core.ImageFile{Filename: "mask.png", ContentType: "image/png", Data: []byte("mask")}
	meta := map[string]any{"model": "gpt-image-1", "prompt": "add a hat"}

	t.Run("stores media when captured", func(t *testing.T) {
		capture, store := newTestMediaCapture(t, 30)
		body := BuildImageUploadBody(capture, images, mask, meta)
		require.True(t, body.Images)
		require.Len(t, body.Items, 2)
		require.Equal(t, "add a hat", body.Meta["prompt"], "body = %+v", body)

		src, msk := body.Items[0], body.Items[1]
		assert.Equal(t, "input", src.Role)
		assert.Equal(t, "cat.png", src.Filename)
		assert.Equal(t, "image/png", src.ContentType)
		assert.Equal(t, int64(3), src.Bytes, "input item = %+v", src)
		require.True(t, src.Stored)
		object, data := readMedia(t, store, src.MediaID)
		assert.Equal(t, "cat", string(data))
		assert.Equal(t, mediastore.KindImage, object.Kind)
		assert.Equal(t, "image/png", object.ContentType)
		assert.Equal(t, "mask", msk.Role)
		require.True(t, msk.Stored)
		assert.Equal(t, int64(4), msk.Bytes, "mask item = %+v", msk)
		_, data = readMedia(t, store, msk.MediaID)
		assert.Equal(t, "mask", string(data))
	})

	t.Run("keeps metadata only without capture", func(t *testing.T) {
		body := BuildImageUploadBody(nil, images, mask, meta)
		for _, item := range body.Items {
			assert.False(t, item.Stored)
			assert.Empty(t, item.MediaID, "item should be a placeholder: %+v", item)
			assert.NotEqual(t, int64(0), item.Bytes)
			assert.NotEmpty(t, item.Filename, "placeholder must keep size and filename: %+v", item)
		}
		assert.Equal(t, "gpt-image-1", body.Meta["model"], "meta should be kept on placeholders: %+v", body.Meta)
	})

	t.Run("no mask", func(t *testing.T) {
		body := BuildImageUploadBody(nil, images, nil, nil)
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

	t.Run("stores base64 outputs decoded and keeps urls", func(t *testing.T) {
		capture, store := newTestMediaCapture(t, 30)
		body := BuildImageResponseBody(capture, resp)
		require.True(t, body.Images)
		require.Len(t, body.Items, 2, "body = %+v", body)

		out := body.Items[0]
		assert.Equal(t, "output", out.Role)
		assert.Equal(t, "image/jpeg", out.ContentType)
		assert.Equal(t, int64(len(png)), out.Bytes)
		assert.Equal(t, "a fluffy cat", out.RevisedPrompt, "output item = %+v", out)
		require.True(t, out.Stored)
		object, data := readMedia(t, store, out.MediaID)
		assert.Equal(t, png, data, "the stored object holds the decoded pixels, not base64")
		assert.Equal(t, "image/jpeg", object.ContentType)

		hosted := body.Items[1]
		assert.Equal(t, "https://img/1.png", hosted.URL)
		assert.False(t, hosted.Stored)
		assert.Equal(t, int64(0), hosted.Bytes, "url item = %+v", hosted)
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

	t.Run("placeholder keeps envelope and urls without capture", func(t *testing.T) {
		body := BuildImageResponseBody(nil, resp)
		assert.False(t, body.Items[0].Stored)
		assert.Empty(t, body.Items[0].MediaID)
		assert.Equal(t, int64(len(png)), body.Items[0].Bytes)
		assert.Equal(t, "image/jpeg", body.Items[0].ContentType, "base64 item should be a sized placeholder: %+v", body.Items[0])
		assert.Equal(t, "https://img/1.png", body.Items[1].URL, "url must be kept without image storage: %+v", body.Items[1])
		assert.Equal(t, "1024x1024", body.Meta["size"], "meta = %+v", body.Meta)
	})

	t.Run("malformed base64 is a sized placeholder", func(t *testing.T) {
		capture, _ := newTestMediaCapture(t, 30)
		body := BuildImageResponseBody(capture, &core.ImageGenerationResponse{Data: []core.ImageData{{B64JSON: "=="}, {B64JSON: "not base64!"}}})
		require.Len(t, body.Items, 2)
		assert.False(t, body.Items[0].Stored)
		assert.Equal(t, int64(0), body.Items[0].Bytes, "padding-only payload must not be stored or sized: %+v", body.Items[0])
		assert.False(t, body.Items[1].Stored, "a payload that fails to decode is not stored: %+v", body.Items[1])
	})

	t.Run("nil response", func(t *testing.T) {
		body := BuildImageResponseBody(nil, nil)
		assert.True(t, body.Images)
		assert.Empty(t, body.Items)
		assert.Nil(t, body.Meta, "body = %+v", body)
	})
}

func TestBase64DecodedLen(t *testing.T) {
	for _, raw := range []string{"", "a", "ab", "abc", "abcd", "hello world!"} {
		assert.Equal(t, len(raw), base64DecodedLen(base64.StdEncoding.EncodeToString([]byte(raw))), "base64DecodedLen(%q)", raw)
	}
	for _, malformed := range []string{"=", "==", "==="} {
		assert.GreaterOrEqual(t, base64DecodedLen(malformed), 0, "base64DecodedLen(%q)", malformed)
	}
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
	capture, _ := newTestMediaCapture(t, 30)
	hugePrompt := strings.Repeat("p", imageMetaMaxBytes+4096)
	meta := map[string]any{"model": "gpt-image-1", "prompt": hugePrompt, "size": "1024x1024"}
	images := []core.ImageFile{{Filename: "cat.png", Data: []byte("cat")}}

	body := BuildImageUploadBody(capture, images, nil, meta)

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
	body := BuildImageResponseBody(nil, &core.ImageGenerationResponse{
		Data: []core.ImageData{{URL: "https://img/1.png", RevisedPrompt: long}},
	})

	kept := body.Items[0].RevisedPrompt
	assert.LessOrEqual(t, len(kept), imageRevisedPromptMaxBytes)
	assert.True(t, utf8.ValidString(kept))
}
