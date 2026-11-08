package mcpgateway

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// OAuth sign-in states shown in admin views.
const (
	OAuthStatusSignedIn = "signed_in"
	OAuthStatusExpired  = "expired"
	OAuthStatusError    = "error"
)

// oauthSaveTimeout bounds persisting a refreshed token.
const oauthSaveTimeout = 10 * time.Second

// oauthLookupTimeout bounds one read of the stored record around a refresh.
const oauthLookupTimeout = 3 * time.Second

// oauthRefreshTimeout bounds one token-endpoint request, so a stalled
// authorization server cannot hold up a tool call indefinitely.
const oauthRefreshTimeout = 15 * time.Second

// errOAuthExpired means the access token expired and no refresh token can
// renew it: an operator has to sign in again.
var errOAuthExpired = errors.New("the OAuth access token expired and the server issued no refresh token; sign in again from the dashboard")

// errOAuthRejected means the MCP server refused an unexpired access token
// and no refresh token can replace it: an operator has to sign in again.
var errOAuthRejected = errors.New("the mcp server rejected the OAuth access token and the server issued no refresh token; sign in again from the dashboard")

// OAuthView is the admin-facing sign-in state of one server. It never
// carries a token or client secret.
type OAuthView struct {
	Status     string
	Issuer     string
	Scopes     []string
	ExpiresAt  time.Time
	SignedInAt time.Time
	LastError  string
}

// oauthSource hands out access tokens for one signed-in server. It refreshes
// an expired token with the refresh token and persists the result, because
// authorization servers rotate refresh tokens for public clients.
//
// mu guards record and lastErr and is never held across I/O, so admin views
// and reloads never wait for a refresh. refreshing admits one refresh at a
// time: callers that waited for it reuse its result.
type oauthSource struct {
	name       string
	store      *oauthStore
	httpClient *http.Client
	// owner names this source in the stored refresh lease.
	owner      string
	refreshing chan struct{}

	mu      sync.Mutex
	record  oauthRecord
	lastErr string
}

func newOAuthSource(name string, record oauthRecord, store *oauthStore, httpClient *http.Client) *oauthSource {
	return &oauthSource{
		name:       name,
		record:     record,
		store:      store,
		httpClient: httpClient,
		owner:      rand.Text(),
		refreshing: make(chan struct{}, 1),
	}
}

// sameSignIn reports whether record comes from the sign-in this source was
// built from, as opposed to a new one that needs a fresh upstream session.
func (o *oauthSource) sameSignIn(record oauthRecord) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.record.sameSignIn(record)
}

// adopt takes tokens another instance refreshed and stored after this one
// last did, so a rotated refresh token is not reused here.
func (o *oauthSource) adopt(record oauthRecord) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if record.UpdatedAt.After(o.record.UpdatedAt) {
		o.record = record
		o.lastErr = ""
	}
}

func (o *oauthSource) view() *OAuthView {
	o.mu.Lock()
	defer o.mu.Unlock()
	view := &OAuthView{
		Status:     OAuthStatusSignedIn,
		Issuer:     o.record.Issuer,
		Scopes:     slices.Clone(o.record.Scopes),
		ExpiresAt:  o.record.Expiry,
		SignedInAt: o.record.SignedInAt,
		LastError:  o.lastErr,
	}
	switch {
	case o.lastErr != "":
		view.Status = OAuthStatusError
	case o.record.RefreshToken == "" && !o.record.token().Valid():
		view.Status = OAuthStatusExpired
	}
	return view
}

// token returns the record's token set.
func (r oauthRecord) token() *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		TokenType:    r.TokenType,
		Expiry:       r.Expiry,
	}
}

// usable reports whether r holds a valid access token other than rejected.
func (r oauthRecord) usable(rejected string) bool {
	return r.token().Valid() && (rejected == "" || r.AccessToken != rejected)
}

// accessToken returns a valid access token, refreshing it when it expired.
func (o *oauthSource) accessToken(ctx context.Context) (string, error) {
	return o.tokenOtherThan(ctx, "")
}

// tokenOtherThan returns a valid access token other than rejected, one the
// upstream refused before it expired: a token another request or instance
// obtained meanwhile, else a refreshed one. An empty rejected only refreshes
// an expired token.
func (o *oauthSource) tokenOtherThan(ctx context.Context, rejected string) (string, error) {
	if token, ok := o.current(rejected); ok {
		return token, nil
	}
	// A canceled caller still gets an idle slot: its refresh is detached.
	select {
	case o.refreshing <- struct{}{}:
	default:
		select {
		case o.refreshing <- struct{}{}:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	defer func() { <-o.refreshing }()
	// A refresh that finished while this caller waited is shared.
	if token, ok := o.current(rejected); ok {
		return token, nil
	}
	record, err := o.renew(ctx, rejected)
	o.mu.Lock()
	defer o.mu.Unlock()
	if err != nil {
		o.lastErr = err.Error()
		if errors.Is(err, errOAuthExpired) || errors.Is(err, errOAuthRejected) {
			return "", err
		}
		return "", fmt.Errorf("refresh the OAuth token of mcp server %q: %w; sign in again from the dashboard if it persists", o.name, err)
	}
	// A reload may have adopted a newer stored token meanwhile.
	if !o.record.UpdatedAt.After(record.UpdatedAt) {
		o.record = record
	}
	o.lastErr = ""
	return record.AccessToken, nil
}

// current returns the in-memory access token when it is usable.
func (o *oauthSource) current(rejected string) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.record.usable(rejected) {
		return o.record.AccessToken, true
	}
	return "", false
}

// snapshot copies the in-memory record.
func (o *oauthSource) snapshot() oauthRecord {
	o.mu.Lock()
	defer o.mu.Unlock()
	record := o.record
	record.Scopes = slices.Clone(record.Scopes)
	return record
}

// serverURL is the upstream URL this source's tokens were issued for.
func (o *oauthSource) serverURL() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.record.ServerURL
}

// oauthTokenError keeps the OAuth error code and description of a failed
// token request and drops the raw response body the oauth2 package quotes.
func oauthTokenError(err error) error {
	if retrieve, ok := errors.AsType[*oauth2.RetrieveError](err); ok {
		if retrieve.ErrorCode != "" {
			if retrieve.ErrorDescription != "" {
				return fmt.Errorf("token endpoint returned %s: %s", retrieve.ErrorCode, truncateOAuthText(retrieve.ErrorDescription))
			}
			return fmt.Errorf("token endpoint returned %s", retrieve.ErrorCode)
		}
		if retrieve.Response != nil {
			return fmt.Errorf("token endpoint returned HTTP %d", retrieve.Response.StatusCode)
		}
		return errors.New("token request failed")
	}
	return redactDialError(err)
}

func truncateOAuthText(text string) string {
	const limit = 200
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}

// bearerRoundTripper sets the OAuth access token on every request to the
// server's origin. The static-header transport wraps it, so a signed-in
// token replaces a configured Authorization header.
type bearerRoundTripper struct {
	base   http.RoundTripper
	source *oauthSource
	origin string
}

func (t *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.origin == "" || requestOrigin(req.URL.String()) != t.origin {
		return t.base.RoundTrip(req)
	}
	token, err := t.source.accessToken(req.Context())
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	resp, err := t.base.RoundTrip(withBearer(req, token))
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	// The server refused a token that had not expired, such as one revoked
	// early or issued without expires_in: refresh it and retry once. A failed
	// refresh shows on the server's sign-in state, and the 401 goes back.
	fresh, err := t.source.tokenOtherThan(req.Context(), token)
	if err != nil {
		return resp, nil
	}
	retry, ok := rewindRequest(req)
	if !ok {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
	return t.base.RoundTrip(withBearer(retry, fresh))
}

func withBearer(req *http.Request, token string) *http.Request {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+token)
	return clone
}

// rewindRequest returns a copy of req that can be sent again after req was:
// one without a body, or with a body GetBody can produce again.
func rewindRequest(req *http.Request) (*http.Request, bool) {
	if req.Body == nil || req.Body == http.NoBody {
		return req, true
	}
	if req.GetBody == nil {
		return nil, false
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	clone := req.Clone(req.Context())
	clone.Body = body
	return clone, true
}
