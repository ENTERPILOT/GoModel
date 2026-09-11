package presidio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// maxResponseBytes bounds what is read from the analyzer.
const maxResponseBytes = 4 << 20

// analyzerRequest is the body of POST /analyze.
type analyzerRequest struct {
	Text             string          `json:"text"`
	Language         string          `json:"language"`
	Entities         []string        `json:"entities,omitempty"`
	ScoreThreshold   *float64        `json:"score_threshold,omitempty"`
	AllowList        []string        `json:"allow_list,omitempty"`
	AdHocRecognizers json.RawMessage `json:"ad_hoc_recognizers,omitempty"`
	CorrelationID    string          `json:"correlation_id,omitempty"`
}

// analyzerResult is one entity the analyzer found. Start and End are
// character (code point) offsets, as Python counts them.
type analyzerResult struct {
	EntityType string  `json:"entity_type"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Score      float64 `json:"score"`
}

// client talks to a Presidio analyzer service.
type client struct {
	http     *http.Client
	baseURL  string
	apiKey   string
	settings *settings
}

// analyze runs the analyzer on text. Errors never carry the response
// body: an analyzer error page can echo the text it was given.
func (c *client) analyze(ctx context.Context, text, correlationID string) ([]analyzerResult, error) {
	req := analyzerRequest{
		Text:             text,
		Language:         c.settings.language,
		Entities:         c.settings.entities,
		ScoreThreshold:   c.settings.scoreThreshold,
		AllowList:        c.settings.allowList,
		AdHocRecognizers: c.settings.adHocRecognizers,
		CorrelationID:    correlationID,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%s: encode analyze request: %w", Name, err)
	}
	resp, err := c.do(ctx, http.MethodPost, "/analyze", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: analyzer returned HTTP %d", Name, resp.StatusCode)
	}
	var results []analyzerResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&results); err != nil {
		return nil, fmt.Errorf("%s: analyzer returned an unreadable response: %w", Name, err)
	}
	return results, nil
}

// health asks for the entity types of the configured language. It fails
// when the service is unreachable and when the language has no model, the
// two ways an instance can be misconfigured.
func (c *client) health(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/supportedentities?language="+url.QueryEscape(c.settings.language), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("analyzer returned HTTP %d for language %q; is the language configured on the analyzer?", resp.StatusCode, c.settings.language)
	}
	return nil
}

func (c *client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", Name, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: analyzer unreachable: %w", Name, redactURLError(err))
	}
	return resp, nil
}

// redactURLError keeps transport errors free of the request URL, which
// could carry credentials in its userinfo.
func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}
