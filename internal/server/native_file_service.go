package server

import (
	"context"
	"errors"
	"net/http"
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
			auditlog.EnrichEntry(c, "file", candidate)
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
	resp, err := s.listMergedFiles(c.Request().Context(), nativeRouter, providers, purpose, limit, after)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
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

type providerFileListState struct {
	provider  string
	after     string
	items     []core.FileObject
	index     int
	hasMore   bool
	exhausted bool
}

func (s *nativeFileService) listMergedFiles(
	ctx context.Context,
	nativeRouter core.NativeFileRoutableProvider,
	providers []string,
	purpose string,
	limit int,
	after string,
) (*core.FileListResponse, error) {
	pageSize := limit + 1
	if pageSize <= 0 {
		pageSize = 1
	}
	after = strings.TrimSpace(after)

	states := make([]*providerFileListState, 0, len(providers))
	anySuccess := false
	var firstErr error
	for _, candidate := range providers {
		state := &providerFileListState{provider: candidate}
		loaded, err := loadProviderFilePage(ctx, nativeRouter, state, purpose, pageSize)
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
		if loaded {
			states = append(states, state)
		}
	}
	if !anySuccess && firstErr != nil {
		return nil, firstErr
	}

	foundAfter := strings.TrimSpace(after) == ""
	collected := make([]core.FileObject, 0, limit+1)
	for len(collected) <= limit {
		next, err := nextMergedFile(ctx, nativeRouter, states, purpose, pageSize)
		if err != nil {
			return nil, err
		}
		if next == nil {
			break
		}
		if !foundAfter {
			if next.ID == after {
				foundAfter = true
			}
			continue
		}
		collected = append(collected, *next)
	}
	if !foundAfter {
		return nil, core.NewNotFoundError("after cursor file not found: " + after)
	}

	resp := &core.FileListResponse{
		Object: "list",
		Data:   collected,
	}
	if len(resp.Data) > limit {
		resp.HasMore = true
		resp.Data = resp.Data[:limit]
	}
	return resp, nil
}

func loadProviderFilePage(
	ctx context.Context,
	nativeRouter core.NativeFileRoutableProvider,
	state *providerFileListState,
	purpose string,
	limit int,
) (bool, error) {
	if state == nil {
		return false, nil
	}

	resp, err := nativeRouter.ListFiles(ctx, state.provider, purpose, limit, state.after)
	if err != nil {
		return false, err
	}

	state.index = 0
	state.items = nil
	state.hasMore = false
	state.exhausted = true
	if resp == nil || len(resp.Data) == 0 {
		return false, nil
	}

	state.items = make([]core.FileObject, len(resp.Data))
	copy(state.items, resp.Data)
	for i := range state.items {
		if strings.TrimSpace(state.items[i].Provider) == "" {
			state.items[i].Provider = state.provider
		}
	}
	state.hasMore = resp.HasMore
	state.after = strings.TrimSpace(state.items[len(state.items)-1].ID)
	state.exhausted = false
	return true, nil
}

func nextMergedFile(
	ctx context.Context,
	nativeRouter core.NativeFileRoutableProvider,
	states []*providerFileListState,
	purpose string,
	pageSize int,
) (*core.FileObject, error) {
	var best *providerFileListState
	for _, state := range states {
		for state != nil && !state.exhausted && state.index >= len(state.items) {
			if !state.hasMore {
				state.exhausted = true
				break
			}
			loaded, err := loadProviderFilePage(ctx, nativeRouter, state, purpose, pageSize)
			if err != nil {
				if isUnsupportedNativeFilesError(err) || isNotFoundGatewayError(err) {
					state.exhausted = true
					break
				}
				return nil, err
			}
			if !loaded {
				state.exhausted = true
				break
			}
		}
		if state == nil || state.exhausted || state.index >= len(state.items) {
			continue
		}
		if best == nil || fileSortsBefore(state.items[state.index], best.items[best.index]) {
			best = state
		}
	}
	if best == nil {
		return nil, nil
	}

	item := best.items[best.index]
	best.index++
	return &item, nil
}

func fileSortsBefore(left, right core.FileObject) bool {
	if left.CreatedAt == right.CreatedAt {
		if left.ID == right.ID {
			return left.Provider > right.Provider
		}
		return left.ID > right.ID
	}
	return left.CreatedAt > right.CreatedAt
}
