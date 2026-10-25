package workloadidentity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-json"
)

// OAuth URNs (RFC 8693, RFC 7523); identifiers, not credentials.
const (
	grantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange" //nolint:gosec // G101: OAuth URN.
	grantTypeJWTBearer     = "urn:ietf:params:oauth:grant-type:jwt-bearer"     //nolint:gosec // G101: OAuth URN.
	subjectTokenTypeJWT    = "urn:ietf:params:oauth:token-type:jwt"            //nolint:gosec // G101: OAuth URN.

	// maxResponseBytes bounds what is read from the token endpoint.
	maxResponseBytes = 1 << 20
	// maxErrorDetail bounds the upstream error text kept in an error.
	maxErrorDetail = 300
)

type mintedToken struct {
	accessToken string
	expiry      time.Time
}

// tokenResponse covers both endpoints: RFC 6749 §5.1 fields, plus OpenAI's
// absolute expires_at.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	ExpiresAt   int64  `json:"expires_at"`
}

// exchange reads the identity token and trades it for an access token.
func (s *Source) exchange(ctx context.Context) (mintedToken, error) {
	identityToken, err := s.identityToken()
	if err != nil {
		return mintedToken{}, err
	}
	body, err := json.Marshal(s.requestBody(identityToken))
	if err != nil {
		return mintedToken{}, fmt.Errorf("encode token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.TokenEndpoint, bytes.NewReader(body))
	if err != nil {
		return mintedToken{}, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return mintedToken{}, fmt.Errorf("token endpoint request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return mintedToken{}, fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return mintedToken{}, exchangeError(resp, raw)
	}

	var parsed tokenResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return mintedToken{}, errors.New("token endpoint returned an unreadable response")
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return mintedToken{}, errors.New("token endpoint returned no access_token")
	}
	expiry := parsed.expiry(s.now())
	if !expiry.IsZero() && !expiry.After(s.now()) {
		return mintedToken{}, errors.New("token endpoint returned an expired access token")
	}
	return mintedToken{accessToken: parsed.AccessToken, expiry: expiry}, nil
}

// expiry prefers the absolute expires_at over expires_in. It is zero when
// the endpoint reported neither.
func (r tokenResponse) expiry(now time.Time) time.Time {
	switch {
	case r.ExpiresAt > 0:
		return time.Unix(r.ExpiresAt, 0)
	case r.ExpiresIn > 0:
		return now.Add(time.Duration(r.ExpiresIn) * time.Second)
	default:
		return time.Time{}
	}
}

// identityToken returns the platform identity token, re-reading the file on
// every call because projected tokens rotate in place.
func (s *Source) identityToken() (string, error) {
	if path := strings.TrimSpace(s.cfg.IdentityTokenFile); path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // G304: the operator configures this path.
		if err != nil {
			return "", fmt.Errorf("read identity_token_file: %w", err)
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", errors.New("identity_token_file is empty")
		}
		return token, nil
	}
	return strings.TrimSpace(s.cfg.IdentityToken), nil
}

// requestBody builds the JSON body each provider documents for its grant.
func (s *Source) requestBody(identityToken string) map[string]string {
	c := s.cfg
	switch c.Grant {
	case TokenExchange:
		return map[string]string{
			"grant_type":           grantTypeTokenExchange,
			"subject_token_type":   subjectTokenTypeJWT,
			"subject_token":        identityToken,
			"identity_provider_id": strings.TrimSpace(c.IdentityProviderID),
			"service_account_id":   strings.TrimSpace(c.ServiceAccountID),
		}
	default: // JWTBearer; Validate rejects anything else.
		body := map[string]string{
			"grant_type":         grantTypeJWTBearer,
			"assertion":          identityToken,
			"federation_rule_id": strings.TrimSpace(c.FederationRuleID),
			"organization_id":    strings.TrimSpace(c.OrganizationID),
			"service_account_id": strings.TrimSpace(c.ServiceAccountID),
		}
		if workspace := strings.TrimSpace(c.WorkspaceID); workspace != "" {
			body["workspace_id"] = workspace
		}
		return body
	}
}

// exchangeError describes a rejected exchange from the status and the
// error fields of the body. The rest of the body is dropped, so nothing the
// endpoint echoes back can leak into logs.
func exchangeError(resp *http.Response, raw []byte) error {
	var msg strings.Builder
	_, _ = fmt.Fprintf(&msg, "token endpoint returned HTTP %d", resp.StatusCode)
	if detail := errorDetail(raw); detail != "" {
		msg.WriteString(": " + detail)
	}
	for _, header := range []string{"request-id", "x-request-id"} {
		if id := resp.Header.Get(header); id != "" {
			msg.WriteString(" (request id " + truncate(id, 100) + ")")
			break
		}
	}
	return errors.New(msg.String())
}

// errorDetail extracts the OAuth ({"error": "...", "error_description":
// "..."}) or Anthropic ({"error": {"type": "...", "message": "..."}}) error
// fields.
func errorDetail(raw []byte) string {
	var body struct {
		Error            json.RawMessage `json:"error"`
		ErrorDescription string          `json:"error_description"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	var parts []string
	var code string
	var nested struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	switch {
	case json.Unmarshal(body.Error, &code) == nil:
		parts = append(parts, code)
	case json.Unmarshal(body.Error, &nested) == nil:
		parts = append(parts, nested.Type, nested.Message)
	}
	parts = append(parts, body.ErrorDescription)
	var kept []string
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			kept = append(kept, part)
		}
	}
	return truncate(strings.Join(kept, ": "), maxErrorDetail)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
