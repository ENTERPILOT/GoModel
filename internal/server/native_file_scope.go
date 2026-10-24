package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/filestore"
)

// listScopedFiles serves GET /v1/files for a user-path scoped credential from
// the gateway's ownership records. Provider listings are not tenant-aware, so
// a scoped caller sees exactly the files whose mapping lies in its scope; the
// mapping carries the metadata the list shape needs. Paging is exact and the
// cursor is always one of the caller's own files. Without a file store
// nothing is provably owned, so the listing is empty.
func (s *nativeFileService) listScopedFiles(ctx context.Context, scope core.AccessScope, providerType, purpose string, limit int, after string) (*core.FileListResponse, error) {
	resp := &core.FileListResponse{Object: "list", Data: []core.FileObject{}}
	if s.fileStore == nil {
		return resp, nil
	}
	filter := filestore.ListFilter{UserPath: scope.UserPath, ProviderType: providerType, Purpose: purpose}
	items, err := s.fileStore.List(ctx, filter, limit+1, after)
	if err != nil {
		if errors.Is(err, filestore.ErrNotFound) {
			return nil, core.NewNotFoundError("after cursor file not found: " + after)
		}
		return nil, core.NewProviderError("file_store", http.StatusInternalServerError, "failed to list file provider mappings", err)
	}
	if len(items) > limit {
		resp.HasMore = true
		items = items[:limit]
	}
	for _, item := range items {
		resp.Data = append(resp.Data, core.FileObject{
			ID:        item.ID,
			Object:    "file",
			Bytes:     item.Bytes,
			CreatedAt: item.CreatedAt,
			Filename:  item.Filename,
			Purpose:   item.Purpose,
			Provider:  item.ProviderType,
		})
	}
	return resp, nil
}
