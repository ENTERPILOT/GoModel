package workloadidentity

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	// refreshAhead is how long before expiry a cached token is replaced. A
	// failed refresh inside this window keeps serving the cached token.
	refreshAhead = 2 * time.Minute
	// staleAhead is how long before expiry a cached token stops being served,
	// so a request never leaves with a token about to expire in flight.
	staleAhead = 30 * time.Second
	// failureBackoff spaces out exchanges after a failure, so a broken token
	// endpoint or a rejected identity token is not hit by every request.
	failureBackoff = 5 * time.Second
	// exchangeTimeout bounds one exchange independently of the request that
	// triggered it, so a cancelled caller does not fail the shared refresh.
	exchangeTimeout = 30 * time.Second
	// defaultLifetime is assumed when the endpoint returns no expiry.
	defaultLifetime = 5 * time.Minute
)

// Source hands out a cached provider access token, exchanging the identity
// token for a new one ahead of expiry. It is safe for concurrent use;
// concurrent callers share one exchange.
type Source struct {
	cfg    Config
	client *http.Client
	now    func() time.Time

	mu        sync.Mutex
	token     string
	refreshAt time.Time // replace the token from here on
	staleAt   time.Time // stop serving the token from here on
	lastErr   error
	retryAt   time.Time
}

// New validates cfg and returns a Source that exchanges through client. No
// exchange happens until the first Token call.
func New(cfg Config, client *http.Client) (*Source, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Source{cfg: cfg, client: client, now: time.Now}, nil
}

// Token returns a valid access token, exchanging for a new one when the
// cached token is due for refresh. When a refresh fails but the cached token
// is still safely valid, the cached token is returned.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if s.token != "" && now.Before(s.refreshAt) {
		return s.token, nil
	}
	usable := s.token != "" && now.Before(s.staleAt)
	if now.Before(s.retryAt) {
		if usable {
			return s.token, nil
		}
		return "", s.lastErr
	}

	exchangeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), exchangeTimeout)
	defer cancel()
	minted, err := s.exchange(exchangeCtx)
	if err != nil {
		s.lastErr = err
		s.retryAt = s.now().Add(failureBackoff)
		slog.Warn("workload identity token exchange failed",
			"provider", s.cfg.Provider,
			"endpoint", s.cfg.TokenEndpoint,
			"serving_cached_token", usable,
			"error", err)
		if usable {
			return s.token, nil
		}
		return "", err
	}

	s.store(minted)
	return s.token, nil
}

// store caches minted, scaling the refresh margins down for tokens whose
// whole lifetime is shorter than the default margins.
func (s *Source) store(minted mintedToken) {
	now := s.now()
	expiry := minted.expiry
	if expiry.IsZero() {
		expiry = now.Add(defaultLifetime)
	}
	lifetime := expiry.Sub(now)
	s.token = minted.accessToken
	s.refreshAt = expiry.Add(-min(refreshAhead, lifetime/2))
	s.staleAt = expiry.Add(-min(staleAhead, lifetime/4))
	s.lastErr = nil
	s.retryAt = time.Time{}
}
