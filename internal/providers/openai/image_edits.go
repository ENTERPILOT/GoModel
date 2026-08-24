package openai

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// CreateImageEdit implements OpenAI image editing (POST /images/edits). The
// request is re-encoded as multipart/form-data: the typed model and prompt,
// every extra form field the client sent, then the image(s) and optional mask.
// The body is buffered (the images are already in memory) so the client can
// retry transient upstream failures.
func (p *CompatibleProvider) CreateImageEdit(ctx context.Context, req *core.ImageEditRequest) (*core.ImageGenerationResponse, error) {
	if err := core.ValidateImageEditRequest(req); err != nil {
		return nil, err
	}
	body, contentType, err := imageEditMultipart(req)
	if err != nil {
		return nil, err
	}
	var resp core.ImageGenerationResponse
	err = p.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/images/edits",
		Model:    req.Model,
		RawBody:  body,
		Headers:  http.Header{"Content-Type": {contentType}},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Created == 0 {
		resp.Created = time.Now().Unix()
	}
	if resp.Data == nil {
		resp.Data = []core.ImageData{}
	}
	return &resp, nil
}

// imageEditMultipart encodes an image edit request as a multipart/form-data
// body. A single source image is sent as "image" (the shape DALL·E 2 requires);
// several are sent as "image[]" (gpt-image-1). Each file part carries the
// client's declared Content-Type so the upstream can validate the format.
func imageEditMultipart(req *core.ImageEditRequest) ([]byte, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	fields := append([]core.FormField{{Name: "model", Value: req.Model}, {Name: "prompt", Value: req.Prompt}}, req.Fields...)
	for _, field := range fields {
		if err := writer.WriteField(field.Name, field.Value); err != nil {
			return nil, "", core.NewInvalidRequestError("failed to write "+field.Name+" field", err)
		}
	}

	imageField := "image"
	if len(req.Images) > 1 {
		imageField = "image[]"
	}
	for _, img := range req.Images {
		if err := writeImagePart(writer, imageField, img, "image.png"); err != nil {
			return nil, "", err
		}
	}
	if req.Mask != nil {
		if err := writeImagePart(writer, "mask", *req.Mask, "mask.png"); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", core.NewInvalidRequestError("failed to finalize multipart payload", err)
	}
	return buf.Bytes(), writer.FormDataContentType(), nil
}

// writeImagePart appends one file part, defaulting the filename and content
// type when the client omitted them (OpenAI rejects parts without a filename).
// Client-declared metadata is stripped of CR/LF before it reaches the MIME
// headers: multipart.Writer serializes header values verbatim, and a newline
// smuggled through them could inject arbitrary parts or headers upstream.
func writeImagePart(writer *multipart.Writer, field string, img core.ImageFile, defaultName string) error {
	filename := strings.TrimSpace(stripCRLF(img.Filename))
	if filename == "" {
		filename = defaultName
	}
	contentType := strings.TrimSpace(stripCRLF(img.ContentType))
	if contentType == "" {
		contentType = "image/png"
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="`+escapeQuotes(field)+`"; filename="`+escapeQuotes(filename)+`"`)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return core.NewInvalidRequestError("failed to create multipart "+field+" field", err)
	}
	if _, err := part.Write(img.Data); err != nil {
		return core.NewInvalidRequestError("failed to write "+field+" content", err)
	}
	return nil
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

var crlfStripper = strings.NewReplacer("\r", "", "\n", "")

func stripCRLF(s string) string {
	return crlfStripper.Replace(s)
}

func escapeQuotes(s string) string {
	return quoteEscaper.Replace(s)
}
