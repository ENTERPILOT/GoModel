package server

import (
	"context"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
)

// maxScopedFileListPages bounds how many provider pages one scoped ListFiles
// call reads while collecting files inside the caller's user-path scope.
// Provider listings are not tenant-aware, so a scoped caller sees only the
// files whose gateway ownership record lies in its scope; when the cap is hit
// before the page fills, has_more is reported true so the caller can page on.
const maxScopedFileListPages = 10

// listScopedFiles filters provider-backed listings through the gateway's
// ownership records for a non-global scope. fetch returns one provider page
// starting after the given cursor. Without a file store nothing is provably
// owned, so the listing is empty.
func (s *nativeFileService) listScopedFiles(
	ctx context.Context,
	scope core.AccessScope,
	fetch func(after string) (*core.FileListResponse, error),
	limit int,
	after string,
) (*core.FileListResponse, error) {
	resp := &core.FileListResponse{Object: "list", Data: []core.FileObject{}}
	if s.fileStore == nil {
		return resp, nil
	}

	cursor := after
	for range maxScopedFileListPages {
		page, err := fetch(cursor)
		if err != nil {
			return nil, err
		}
		if page == nil || len(page.Data) == 0 {
			return resp, nil
		}

		ids := make([]string, 0, len(page.Data))
		for _, item := range page.Data {
			ids = append(ids, item.ID)
		}
		owned, err := s.fileStore.GetMany(ctx, ids)
		if err != nil {
			return nil, core.NewProviderError("file_store", http.StatusInternalServerError, "failed to load file provider mappings", err)
		}
		for _, item := range page.Data {
			stored, ok := owned[item.ID]
			if !ok || stored == nil || !scope.Allows(stored.UserPath) {
				continue
			}
			if len(resp.Data) == limit {
				resp.HasMore = true
				return resp, nil
			}
			resp.Data = append(resp.Data, item)
		}
		if !page.HasMore {
			return resp, nil
		}
		cursor = page.Data[len(page.Data)-1].ID
	}
	resp.HasMore = true
	return resp, nil
}
