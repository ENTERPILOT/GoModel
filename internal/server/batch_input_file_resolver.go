package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/filestore"
	"github.com/enterpilot/gomodel/internal/gateway"
)

type batchInputFileProviderResolver struct {
	provider  core.RoutableProvider
	fileStore filestore.Store
}

func newBatchInputFileProviderResolver(provider core.RoutableProvider, fileStore filestore.Store) gateway.BatchInputFileProviderResolver {
	if provider == nil && fileStore == nil {
		return nil
	}
	return &batchInputFileProviderResolver{
		provider:  provider,
		fileStore: fileStore,
	}
}

func (r *batchInputFileProviderResolver) ResolveBatchInputFileProvider(ctx context.Context, fileID string) (string, bool, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return "", false, nil
	}
	var lookupErr error
	if r.fileStore != nil {
		stored, err := r.fileStore.Get(ctx, fileID)
		switch {
		case err == nil && stored != nil && strings.TrimSpace(stored.ProviderType) != "":
			return strings.TrimSpace(stored.ProviderType), true, nil
		case err == nil:
			// Legacy rows without provider_type fall through to provider probing.
		case errors.Is(err, filestore.ErrNotFound):
		default:
			lookupErr = err
		}
	}
	providerType, ok, fallbackErr := r.resolveProviderByFallback(ctx, fileID)
	if fallbackErr != nil {
		if lookupErr != nil {
			if isClientGatewayError(fallbackErr) {
				return "", false, fallbackErr
			}
			return "", false, core.NewProviderError("file_store", http.StatusBadGateway, "failed to resolve input file provider", errors.Join(lookupErr, fallbackErr))
		}
		return "", false, fallbackErr
	}
	if ok {
		return providerType, true, nil
	}
	if lookupErr != nil {
		return "", false, core.NewProviderError("file_store", http.StatusInternalServerError, "failed to look up input file provider and fallback provider probing did not resolve the file", lookupErr)
	}
	return "", false, nil
}

func isClientGatewayError(err error) bool {
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		return false
	}
	status := gatewayErr.HTTPStatusCode()
	return status >= http.StatusBadRequest && status < http.StatusInternalServerError
}

func (r *batchInputFileProviderResolver) resolveProviderByFallback(ctx context.Context, fileID string) (string, bool, error) {
	candidates := nativeBatchFileProviderCandidates(r.provider)
	if len(candidates) == 0 {
		return "", false, nil
	}
	if len(candidates) == 1 {
		// Single-provider batches skip the extra GetFile preflight; upstream
		// batch creation still validates the file ID.
		return candidates[0], true, nil
	}

	nativeFiles, ok := r.provider.(core.NativeFileRoutableProvider)
	if !ok {
		return "", false, nil
	}

	var matches []string
	var firstErr error
	for _, candidate := range candidates {
		if _, err := nativeFiles.GetFile(ctx, candidate, fileID); err == nil {
			matches = append(matches, candidate)
			continue
		} else if isNotFoundGatewayError(err) || isUnsupportedNativeFilesError(err) {
			continue
		} else if firstErr == nil {
			firstErr = err
		}
	}
	switch len(matches) {
	case 0:
		if firstErr != nil {
			return "", false, firstErr
		}
		return "", false, nil
	case 1:
		return matches[0], true, nil
	default:
		return "", false, core.NewInvalidRequestError("input_file_id is ambiguous across providers; pass metadata.provider", nil)
	}
}

func nativeBatchFileProviderCandidates(provider core.RoutableProvider) []string {
	fileTypes, ok := provider.(core.NativeFileProviderTypeLister)
	if !ok {
		return nil
	}
	candidates := normalizeProviderTypeList(fileTypes.NativeFileProviderTypes())
	if len(candidates) == 0 {
		return nil
	}
	batchTypes, ok := provider.(core.NativeBatchProviderTypeLister)
	if !ok {
		return candidates
	}
	batchProviderTypes := batchTypes.NativeBatchProviderTypes()
	batchSet := make(map[string]struct{}, len(batchProviderTypes))
	for _, providerType := range batchProviderTypes {
		providerType = strings.TrimSpace(providerType)
		if providerType != "" {
			batchSet[providerType] = struct{}{}
		}
	}
	// Reusing candidates for filtered is safe because append writes only
	// batchSet matches up to the current index while
	// for _, candidate := range candidates reads ahead.
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if _, ok := batchSet[candidate]; ok {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func normalizeProviderTypeList(providerTypes []string) []string {
	seen := make(map[string]struct{}, len(providerTypes))
	out := make([]string, 0, len(providerTypes))
	for _, providerType := range providerTypes {
		providerType = strings.TrimSpace(providerType)
		if providerType == "" {
			continue
		}
		if _, ok := seen[providerType]; ok {
			continue
		}
		seen[providerType] = struct{}{}
		out = append(out, providerType)
	}
	sort.Strings(out)
	return out
}
