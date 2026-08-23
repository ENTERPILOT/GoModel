package server

import (
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// imageEditFormFields are the multipart fields the gateway consumes itself;
// every other field is forwarded to the provider verbatim.
var imageEditFormFields = map[string]bool{"model": true, "prompt": true, "provider": true}

func (s *imageService) editRouter() (core.ImageEditProvider, error) {
	router, ok := s.provider.(core.ImageEditProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("image edits are not supported by the current provider router", nil)
	}
	return router, nil
}

// CreateImageEdit handles POST /v1/images/edits.
func (s *imageService) CreateImageEdit(c *echo.Context) error {
	router, err := s.editRouter()
	if err != nil {
		return handleError(c, err)
	}

	req, err := imageEditRequestFromForm(c)
	if err != nil {
		return handleError(c, err)
	}
	// The upload is multipart, so the audit middleware has no JSON body to
	// capture; record the edit parameters and the uploads instead. Image
	// bytes are embedded only when logImageInputs is on.
	if s.logBodies {
		auditlog.EnrichEntryWithRequestBody(c, auditlog.BuildImageUploadBody(req.Images, req.Mask, s.logImageInputs, imageEditAuditMeta(req)))
	}
	if err := core.ValidateImageEditRequest(req); err != nil {
		return handleError(c, err)
	}

	ctx, route, err := s.prepare(c, req.Model, req.Provider)
	if err != nil {
		return handleError(c, err)
	}
	// Dispatch on the resolved model: an alias never reaches the provider lookup.
	req.Model, req.Provider = route.selector.Model, route.selector.Provider
	release, err := enforceRateLimit(c, s.rateLimiter, rateLimitRoute{provider: route.providerName, model: route.model})
	if err != nil {
		return handleError(c, err)
	}
	defer release()
	started := time.Now()
	resp, err := router.CreateImageEdit(ctx, req)
	inferenceTime := time.Since(started)
	if err != nil {
		return handleError(c, err)
	}
	if resp == nil {
		return handleError(c, core.NewProviderError("", http.StatusBadGateway, "provider returned empty image response", nil))
	}
	s.logUsage(ctx, route, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromImageEditResponse(resp, route.requestID, route.model, route.providerType, pricing)
	})
	if err := waitForModelSlowdownFactor(ctx, route.slowdown, inferenceTime); err != nil {
		return handleError(c, err)
	}
	return s.respondImages(c, resp)
}

// imageEditRequestFromForm decodes the OpenAI multipart edit request. Source
// images arrive as "image" (one) or "image[]" (several); the optional "mask"
// is a single file. Remaining form values are kept for verbatim forwarding,
// sorted by name for a deterministic upstream body; values sharing a name
// keep their request order (multipart grouping already discards cross-name
// order, and form fields carry no cross-name order semantics).
func imageEditRequestFromForm(c *echo.Context) (*core.ImageEditRequest, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid multipart form", err)
	}
	req := &core.ImageEditRequest{
		Model:    strings.TrimSpace(c.FormValue("model")),
		Prompt:   c.FormValue("prompt"),
		Provider: strings.TrimSpace(c.FormValue("provider")),
	}
	if form == nil {
		return req, nil
	}

	for _, field := range []string{"image", "image[]"} {
		for _, header := range form.File[field] {
			img, err := readImageFile(header)
			if err != nil {
				return nil, err
			}
			req.Images = append(req.Images, img)
		}
	}
	if masks := form.File["mask"]; len(masks) > 0 {
		mask, err := readImageFile(masks[0])
		if err != nil {
			return nil, err
		}
		req.Mask = &mask
	}

	names := make([]string, 0, len(form.Value))
	for name := range form.Value {
		if !imageEditFormFields[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		for _, value := range form.Value[name] {
			req.Fields = append(req.Fields, core.FormField{Name: name, Value: value})
		}
	}
	return req, nil
}

func readImageFile(header *multipart.FileHeader) (core.ImageFile, error) {
	file, err := header.Open()
	if err != nil {
		return core.ImageFile{}, core.NewInvalidRequestError("failed to open uploaded image", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		return core.ImageFile{}, core.NewInvalidRequestError("failed to read uploaded image", err)
	}
	return core.ImageFile{
		Filename:    header.Filename,
		ContentType: header.Header.Get("Content-Type"),
		Data:        data,
	}, nil
}

// imageEditAuditMeta builds the parameter metadata attached to a logged edit
// request: the user-facing fields, never routing hints. The uploads themselves
// are recorded as image items alongside it.
func imageEditAuditMeta(req *core.ImageEditRequest) map[string]any {
	meta := map[string]any{
		"model":  req.Model,
		"prompt": req.Prompt,
	}
	for _, field := range req.Fields {
		if existing, ok := meta[field.Name]; ok {
			meta[field.Name] = appendAuditValue(existing, field.Value)
			continue
		}
		meta[field.Name] = field.Value
	}
	return meta
}

func appendAuditValue(existing any, value string) []string {
	switch v := existing.(type) {
	case []string:
		return append(v, value)
	case string:
		return []string{v, value}
	default:
		return []string{value}
	}
}
