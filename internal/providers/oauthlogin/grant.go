package oauthlogin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/httpclient"
)

// grantTimeout bounds one call to an authorization server. The device grant
// polls, so a single token request must not be allowed to hang the poll.
const grantTimeout = 15 * time.Second

// bodyLimit caps how much of an authorization server response is read. Token
// responses are small; the cap keeps a misbehaving endpoint from buffering
// without bound.
const bodyLimit = 1 << 20

// defaultPollInterval is how often the token endpoint is polled while the
// user has not yet approved. RFC 8628 requires honoring the interval the
// authorization server returns; this only applies when it returns none.
const defaultPollInterval = 5 * time.Second

// minPollInterval is the floor applied to a server-supplied interval, so a
// zero or negative value cannot turn the poll into a busy loop.
const minPollInterval = time.Second

// errSlowDown is the RFC 8628 signal to back off. It is internal: callers see
// a longer wait, not the error.
var errSlowDown = errors.New("authorization pending, slow down")

// errAuthorizationPending means the user has not approved the code yet.
var errAuthorizationPending = errors.New("authorization pending")

// errAccessDenied means the user declined, or the authorization server
// rejected the grant outright.
var errAccessDenied = errors.New("authorization denied")

// errExpiredToken means the device code expired before it was approved.
var errExpiredToken = errors.New("device code expired")

// DeviceAuthorization is what an operator needs in order to approve a login:
// where to go, which code to enter, and how long the code stays valid. It
// carries nothing that authenticates a request.
type DeviceAuthorization struct {
	// DeviceCode is redeemed at the token endpoint. It is a secret: it never
	// leaves the gateway, and it is never logged.
	DeviceCode string
	// UserCode is the code the user enters at VerificationURL.
	UserCode string
	// VerificationURL is where the user approves the request. It is the
	// complete URL when the server returned one, otherwise the plain
	// verification URL.
	VerificationURL string
	// ExpiresIn is how many seconds the device code stays valid.
	ExpiresIn int
	// Interval is how many seconds to wait between token polls.
	Interval int
}

// tokenSet is one successful token response. RefreshToken is empty when the
// server did not rotate it, and the caller keeps the one it already holds.
type tokenSet struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	// ExpiresIn is how many seconds the access token stays valid. Zero means
	// the server did not say; the caller treats the token as long-lived.
	ExpiresIn int
}

// grantClient talks to one account kind's authorization server.
type grantClient struct {
	httpClient     *http.Client
	deviceEndpoint string
	tokenEndpoint  string
	clientID       string
	scope          string
}

// newGrantClient builds a client for the authorization server. httpClient is
// the provider's outbound client, so the grant goes through the same proxy
// and TLS trust as inference; nil uses the gateway default.
func newGrantClient(httpClient *http.Client, kind accountKind, clientID string) *grantClient {
	if httpClient == nil {
		httpClient = httpclient.NewDefaultHTTPClient()
	}
	return &grantClient{
		httpClient:     httpClient,
		deviceEndpoint: kind.deviceEndpoint,
		tokenEndpoint:  kind.tokenEndpoint,
		clientID:       clientID,
		scope:          kind.scope,
	}
}

// authorize starts a device authorization grant (RFC 8628 §3.1 and §3.2).
func (g *grantClient) authorize(ctx context.Context) (DeviceAuthorization, error) {
	form := url.Values{"client_id": {g.clientID}}
	if g.scope != "" {
		form.Set("scope", g.scope)
	}
	var resp deviceCodeResponse
	if err := g.postForm(ctx, g.deviceEndpoint, form, &resp); err != nil {
		return DeviceAuthorization{}, fmt.Errorf("start device authorization: %w", err)
	}
	if resp.Error != "" {
		return DeviceAuthorization{}, fmt.Errorf("start device authorization: %w", classifyGrantError(resp.Error))
	}
	if resp.DeviceCode == "" || resp.UserCode == "" {
		return DeviceAuthorization{}, errors.New("start device authorization: the authorization server returned no device code")
	}
	verificationURL := firstNonEmpty(resp.VerificationURIComplete, resp.VerificationURI, resp.VerificationURL)
	if verificationURL == "" {
		return DeviceAuthorization{}, errors.New("start device authorization: the authorization server returned no verification URL")
	}
	interval := resp.Interval
	if interval <= 0 {
		interval = int(defaultPollInterval / time.Second)
	}
	return DeviceAuthorization{
		DeviceCode:      resp.DeviceCode,
		UserCode:        resp.UserCode,
		VerificationURL: verificationURL,
		ExpiresIn:       resp.ExpiresIn,
		Interval:        interval,
	}, nil
}

// pollToken blocks until the user approves the device code, the code expires,
// or ctx is cancelled. It waits the interval the authorization server
// advertised and lengthens it when the server asks it to slow down (RFC 8628
// §3.5).
func (g *grantClient) pollToken(ctx context.Context, auth DeviceAuthorization) (tokenSet, error) {
	interval := time.Duration(auth.Interval) * time.Second
	if interval < minPollInterval {
		interval = defaultPollInterval
	}
	deadline := time.Now().Add(time.Duration(auth.ExpiresIn) * time.Second)
	if auth.ExpiresIn <= 0 {
		deadline = time.Now().Add(15 * time.Minute)
	}
	for {
		tokens, err := g.redeem(ctx, auth.DeviceCode)
		if err == nil {
			return tokens, nil
		}
		if errors.Is(err, errSlowDown) {
			interval += defaultPollInterval
		} else if !errors.Is(err, errAuthorizationPending) {
			return tokenSet{}, err
		}
		if !time.Now().Add(interval).Before(deadline) {
			return tokenSet{}, errExpiredToken
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return tokenSet{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// redeem exchanges a device code for tokens (RFC 8628 §3.4). While the user
// has not approved, it returns errAuthorizationPending or errSlowDown.
func (g *grantClient) redeem(ctx context.Context, deviceCode string) (tokenSet, error) {
	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
		"client_id":   {g.clientID},
	}
	return g.token(ctx, form)
}

// refresh exchanges a refresh token for a fresh access token (RFC 6749 §6).
// A rejected refresh token comes back as errAccessDenied, which tells the
// caller the grant is over and a fresh login is required.
func (g *grantClient) refresh(ctx context.Context, refreshToken string) (tokenSet, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {g.clientID},
	}
	return g.token(ctx, form)
}

// token posts one token request and classifies the RFC 8628 error responses.
func (g *grantClient) token(ctx context.Context, form url.Values) (tokenSet, error) {
	var resp tokenResponse
	if err := g.postForm(ctx, g.tokenEndpoint, form, &resp); err != nil {
		return tokenSet{}, err
	}
	// The endpoint answered but granted nothing. That is the user not having
	// approved yet, so the poll waits and tries again; a 5xx is not a verdict
	// on the grant either. Only an explicit error code ends it.
	if resp.Error == "" && resp.AccessToken == "" {
		return tokenSet{}, errAuthorizationPending
	}
	if resp.Error != "" {
		return tokenSet{}, classifyGrantError(resp.Error)
	}
	if resp.AccessToken == "" {
		return tokenSet{}, errors.New("the authorization server returned no access token")
	}
	return tokenSet{
		AccessToken:  resp.AccessToken,
		RefreshToken: resp.RefreshToken,
		TokenType:    resp.TokenType,
		ExpiresIn:    resp.ExpiresIn,
	}, nil
}

// classifyGrantError maps an authorization server error code onto the errors
// the polling loop acts on. Anything else rejects the grant.
func classifyGrantError(code string) error {
	switch code {
	case "authorization_pending":
		return errAuthorizationPending
	case "slow_down":
		return errSlowDown
	case "expired_token":
		return errExpiredToken
	case "access_denied", "invalid_grant":
		return errAccessDenied
	default:
		return fmt.Errorf("authorization server rejected the grant (%s)", code)
	}
}

// postForm sends one form-encoded authorization request and decodes its JSON
// body. The error it returns names the endpoint's failure, never the request
// body: that body carries the tokens and the device code.
func (g *grantClient) postForm(ctx context.Context, endpoint string, form url.Values, dest any) error {
	ctx, cancel := context.WithTimeout(ctx, grantTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build authorization request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := g.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(res.Body, bodyLimit))
	if err != nil {
		return fmt.Errorf("read authorization response: %w", err)
	}
	if len(payload) == 0 {
		if res.StatusCode >= http.StatusBadRequest {
			return core.NewProviderError(providerType, res.StatusCode, "authorization server returned an empty error response", nil)
		}
		return errors.New("authorization server returned an empty response")
	}
	if err := json.Unmarshal(payload, dest); err != nil {
		if res.StatusCode >= http.StatusBadRequest {
			return core.NewProviderError(providerType, res.StatusCode, "authorization server rejected the request", nil)
		}
		return errors.New("authorization server returned a response that is not JSON")
	}
	return nil
}

// deviceCodeResponse is the RFC 8628 device authorization response. Both URI
// spellings are read; servers differ on which one they send.
type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURL         string `json:"verification_url"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Error                   string `json:"error"`
}

// tokenResponse is the token endpoint response, covering both success and the
// error shape RFC 6749 defines.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}
