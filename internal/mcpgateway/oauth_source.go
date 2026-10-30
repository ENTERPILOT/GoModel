package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/enterpilot/gomodel/internal/encryption"
)

// OAuth sign-in states shown in admin views.
const (
	OAuthStatusSignedIn = "signed_in"
	OAuthStatusExpired  = "expired"
	OAuthStatusError    = "error"
)

// oauthSaveTimeout bounds persisting a refreshed token.
const oauthSaveTimeout = 10 * time.Second

// oauthRefreshTimeout bounds one token-endpoint request, so a stalled
// authorization server cannot hold the source's lock for a whole tool call.
const oauthRefreshTimeout = 15 * time.Second

// errOAuthExpired means the access token expired and no refresh token can
// renew it: an operator has to sign in again.
var errOAuthExpired = errors.New("the OAuth access token expired and the server issued no refresh token; sign in again from the dashboard")

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
type oauthSource struct {
	name       string
	store      *oauthStore
	httpClient *http.Client

	mu      sync.Mutex
	record  oauthRecord
	lastErr string
}

func newOAuthSource(name string, record oauthRecord, store *oauthStore, httpClient *http.Client) *oauthSource {
	return &oauthSource{name: name, record: record, store: store, httpClient: httpClient}
}

// sameSignIn reports whether record comes from the sign-in this source was
// built from, as opposed to a new one that needs a fresh upstream session.
func (o *oauthSource) sameSignIn(record oauthRecord) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.record.SignedInAt.Equal(record.SignedInAt) && o.record.ClientID == record.ClientID
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
	case o.record.RefreshToken == "" && !o.token().Valid():
		view.Status = OAuthStatusExpired
	}
	return view
}

// token returns the stored token; mu must be held.
func (o *oauthSource) token() *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  o.record.AccessToken,
		RefreshToken: o.record.RefreshToken,
		TokenType:    o.record.TokenType,
		Expiry:       o.record.Expiry,
	}
}

// accessToken returns a valid access token, refreshing it when it expired.
// Refreshes are serialized, so concurrent requests share one.
func (o *oauthSource) accessToken(ctx context.Context) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.token().Valid() {
		return o.record.AccessToken, nil
	}
	if o.record.RefreshToken == "" {
		o.lastErr = errOAuthExpired.Error()
		return "", errOAuthExpired
	}
	fresh, err := o.refresh(ctx, o.record)
	if err != nil && o.adoptStored(ctx) {
		// Another instance rotated the refresh token first: use its result.
		if o.token().Valid() {
			return o.record.AccessToken, nil
		}
		fresh, err = o.refresh(ctx, o.record)
	}
	if err != nil {
		o.lastErr = err.Error()
		return "", fmt.Errorf("refresh the OAuth token of mcp server %q: %w; sign in again from the dashboard if it persists", o.name, err)
	}
	o.record.AccessToken = fresh.AccessToken
	o.record.RefreshToken = fresh.RefreshToken
	o.record.TokenType = fresh.TokenType
	o.record.Expiry = fresh.Expiry
	o.record.UpdatedAt = time.Now().UTC()
	o.lastErr = ""
	o.persist(ctx)
	return o.record.AccessToken, nil
}

// adoptStored replaces the in-memory record with the stored one when that
// holds a different refresh token from the same sign-in; mu must be held.
func (o *oauthSource) adoptStored(ctx context.Context) bool {
	if o.store == nil {
		return false
	}
	stored, err := o.store.get(ctx, o.name)
	if err != nil || !stored.SignedInAt.Equal(o.record.SignedInAt) || stored.RefreshToken == o.record.RefreshToken {
		return false
	}
	o.record = *stored
	return true
}

// persist saves the refreshed token only while the stored record still
// belongs to this sign-in: a sign-out or newer sign-in that landed meanwhile,
// here or on another instance, wins, and the next reload drops this source.
// mu must be held.
func (o *oauthSource) persist(ctx context.Context) {
	if o.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthSaveTimeout)
	defer cancel()
	saved, err := o.store.update(ctx, o.name, o.record, func(stored oauthRecord) bool {
		return stored.SignedInAt.Equal(o.record.SignedInAt) && stored.ClientID == o.record.ClientID
	})
	switch {
	case err != nil && !errors.Is(err, encryption.ErrSealUnconfirmed):
		// The refreshed token keeps working in memory; only a restart loses it.
		slog.Warn("failed to save a refreshed mcp oauth token", "server", o.name, "error", err)
	case !saved && err == nil:
		slog.Info("mcp oauth sign-in changed while a token refresh ran; keeping the stored one", "server", o.name)
	}
}

// serverURL is the upstream URL this source's tokens were issued for.
func (o *oauthSource) serverURL() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.record.ServerURL
}

// refresh runs the refresh-token grant, sending the RFC 8707 resource
// indicator like the authorization and code exchange requests did.
func (o *oauthSource) refresh(ctx context.Context, record oauthRecord) (*oauth2.Token, error) {
	cfg := record.config("")
	ctx, cancel := context.WithTimeout(ctx, oauthRefreshTimeout)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, resourceClient(o.httpClient, record.TokenURL, record.Resource))
	token, err := cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: record.RefreshToken}).Token()
	if err != nil {
		return nil, oauthTokenError(err)
	}
	return token, nil
}

// config builds the oauth2 client configuration for token requests.
func (r oauthRecord) config(authURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     r.ClientID,
		ClientSecret: r.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   authURL,
			TokenURL:  r.TokenURL,
			AuthStyle: oauth2.AuthStyle(r.AuthStyle),
		},
		RedirectURL: r.RedirectURI,
		Scopes:      r.Scopes,
	}
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
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(clone)
}
