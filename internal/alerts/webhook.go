package alerts

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/version"
)

// Headers of a webhook delivery.
const (
	// HeaderEvent carries the alert type, HeaderDelivery the alert ID.
	HeaderEvent    = "X-GoModel-Event"
	HeaderDelivery = "X-GoModel-Delivery"
	// HeaderSignature carries "sha256=" and the hex HMAC-SHA256 of the request
	// body, keyed with the webhook secret. It is sent only when a secret is set.
	HeaderSignature = "X-GoModel-Signature"
)

const (
	defaultWebhookTimeout = 10 * time.Second
	// webhookBackoff is the wait before the first retry; it doubles with
	// every further one, up to maxWebhookBackoff.
	webhookBackoff    = time.Second
	maxWebhookBackoff = 30 * time.Second
)

// webhook POSTs alerts as JSON to one endpoint.
type webhook struct {
	url        string
	host       string // for logs; the full URL may carry a token
	secret     []byte
	client     *http.Client
	maxRetries int
	backoff    time.Duration
}

// newWebhook validates the endpoint. Its errors never echo the URL.
func newWebhook(cfg WebhookConfig, timeout time.Duration, maxRetries int) (*webhook, error) {
	raw := strings.TrimSpace(cfg.URL)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("must be an http or https URL")
	}
	if timeout <= 0 {
		timeout = defaultWebhookTimeout
	}
	return &webhook{
		url:    raw,
		host:   parsed.Host,
		secret: []byte(cfg.Secret),
		// The shared transport applies the proxy environment and http.tls
		// trust, and never follows a redirect to another origin.
		client:     httpclient.NewClientWithTimeout(timeout),
		maxRetries: max(maxRetries, 0),
		backoff:    webhookBackoff,
	}, nil
}

// send delivers event, retrying a failure that may be transient.
func (w *webhook) send(ctx context.Context, event ext.AlertEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode alert: %w", err)
	}
	for attempt := 0; ; attempt++ {
		retry, err := w.post(ctx, body, event)
		if err == nil {
			return nil
		}
		if !retry || attempt >= w.maxRetries {
			return fmt.Errorf("after %d attempt(s): %w", attempt+1, err)
		}
		wait := min(w.backoff<<attempt, maxWebhookBackoff)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("after %d attempt(s): %w", attempt+1, err)
		case <-timer.C:
		}
	}
}

// post makes one attempt and reports whether a failure is worth retrying.
func (w *webhook) post(ctx context.Context, body []byte, event ext.AlertEvent) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return false, errors.New("build request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "GoModel/"+version.Version)
	req.Header.Set(HeaderEvent, event.Type)
	req.Header.Set(HeaderDelivery, event.ID)
	if len(w.secret) > 0 {
		req.Header.Set(HeaderSignature, Sign(w.secret, body))
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return ctx.Err() == nil, transportError(err)
	}
	defer resp.Body.Close()
	// Read a little so the connection can be reused; the response is unused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return false, nil
	}
	retry = resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout
	return retry, fmt.Errorf("webhook answered HTTP %d", resp.StatusCode)
}

// Sign returns the HeaderSignature value for body: "sha256=" and the hex
// HMAC-SHA256 of body keyed with secret.
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// transportError strips the request URL net/http puts into its errors: a
// webhook URL commonly carries a token.
func transportError(err error) error {
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		return fmt.Errorf("%s: %w", urlErr.Op, urlErr.Err)
	}
	return err
}
