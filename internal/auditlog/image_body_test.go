package auditlog

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

func TestBuildImageUploadBody(t *testing.T) {
	images := []core.ImageFile{{Filename: "cat.png", ContentType: "image/png; charset=binary", Data: []byte("cat")}}
	mask := &core.ImageFile{Filename: "mask.png", ContentType: "image/png", Data: []byte("mask")}
	meta := map[string]any{"model": "gpt-image-1", "prompt": "add a hat"}

	t.Run("stores base64 when enabled", func(t *testing.T) {
		body := BuildImageUploadBody(images, mask, true, meta, nil)
		if !body.Images || len(body.Items) != 2 || body.Meta["prompt"] != "add a hat" {
			t.Fatalf("body = %+v", body)
		}
		src, msk := body.Items[0], body.Items[1]
		if src.Role != "input" || src.Filename != "cat.png" || src.ContentType != "image/png" || src.Bytes != 3 {
			t.Errorf("input item = %+v", src)
		}
		if !src.Stored || src.Encoding != "base64" {
			t.Errorf("input item should be stored: %+v", src)
		}
		if decoded, err := base64.StdEncoding.DecodeString(src.Data); err != nil || string(decoded) != "cat" {
			t.Errorf("base64 did not round-trip: %q %v", decoded, err)
		}
		if msk.Role != "mask" || !msk.Stored || msk.Bytes != 4 {
			t.Errorf("mask item = %+v", msk)
		}
	})

	t.Run("keeps metadata only when disabled", func(t *testing.T) {
		body := BuildImageUploadBody(images, mask, false, meta, nil)
		for _, item := range body.Items {
			if item.Stored || item.Data != "" || item.TooLarge {
				t.Errorf("item should be a placeholder: %+v", item)
			}
			if item.Bytes == 0 || item.Filename == "" {
				t.Errorf("placeholder must keep size and filename: %+v", item)
			}
		}
		if body.Meta["model"] != "gpt-image-1" {
			t.Errorf("meta should be kept on placeholders: %+v", body.Meta)
		}
	})

	t.Run("no mask", func(t *testing.T) {
		body := BuildImageUploadBody(images, nil, true, nil, nil)
		if len(body.Items) != 1 {
			t.Fatalf("items = %+v, want only the source image", body.Items)
		}
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
		if !body.Images || len(body.Items) != 2 {
			t.Fatalf("body = %+v", body)
		}
		b64 := body.Items[0]
		if b64.Role != "output" || b64.ContentType != "image/jpeg" || b64.Bytes != len(png) || b64.RevisedPrompt != "a fluffy cat" {
			t.Errorf("base64 item = %+v", b64)
		}
		if !b64.Stored || b64.Encoding != "base64" || b64.Data != resp.Data[0].B64JSON {
			t.Errorf("base64 item should embed the payload verbatim: %+v", b64)
		}
		hosted := body.Items[1]
		if hosted.URL != "https://img/1.png" || hosted.Stored || hosted.Bytes != 0 {
			t.Errorf("url item = %+v", hosted)
		}
		if body.Meta["created"] != int64(1713833628) || body.Meta["size"] != "1024x1024" || body.Meta["quality"] != "high" || body.Meta["provider"] != "openai" {
			t.Errorf("meta = %+v", body.Meta)
		}
		if usage, _ := body.Meta["usage"].(map[string]any); usage == nil || usage["total_tokens"] != 282 {
			t.Errorf("usage = %+v", body.Meta["usage"])
		}
		if _, present := body.Meta["background"]; present {
			t.Errorf("empty envelope fields must be omitted: %+v", body.Meta)
		}
	})

	t.Run("placeholder keeps envelope and urls when disabled", func(t *testing.T) {
		body := BuildImageResponseBody(resp, false, nil)
		if body.Items[0].Stored || body.Items[0].Data != "" || body.Items[0].Bytes != len(png) || body.Items[0].ContentType != "image/jpeg" {
			t.Errorf("base64 item should be a sized placeholder: %+v", body.Items[0])
		}
		if body.Items[1].URL != "https://img/1.png" {
			t.Errorf("url must be kept without image storage: %+v", body.Items[1])
		}
		if body.Meta["size"] != "1024x1024" {
			t.Errorf("meta = %+v", body.Meta)
		}
	})

	t.Run("nil response", func(t *testing.T) {
		body := BuildImageResponseBody(nil, true, nil)
		if !body.Images || len(body.Items) != 0 || body.Meta != nil {
			t.Errorf("body = %+v", body)
		}
	})
}

func TestBuildImageResponseBody_BudgetAcrossImages(t *testing.T) {
	big := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xab}, imageBodyMaxBytes/2+1))
	resp := &core.ImageGenerationResponse{Data: []core.ImageData{{B64JSON: big}, {B64JSON: big}, {B64JSON: "aGk="}}}

	body := BuildImageResponseBody(resp, true, nil)

	if !body.Items[0].Stored {
		t.Errorf("first image fits the budget and should be stored: %+v", body.Items[0].Bytes)
	}
	if body.Items[1].Stored || !body.Items[1].TooLarge || body.Items[1].Bytes == 0 {
		t.Errorf("second image exceeds the remaining budget: %+v", body.Items[1].Bytes)
	}
	if !body.Items[2].Stored {
		t.Errorf("small third image still fits: %+v", body.Items[2])
	}
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
	if !reqBody.Items[0].Stored {
		t.Fatalf("upload within budget should be stored: %+v", reqBody.Items[0].Bytes)
	}

	small := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x02}, 60))
	tooBig := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x03}, 600_000))
	respBody := BuildImageResponseBody(&core.ImageGenerationResponse{
		Data: []core.ImageData{{B64JSON: tooBig}, {B64JSON: small}},
	}, true, budget)

	if respBody.Items[0].Stored || !respBody.Items[0].TooLarge {
		t.Errorf("output exceeding the shared remainder must become a placeholder: %+v", respBody.Items[0].Bytes)
	}
	if !respBody.Items[1].Stored {
		t.Errorf("output within the shared remainder should be stored: %+v", respBody.Items[1].Bytes)
	}

	total := 0
	for _, item := range append(reqBody.Items, respBody.Items...) {
		if item.Stored {
			total += len(item.Data)
		}
	}
	if total > imageBodyMaxBytes {
		t.Errorf("stored %d encoded bytes in one entry, budget is %d", total, imageBodyMaxBytes)
	}
}

func TestBase64DecodedLen(t *testing.T) {
	for _, raw := range []string{"", "a", "ab", "abc", "abcd", "hello world!"} {
		if got := base64DecodedLen(base64.StdEncoding.EncodeToString([]byte(raw))); got != len(raw) {
			t.Errorf("base64DecodedLen(%q) = %d, want %d", raw, got, len(raw))
		}
	}
	// Malformed base64 must never yield a negative length: a negative size
	// handed to the budget would increase it instead of reserving from it.
	for _, malformed := range []string{"=", "==", "==="} {
		if got := base64DecodedLen(malformed); got < 0 {
			t.Errorf("base64DecodedLen(%q) = %d, want >= 0", malformed, got)
		}
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

	if body.Items[0].Stored || body.Items[0].Bytes != 0 {
		t.Errorf("malformed payload must not be stored or sized: %+v", body.Items[0])
	}
	if !body.Items[1].Stored {
		t.Errorf("full-budget image should still fit — the malformed entry must not shrink the budget: bytes=%d remaining=%d", body.Items[1].Bytes, budget.remaining)
	}
	if budget.remaining != 0 {
		t.Errorf("remaining = %d, want exactly 0 after a full-budget store", budget.remaining)
	}
}

func TestImageOutputContentType(t *testing.T) {
	for format, want := range map[string]string{"": "image/png", "png": "image/png", "JPEG": "image/jpeg", "jpg": "image/jpeg", "webp": "image/webp"} {
		if got := imageOutputContentType(format); got != want {
			t.Errorf("imageOutputContentType(%q) = %q, want %q", format, got, want)
		}
	}
}

// TestMiddleware_HandlerCapturedResponseBodyIsKept verifies that a JSON body
// stored by the handler via EnrichEntryWithResponseBody survives the
// middleware's generic capture and its truncation flag.
func TestMiddleware_HandlerCapturedResponseBodyIsKept(t *testing.T) {
	e := echo.New()
	logger := &capturingLogger{cfg: Config{Enabled: true, LogBodies: true}}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-1","prompt":"a cat"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	oversized := `{"created":1,"data":[{"b64_json":"` + strings.Repeat("A", int(MaxBodyCapture)+16) + `"}]}`
	handler := Middleware(logger)(func(c *echo.Context) error {
		EnrichEntryWithResponseBody(c, ImageBodyLog{Images: true, Items: []ImageItemLog{{Role: "output", Bytes: 12}}})
		return c.JSONBlob(http.StatusOK, []byte(oversized))
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if len(logger.entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(logger.entries))
	}
	entry := logger.entries[0]
	if entry.Data == nil {
		t.Fatal("expected log data")
	}
	body, ok := entry.Data.ResponseBody.(ImageBodyLog)
	if !ok || !body.Images || len(body.Items) != 1 {
		t.Fatalf("response body = %T %+v, want the handler-captured image body", entry.Data.ResponseBody, entry.Data.ResponseBody)
	}
	if entry.Data.ResponseBodyTooBigToHandle {
		t.Error("middleware must not flag a handler-captured body as truncated")
	}
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
			if cfg.LogImageInputs != tt.wantIn || cfg.LogImageOutputs != tt.wantOut {
				t.Fatalf("inputs/outputs = %v/%v, want %v/%v", cfg.LogImageInputs, cfg.LogImageOutputs, tt.wantIn, tt.wantOut)
			}
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
	if total > imageMetaMaxBytes {
		t.Errorf("meta keeps %d string bytes, cap is %d", total, imageMetaMaxBytes)
	}
	if body.Meta["meta_truncated"] != true {
		t.Errorf("truncation must be flagged: %v", body.Meta["meta_truncated"])
	}
	if body.Meta["model"] != "gpt-image-1" {
		t.Errorf("small values must survive intact: %v", body.Meta["model"])
	}
	if kept, _ := body.Meta["prompt"].(string); len(kept) == 0 || len(kept) >= len(hugePrompt) {
		t.Errorf("prompt should be truncated, kept %d of %d bytes", len(kept), len(hugePrompt))
	}
	if !body.Items[0].Stored {
		t.Errorf("image storage must be unaffected by meta capping: %+v", body.Items[0])
	}
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
	if len(kept) > imageRevisedPromptMaxBytes {
		t.Errorf("revised_prompt keeps %d bytes, cap is %d", len(kept), imageRevisedPromptMaxBytes)
	}
	if !utf8.ValidString(kept) {
		t.Error("truncation split a rune")
	}
}
