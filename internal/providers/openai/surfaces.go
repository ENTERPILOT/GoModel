package openai

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
)

// BatchSurface is an embeddable facet exposing CompatibleProvider's native
// batch API. Partial-surface providers (see the CompatibleProvider doc)
// embed it to advertise exactly the core.NativeBatchProvider capability
// without inheriting the rest of the full OpenAI surface.
type BatchSurface struct {
	compat *CompatibleProvider
}

// NewBatchSurface wraps compat's batch endpoints as an embeddable facet.
func NewBatchSurface(compat *CompatibleProvider) *BatchSurface {
	return &BatchSurface{compat: compat}
}

var _ core.NativeBatchProvider = (*BatchSurface)(nil)

// CreateBatch creates a native batch job.
func (s *BatchSurface) CreateBatch(ctx context.Context, req *core.BatchRequest) (*core.BatchResponse, error) {
	return s.compat.CreateBatch(ctx, req)
}

// GetBatch retrieves a native batch job.
func (s *BatchSurface) GetBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	return s.compat.GetBatch(ctx, id)
}

// ListBatches lists native batch jobs.
func (s *BatchSurface) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
	return s.compat.ListBatches(ctx, limit, after)
}

// CancelBatch cancels a native batch job.
func (s *BatchSurface) CancelBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	return s.compat.CancelBatch(ctx, id)
}

// GetBatchResults fetches batch results via the output file API.
func (s *BatchSurface) GetBatchResults(ctx context.Context, id string) (*core.BatchResultsResponse, error) {
	return s.compat.GetBatchResults(ctx, id)
}

// FileSurface is an embeddable facet exposing CompatibleProvider's
// OpenAI-compatible files API. Partial-surface providers embed it to
// advertise exactly the core.NativeFileProvider capability without
// inheriting the rest of the full OpenAI surface.
type FileSurface struct {
	compat *CompatibleProvider
}

// NewFileSurface wraps compat's file endpoints as an embeddable facet.
func NewFileSurface(compat *CompatibleProvider) *FileSurface {
	return &FileSurface{compat: compat}
}

var _ core.NativeFileProvider = (*FileSurface)(nil)

// CreateFile uploads a file through the OpenAI-compatible /files API.
func (s *FileSurface) CreateFile(ctx context.Context, req *core.FileCreateRequest) (*core.FileObject, error) {
	return s.compat.CreateFile(ctx, req)
}

// ListFiles lists files through the OpenAI-compatible /files API.
func (s *FileSurface) ListFiles(ctx context.Context, purpose string, limit int, after string) (*core.FileListResponse, error) {
	return s.compat.ListFiles(ctx, purpose, limit, after)
}

// GetFile retrieves one file object through the OpenAI-compatible /files API.
func (s *FileSurface) GetFile(ctx context.Context, id string) (*core.FileObject, error) {
	return s.compat.GetFile(ctx, id)
}

// DeleteFile deletes a file object through the OpenAI-compatible /files API.
func (s *FileSurface) DeleteFile(ctx context.Context, id string) (*core.FileDeleteResponse, error) {
	return s.compat.DeleteFile(ctx, id)
}

// GetFileContent fetches raw file bytes through the /files/{id}/content API.
func (s *FileSurface) GetFileContent(ctx context.Context, id string) (*core.FileContentResponse, error) {
	return s.compat.GetFileContent(ctx, id)
}
