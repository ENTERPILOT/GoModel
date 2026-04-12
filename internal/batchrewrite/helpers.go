package batchrewrite

import (
	"context"
	"log/slog"
	"maps"
	"strings"

	"gomodel/internal/core"
)

const defaultCleanupLogMessage = "failed to delete rewritten batch input file"

// FileDeleter is the native file API surface needed to remove temporary batch
// input files created during request rewriting.
type FileDeleter interface {
	DeleteFile(ctx context.Context, providerType, id string) (*core.FileDeleteResponse, error)
}

// FileRouter resolves a routed native file provider lazily.
type FileRouter func() (core.NativeFileRoutableProvider, error)

// RecordPreparation stores request-scoped rewrite metadata for persistence and
// later cleanup.
func RecordPreparation(ctx context.Context, original, rewritten *core.BatchRequest) {
	if ctx == nil || original == nil || rewritten == nil {
		return
	}
	metadata := core.GetBatchPreparationMetadata(ctx)
	if metadata == nil {
		return
	}
	metadata.RecordInputFileRewrite(original.InputFileID, rewritten.InputFileID)
}

// RecordResult stores rewrite metadata produced by an explicit batch preparer.
func RecordResult(ctx context.Context, result *core.BatchRewriteResult) {
	if ctx == nil || result == nil {
		return
	}
	metadata := core.GetBatchPreparationMetadata(ctx)
	if metadata == nil {
		return
	}
	metadata.RecordInputFileRewrite(result.OriginalInputFileID, result.RewrittenInputFileID)
}

// CleanupFile deletes a temporary rewritten batch input file. It returns true
// only when a non-empty file id was deleted successfully.
func CleanupFile(ctx context.Context, files FileDeleter, providerType, fileID, logMessage string, attrs ...any) bool {
	fileID = strings.TrimSpace(fileID)
	if files == nil || fileID == "" {
		return false
	}
	if _, err := files.DeleteFile(ctx, providerType, fileID); err != nil {
		if logMessage == "" {
			logMessage = defaultCleanupLogMessage
		}
		logAttrs := make([]any, 0, len(attrs)+6)
		logAttrs = append(logAttrs, attrs...)
		logAttrs = append(logAttrs, "provider", providerType, "file_id", fileID, "error", err)
		slog.Warn(logMessage, logAttrs...)
		return false
	}
	return true
}

// CleanupFileFromRouter resolves the native file API only when there is a file
// id to delete.
func CleanupFileFromRouter(ctx context.Context, router FileRouter, providerType, fileID, logMessage string, attrs ...any) bool {
	fileID = strings.TrimSpace(fileID)
	if router == nil || fileID == "" {
		return false
	}
	files, err := router()
	if err != nil {
		return false
	}
	return CleanupFile(ctx, files, providerType, fileID, logMessage, attrs...)
}

// CleanupSupersededFileFromRouter deletes a local rewrite artifact only when a
// later rewrite has replaced it in the request-scoped batch metadata.
func CleanupSupersededFileFromRouter(ctx context.Context, router FileRouter, providerType, fileID, logMessage string, attrs ...any) bool {
	if !ShouldCleanupSupersededFile(ctx, fileID) {
		return false
	}
	return CleanupFileFromRouter(ctx, router, providerType, fileID, logMessage, attrs...)
}

// CleanupSupersededFile deletes a local rewrite artifact only when a later
// rewrite has replaced it in the request-scoped batch metadata.
func CleanupSupersededFile(ctx context.Context, files FileDeleter, providerType, fileID, logMessage string, attrs ...any) bool {
	if !ShouldCleanupSupersededFile(ctx, fileID) {
		return false
	}
	return CleanupFile(ctx, files, providerType, fileID, logMessage, attrs...)
}

// ShouldCleanupSupersededFile reports whether fileID is a temporary rewrite
// artifact that has been superseded by a later rewrite stage.
func ShouldCleanupSupersededFile(ctx context.Context, fileID string) bool {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return false
	}
	metadata := core.GetBatchPreparationMetadata(ctx)
	if metadata == nil {
		return false
	}
	return strings.TrimSpace(metadata.RewrittenInputFileID) != fileID
}

// MergeEndpointHints returns a fresh map containing left hints overwritten by
// right hints. It preserves nil when both inputs are empty.
func MergeEndpointHints(left, right map[string]string) map[string]string {
	if len(left) == 0 {
		if len(right) == 0 {
			return nil
		}
		merged := make(map[string]string, len(right))
		maps.Copy(merged, right)
		return merged
	}

	merged := make(map[string]string, len(left)+len(right))
	maps.Copy(merged, left)
	maps.Copy(merged, right)
	return merged
}
