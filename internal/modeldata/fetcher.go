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

// Fetch downloads and parses the model list from the given URL.
// Returns the parsed ModelList, the raw JSON bytes (for caching), and any error.
// Returns nil, nil, nil if the URL is empty (feature disabled).
// The caller controls timeout via the provided context (e.g. context.WithTimeout).
func Fetch(ctx context.Context, url string) (*ModelList, []byte, error) {
	if url == "" {
		return nil, nil, nil
	}

	client := httpClient

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching model list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url)
	}

	const maxBodySize = 10 * 1024 * 1024 // 10 MB
	limited := io.LimitReader(resp.Body, maxBodySize+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, fmt.Errorf("reading response body: %w", err)
	}
	if len(raw) > maxBodySize {
		return nil, nil, fmt.Errorf("response body too large (exceeds %d bytes)", maxBodySize)
	}

	list, err := Parse(raw)
	if err != nil {
		return nil, nil, err
	}

	return list, raw, nil
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
