package server

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

// nativeFileService owns native file orchestration so HTTP handlers can remain
// thin transport adapters.
type nativeFileService struct {
	provider core.RoutableProvider
}

func (s *nativeFileService) router() (core.NativeFileRoutableProvider, error) {
	nativeRouter, ok := s.provider.(core.NativeFileRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("file routing is not supported by the current provider router", nil)
	}
	return nativeRouter, nil
}

func (s *nativeFileService) providerTypes() ([]string, error) {
	typed, ok := s.provider.(core.NativeFileProviderTypeLister)
	if !ok {
		return nil, core.NewProviderError("", http.StatusInternalServerError, "file provider inventory is unavailable", nil)
	}
	return typed.NativeFileProviderTypes(), nil
}

func (s *nativeFileService) fileByID(
	c *echo.Context,
	callFn func(core.NativeFileRoutableProvider, string, string) (any, error),
	respondFn func(*echo.Context, any) error,
) error {
	nativeRouter, err := s.router()
	if err != nil {
		return handleError(c, err)
	}

	fileReq, err := fileRouteInfoFromSemantics(c)
	if err != nil {
		return handleError(c, err)
	}

	id := strings.TrimSpace(fileReq.FileID)
	if id == "" {
		return handleError(c, core.NewInvalidRequestError("file id is required", nil))
	}

	if providerType := fileReq.Provider; providerType != "" {
		auditlog.EnrichEntry(c, "file", providerType)
		result, err := callFn(nativeRouter, providerType, id)
		if err != nil {
			return handleError(c, err)
		}
		return respondFn(c, result)
	}

	providers, err := s.providerTypes()
	if err != nil {
		return handleError(c, err)
	}
	auditlog.EnrichEntry(c, "file", "")

	var firstErr error
	for _, candidate := range providers {
		result, err := callFn(nativeRouter, candidate, id)
		if err == nil {
			return respondFn(c, result)
		}
		if isNotFoundGatewayError(err) || isUnsupportedNativeFilesError(err) {
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return handleError(c, firstErr)
	}
	return handleError(c, core.NewNotFoundError("file not found: "+id))
}

func (s *nativeFileService) CreateFile(c *echo.Context) error {
	nativeRouter, err := s.router()
	if err != nil {
		return handleError(c, err)
	}

	fileReq, err := fileRouteInfoFromSemantics(c)
	if err != nil {
		return handleError(c, err)
	}

	providers, err := s.providerTypes()
	if err != nil {
		return handleError(c, err)
	}

	providerType := fileReq.Provider
	if providerType == "" {
		if len(providers) == 1 {
			providerType = providers[0]
		} else if len(providers) == 0 {
			return handleError(c, core.NewInvalidRequestError("no providers are available for file uploads", nil))
		} else {
			return handleError(c, core.NewInvalidRequestError("provider is required when multiple providers are configured; pass ?provider=<type>", nil))
		}
	}
	auditlog.EnrichEntry(c, "file", providerType)

	purpose := strings.TrimSpace(fileReq.Purpose)
	if purpose == "" {
		return handleError(c, core.NewInvalidRequestError("purpose is required", nil))
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("file is required", err))
	}
	file, err := fileHeader.Open()
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("failed to open uploaded file", err))
	}
	defer func() {
		_ = file.Close()
	}()

	ctx, _ := requestContextWithRequestID(c.Request())
	filename := strings.TrimSpace(fileReq.Filename)
	if filename == "" {
		filename = fileHeader.Filename
	}
	resp, err := nativeRouter.CreateFile(ctx, providerType, &core.FileCreateRequest{
		Purpose:       purpose,
		Filename:      filename,
		ContentReader: file,
	})
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (s *nativeFileService) ListFiles(c *echo.Context) error {
	nativeRouter, err := s.router()
	if err != nil {
		return handleError(c, err)
	}

	fileReq, err := fileRouteInfoFromSemantics(c)
	if err != nil {
		return handleError(c, err)
	}
	limit := 20
	if fileReq.HasLimit {
		limit = fileReq.Limit
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	purpose := fileReq.Purpose
	after := fileReq.After
	providerType := fileReq.Provider

	if providerType != "" {
		auditlog.EnrichEntry(c, "file", providerType)
		resp, err := nativeRouter.ListFiles(c.Request().Context(), providerType, purpose, limit, after)
		if err != nil {
			return handleError(c, err)
		}
		if resp == nil {
			resp = &core.FileListResponse{Object: "list"}
		}
		if resp.Object == "" {
			resp.Object = "list"
		}
		return c.JSON(http.StatusOK, resp)
	}

	providers, err := s.providerTypes()
	if err != nil {
		return handleError(c, err)
	}
	auditlog.EnrichEntry(c, "file", "")

	aggregated := make([]core.FileObject, 0)
	anySuccess := false
	var firstErr error
	for _, candidate := range providers {
		resp, err := nativeRouter.ListFiles(c.Request().Context(), candidate, purpose, limit+1, "")
		if err != nil {
			if isUnsupportedNativeFilesError(err) || isNotFoundGatewayError(err) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		anySuccess = true
		if resp == nil {
			continue
		}
		aggregated = append(aggregated, resp.Data...)
	}
	if !anySuccess && firstErr != nil {
		return handleError(c, firstErr)
	}

	sortFilesDesc(aggregated)
	aggregated, err = applyAfterCursor(aggregated, after)
	if err != nil {
		return handleError(c, err)
	}
	hasMore := len(aggregated) > limit
	if hasMore {
		aggregated = aggregated[:limit]
	}

	return c.JSON(http.StatusOK, core.FileListResponse{
		Object:  "list",
		Data:    aggregated,
		HasMore: hasMore,
	})
}

func (s *nativeFileService) GetFile(c *echo.Context) error {
	return s.fileByID(c,
		func(r core.NativeFileRoutableProvider, provider, id string) (any, error) {
			return r.GetFile(c.Request().Context(), provider, id)
		},
		func(c *echo.Context, result any) error {
			return c.JSON(http.StatusOK, result)
		},
	)
}

func (s *nativeFileService) DeleteFile(c *echo.Context) error {
	return s.fileByID(c,
		func(r core.NativeFileRoutableProvider, provider, id string) (any, error) {
			return r.DeleteFile(c.Request().Context(), provider, id)
		},
		func(c *echo.Context, result any) error {
			return c.JSON(http.StatusOK, result)
		},
	)
}

func (s *nativeFileService) GetFileContent(c *echo.Context) error {
	return s.fileByID(c,
		func(r core.NativeFileRoutableProvider, provider, id string) (any, error) {
			return r.GetFileContent(c.Request().Context(), provider, id)
		},
		func(c *echo.Context, result any) error {
			resp, ok := result.(*core.FileContentResponse)
			if !ok || resp == nil {
				return handleError(c, core.NewProviderError("", http.StatusBadGateway, "provider returned empty file content response", nil))
			}
			contentType := strings.TrimSpace(resp.ContentType)
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			return c.Blob(http.StatusOK, contentType, resp.Data)
		},
	)
}

func isNotFoundGatewayError(err error) bool {
	var gatewayErr *core.GatewayError
	return errors.As(err, &gatewayErr) && gatewayErr.HTTPStatusCode() == http.StatusNotFound
}

func isUnsupportedNativeFilesError(err error) bool {
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		return false
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		return false
	}
	return strings.Contains(strings.ToLower(gatewayErr.Message), "does not support native file operations")
}

func sortFilesDesc(items []core.FileObject) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt == items[j].CreatedAt {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt > items[j].CreatedAt
	})
}

func applyAfterCursor(items []core.FileObject, after string) ([]core.FileObject, error) {
	after = strings.TrimSpace(after)
	if after == "" {
		return items, nil
	}
	for i := range items {
		if items[i].ID == after {
			if i+1 >= len(items) {
				return []core.FileObject{}, nil
			}
			return items[i+1:], nil
		}
	}
	return nil, core.NewNotFoundError("after cursor file not found: " + after)
}
