package modeldata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/httpclient"
)

// fetchTimeout is a safety net for one catalog download; callers should use
// context deadlines for finer-grained control.
const fetchTimeout = 60 * time.Second

// maxBodySize caps a catalog, whether downloaded or read from disk.
const maxBodySize = 10 * 1024 * 1024 // 10 MB

// httpClient is shared by every catalog download so refreshes reuse one
// connection pool. Its transport follows the trust settings (private CA,
// mTLS) installed at startup or by a reload, so creating it at package init
// is safe.
var httpClient = httpclient.NewClientWithTimeout(fetchTimeout)

// FetchResult carries the outcome of one conditional model list fetch.
type FetchResult struct {
	List *ModelList
	Raw  []byte
	// ETag is the validator to send on the next conditional fetch. Empty when
	// the server did not return one.
	ETag string
	// NotModified is true when the server answered 304 for the presented ETag;
	// List and Raw are nil and the caller keeps its current data.
	NotModified bool
}

// Fetch downloads and parses the model list from the given URL.
// Returns the parsed ModelList, the raw JSON bytes (for caching), and any error.
// Returns nil, nil, nil if the URL is empty (feature disabled).
// The caller controls timeout via the provided context (e.g. context.WithTimeout).
func Fetch(ctx context.Context, url string) (*ModelList, []byte, error) {
	result, err := FetchIfChanged(ctx, url, "")
	return result.List, result.Raw, err
}

// FetchIfChanged downloads and parses the model list unless the server reports
// it unchanged. When etag is non-empty it is sent as If-None-Match; a 304
// response returns NotModified=true with the etag carried forward, skipping
// the download and reparse entirely. Servers without ETag support keep
// answering 200, so callers transparently degrade to unconditional fetching.
// Returns a zero FetchResult and nil error if the URL is empty (feature disabled).
func FetchIfChanged(ctx context.Context, url, etag string) (FetchResult, error) {
	if url == "" {
		return FetchResult{}, nil
	}
	if path, ok := localPath(url); ok {
		return readLocal(path, etag)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return FetchResult{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return FetchResult{}, fmt.Errorf("fetching model list: %w", err)
	}
	defer resp.Body.Close()

	if etag != "" && resp.StatusCode == http.StatusNotModified {
		// RFC 9111: a 304 may carry updated metadata for the stored
		// representation; adopt its ETag when present so future conditional
		// requests use the server's current validator.
		if respETag := resp.Header.Get("ETag"); respETag != "" {
			etag = respETag
		}
		return FetchResult{ETag: etag, NotModified: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return FetchResult{}, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url)
	}

	limited := io.LimitReader(resp.Body, maxBodySize+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return FetchResult{}, fmt.Errorf("reading response body: %w", err)
	}
	if len(raw) > maxBodySize {
		return FetchResult{}, fmt.Errorf("response body too large (exceeds %d bytes)", maxBodySize)
	}

	list, err := Parse(raw)
	if err != nil {
		return FetchResult{}, err
	}

	return FetchResult{List: list, Raw: raw, ETag: resp.Header.Get("ETag")}, nil
}

// localPath reports whether location names a file on the local filesystem
// and returns the path. Both "file:///etc/gomodel/models.json" and a bare
// "/etc/gomodel/models.json" (or a relative path) are accepted, so an
// air-gapped install can ship the catalog next to the binary or mount it.
func localPath(location string) (string, bool) {
	trimmed := strings.TrimSpace(location)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "file://"):
		u, err := neturl.Parse(trimmed)
		if err != nil || u.Path == "" {
			// "file://relative/path" has a host and no path; treat the
			// remainder as the path so the mistake still resolves.
			return strings.TrimPrefix(trimmed[len("file://"):], "/"), true
		}
		if u.Host != "" && u.Host != "localhost" {
			return u.Host + u.Path, true
		}
		return u.Path, true
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return "", false
	default:
		return trimmed, true
	}
}

// readLocal loads the catalog from disk. The validator is a digest of the
// content, so an unchanged file reports NotModified exactly like a 304 and
// callers skip the reparse and re-enrichment.
func readLocal(path, etag string) (FetchResult, error) {
	// Open non-blocking so a FIFO or device at the path returns immediately
	// instead of hanging with no context to cancel it, then validate the
	// descriptor we actually hold rather than a path that may have been
	// swapped underneath us. O_NONBLOCK has no effect on regular files.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return FetchResult{}, fmt.Errorf("reading model list file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return FetchResult{}, fmt.Errorf("reading model list file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return FetchResult{}, fmt.Errorf("model list file %s is not a regular file (%s)", path, info.Mode().Type())
	}
	// Reject an oversized file from its metadata before allocating anything,
	// and keep the read itself bounded in case the file grows in between.
	if info.Size() > maxBodySize {
		return FetchResult{}, fmt.Errorf("model list file too large (%d bytes exceeds %d)", info.Size(), maxBodySize)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBodySize+1))
	if err != nil {
		return FetchResult{}, fmt.Errorf("reading model list file: %w", err)
	}
	if len(raw) > maxBodySize {
		return FetchResult{}, fmt.Errorf("model list file too large (exceeds %d bytes)", maxBodySize)
	}
	sum := sha256.Sum256(raw)
	digest := `"sha256-` + hex.EncodeToString(sum[:]) + `"`
	if etag != "" && etag == digest {
		return FetchResult{ETag: digest, NotModified: true}, nil
	}
	list, err := Parse(raw)
	if err != nil {
		return FetchResult{}, err
	}
	return FetchResult{List: list, Raw: raw, ETag: digest}, nil
}

// Parse deserializes raw JSON bytes into a ModelList.
func Parse(raw []byte) (*ModelList, error) {
	var list ModelList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parsing model list JSON: %w", err)
	}
	list.buildReverseIndex()
	return &list, nil
}
