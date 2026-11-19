package inferencehook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// userAgent names the caller; "anthropic-dlp/1" is Anthropic's own.
	userAgent = "gomodel-inference-hook/1"
	// maxVerdictBytes is how much of a verdict body the protocol reads.
	maxVerdictBytes = 64 << 10
	// maxDenyReason is where the protocol truncates deny_reason, in characters.
	maxDenyReason = 500
)

// Verdict actions.
const (
	verdictAllow = "allow"
	verdictDeny  = "deny"
)

// referenceIDRe is the protocol's shape of a reference_id; one that does
// not match is dropped, and the verdict stands.
var referenceIDRe = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,50}$`)

// verdict is the server's answer to one frame.
type verdict struct {
	action      string
	denyReason  string
	referenceID string
	// requestID is the request_id of the frame the verdict answers.
	requestID string
}

// client sends frames to the AI security server.
type client struct {
	http     *http.Client
	settings *settings
	now      func() time.Time
}

// newClient copies the host client so redirects are not followed: the
// protocol counts one as a failure, and following it would hand the
// transcript and the auth header to another destination.
func newClient(base *http.Client, s *settings) *client {
	guarded := *base
	guarded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client{http: &guarded, settings: s, now: time.Now}
}

// verdict posts one signed frame and returns the server's verdict. Anything
// but HTTP 200 with a parseable allow or deny is an error, never a deny:
// the guardrail's fail mode decides what a failed hook does. Errors never
// carry the response body, which can echo the transcript.
func (c *client) verdict(ctx context.Context, f frame) (verdict, error) {
	body, err := json.Marshal(f)
	if err != nil {
		return verdict{}, fmt.Errorf("%s: encode frame: %w", Name, err)
	}
	ctx, cancel := context.WithTimeout(ctx, c.settings.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.settings.endpointURL, bytes.NewReader(body))
	if err != nil {
		return verdict{}, fmt.Errorf("%s: build request: %w", Name, redactURLError(err))
	}
	timestamp := strconv.FormatInt(c.now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	// An explicit identity keeps the transport from asking for gzip: the
	// protocol's verdict body is uncompressed.
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("webhook-id", f.RequestID)
	req.Header.Set("webhook-timestamp", timestamp)
	req.Header.Set("webhook-signature", sign(c.settings.signingKey, f.RequestID, timestamp, body))
	if c.settings.authValue != "" {
		req.Header.Set(c.settings.authHeader, c.settings.authValue)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return verdict{}, fmt.Errorf("%s: security server unreachable: %w", Name, redactURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxVerdictBytes))
		return verdict{}, fmt.Errorf("%s: security server returned HTTP %d", Name, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxVerdictBytes+1))
	if err != nil {
		return verdict{}, fmt.Errorf("%s: read verdict: %w", Name, redactURLError(err))
	}
	if len(raw) > maxVerdictBytes {
		return verdict{}, fmt.Errorf("%s: verdict is larger than %d bytes", Name, maxVerdictBytes)
	}
	v, err := parseVerdict(raw)
	v.requestID = f.RequestID
	return v, err
}

// sign returns the Standard Webhooks signature of one delivery: an
// HMAC-SHA256 over "{id}.{timestamp}.{body}", base64, behind the "v1,"
// version tag.
func sign(key []byte, id, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// parseVerdict reads a verdict body. Only the action decides whether it is
// valid: a deny is never lost over a malformed deny_reason or reference_id,
// and unknown fields are ignored.
func parseVerdict(raw []byte) (verdict, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return verdict{}, fmt.Errorf("%s: verdict is not a JSON object", Name)
	}
	v := verdict{action: stringField(fields["action"])}
	if v.action != verdictAllow && v.action != verdictDeny {
		return verdict{}, fmt.Errorf("%s: verdict action must be %q or %q", Name, verdictAllow, verdictDeny)
	}
	if v.action == verdictDeny {
		v.denyReason = truncateRunes(strings.TrimSpace(stringField(fields["deny_reason"])), maxDenyReason)
	}
	if id := stringField(fields["reference_id"]); referenceIDRe.MatchString(id) {
		v.referenceID = id
	}
	return v, nil
}

// stringField decodes a JSON string; anything else is "".
func stringField(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

// redactURLError keeps transport errors free of the request URL.
func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}
