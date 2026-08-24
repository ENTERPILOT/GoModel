package modeldata

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/goccy/go-json"
)

// httpClient is a shared HTTP client for model list fetching.
// The 60-second timeout acts as a safety net; callers should use context
// deadlines for finer-grained control.
var httpClient = &http.Client{
	Timeout: 60 * time.Second,
}

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

	client := httpClient

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return FetchResult{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := client.Do(req)
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

	const maxBodySize = 10 * 1024 * 1024 // 10 MB
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

// Parse deserializes raw JSON bytes into a ModelList.
func Parse(raw []byte) (*ModelList, error) {
	var list ModelList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parsing model list JSON: %w", err)
	}
	list.buildReverseIndex()
	return &list, nil
}
