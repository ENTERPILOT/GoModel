package oauthlogin

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/providers"
)

// loginRequiredMessage is the fixed text reported when the refresh token is
// rejected. It names no token and no endpoint, so it is safe to log and to
// return to clients.
const loginRequiredMessage = "oauthlogin refresh token was rejected; sign in again with a device login"

// errLoginRequired is the sentinel a rejected refresh token surfaces. It
// wraps nothing from the authorization server, whose error can quote the
// request.
var errLoginRequired = errors.New(loginRequiredMessage)

// refreshSkew renews an access token this long before its recorded expiry,
// so a request never leaves with a token that expires in transit.
const refreshSkew = 30 * time.Second

// credentialWriter persists a provider credential row. *providers.CredentialsService
// satisfies it, which is how a completed login reaches the credential store
// the rest of the gateway already uses.
type credentialWriter interface {
	Get(ctx context.Context, name string) (*providers.ManagedProviderCredential, error)
	Upsert(ctx context.Context, cred providers.ManagedProviderCredential) error
}

// storedSession is the login state persisted inside the credential's secret
// field. It is the only place tokens are written, and it is sealed at rest by
// the credential store like every other provider secret.
type storedSession struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry"`
	Account      string    `json:"account"`
	ClientID     string    `json:"client_id"`
}

// session is the live login state of one provider instance: the tokens, and
// the single-flight refresh that keeps them current.
type session struct {
	mu        sync.Mutex
	current   storedSession
	grant     *grantClient
	persister sessionPersister
	now       func() time.Time
	// refreshing is the refresh already in flight, shared by every request
	// that finds the access token expired. Double-checked under mu, so
	// concurrent callers wait for one refresh instead of each spending the
	// refresh token.
	refreshing chan struct{}
}

// sessionPersister stores a rotated token set. A nil persister keeps the
// rotation in memory only, which is all an instance built without a
// credential name can do.
type sessionPersister func(ctx context.Context, updated storedSession) error

// newSession returns the session for one provider instance. grant performs
// the refresh; persister records a rotated token set.
func newSession(initial storedSession, grant *grantClient, persister sessionPersister) *session {
	return &session{current: initial, grant: grant, persister: persister, now: time.Now}
}

// accessToken returns a usable access token, refreshing it first when it is
// expired. A rejected refresh token is not retried and never falls back to
// another credential: the caller is told a fresh login is required.
func (s *session) accessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	if token, ok := s.usable(); ok {
		s.mu.Unlock()
		return token, nil
	}
	if s.current.RefreshToken == "" {
		s.mu.Unlock()
		return "", errLoginRequired
	}
	wait := s.refreshing
	if wait == nil {
		wait = make(chan struct{})
		s.refreshing = wait
		go s.refresh(wait)
	}
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-wait:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if token, ok := s.usable(); ok {
		return token, nil
	}
	return "", errLoginRequired
}

// signedIn reports whether a device login has produced tokens. It is what
// distinguishes "fall back to the pasted key" from "this login is the only
// credential": once a login exists, a rejected refresh never falls back.
func (s *session) signedIn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current.RefreshToken != "" || s.current.AccessToken != ""
}

// usable reports the current access token when it is still valid. The caller
// holds s.mu.
func (s *session) usable() (string, bool) {
	token := strings.TrimSpace(s.current.AccessToken)
	if token == "" {
		return "", false
	}
	if s.current.Expiry.IsZero() || s.now().Add(refreshSkew).Before(s.current.Expiry) {
		return token, true
	}
	return "", false
}

// refresh spends the refresh token once and publishes the result to everyone
// waiting on done. It keeps the previous refresh token when the server did
// not rotate it.
func (s *session) refresh(done chan struct{}) {
	defer close(done)
	s.mu.Lock()
	refreshToken := s.current.RefreshToken
	s.mu.Unlock()

	// Detached from the caller: a client disconnect must not abandon a
	// refresh whose rotated token still has to be stored.
	ctx, cancel := context.WithTimeout(context.Background(), grantTimeout)
	defer cancel()
	tokens, err := s.grant.refresh(ctx, refreshToken)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshing = nil
	if err != nil || s.current.RefreshToken != refreshToken {
		// A rejected grant drops the access token, so the next request
		// reports the login as required instead of retrying a dead token. A
		// refresh token that changed under us belongs to a newer login, and
		// this result must not overwrite it.
		if err != nil && s.current.RefreshToken == refreshToken {
			s.current.AccessToken = ""
			s.current.Expiry = time.Time{}
		}
		return
	}
	updated := s.current
	updated.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		updated.RefreshToken = tokens.RefreshToken
	}
	if tokens.TokenType != "" {
		updated.TokenType = tokens.TokenType
	}
	updated.Expiry = expiryFrom(s.now(), tokens.ExpiresIn)
	s.current = updated
	if s.persister != nil {
		_ = s.persister(context.Background(), updated)
	}
}

// replace swaps the session for one a fresh login produced.
func (s *session) replace(next storedSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = next
}

// expiryFrom converts a token response's expires_in into an absolute expiry.
// Zero means the server stated no lifetime, which is recorded as no expiry.
func expiryFrom(now time.Time, expiresIn int) time.Time {
	if expiresIn <= 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(expiresIn) * time.Second)
}

// parseSession decodes the persisted login state. A blank value is a session
// that has not signed in yet, not an error.
func parseSession(raw string) (storedSession, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return storedSession{}, nil
	}
	var session storedSession
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		return storedSession{}, errors.New("oauthlogin credential does not hold a device-login session")
	}
	return session, nil
}

// encodeSession serializes a login for the credential store.
func encodeSession(session storedSession) (string, error) {
	encoded, err := json.Marshal(session)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// persistSession writes session into the provider credential's secret field,
// preserving every other field of the stored row. The credential store seals
// that field, so the tokens get the same encryption at rest as an API key.
func persistSession(ctx context.Context, store credentialWriter, providerName string, session storedSession) error {
	if store == nil || strings.TrimSpace(providerName) == "" {
		return nil
	}
	row, err := store.Get(ctx, providerName)
	if err != nil {
		return err
	}
	encoded, err := encodeSession(session)
	if err != nil {
		return err
	}
	row.ServiceAccountJSON = encoded
	return store.Upsert(ctx, *row)
}
