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
	// exchangeTimeout bounds one exchange, which runs detached from the
	// request that triggered it.
	exchangeTimeout = 30 * time.Second
	// defaultLifetime is assumed when the endpoint returns no expiry.
	defaultLifetime = 5 * time.Minute
)

// Source hands out a cached provider access token, exchanging the identity
// token for a new one ahead of expiry. It is safe for concurrent use.
//
// No exchange runs under the lock. While the cached token is still usable,
// callers get it immediately and one refresh runs in the background. Callers
// without a usable token wait for the shared exchange, and each stops waiting
// when its own context ends.
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
	// inflight is closed when the running exchange finishes; nil when none
	// is running.
	inflight chan struct{}
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

// Token returns a valid access token. When the cached token is due for
// refresh but still usable, it is returned while a refresh runs in the
// background. Otherwise Token waits for an exchange, or returns the last
// exchange error while failures are backing off.
func (s *Source) Token(ctx context.Context) (string, error) {
	for {
		s.mu.Lock()
		now := s.now()
		if s.token != "" && now.Before(s.refreshAt) {
			token := s.token
			s.mu.Unlock()
			return token, nil
		}
		if s.inflight == nil && !now.Before(s.retryAt) {
			s.inflight = make(chan struct{})
			go s.refresh(context.WithoutCancel(ctx), s.inflight)
		}
		if s.token != "" && now.Before(s.staleAt) {
			token := s.token
			s.mu.Unlock()
			return token, nil
		}
		done, lastErr := s.inflight, s.lastErr
		s.mu.Unlock()
		if done == nil {
			return "", lastErr
		}
		select {
		case <-done:
			// Re-evaluate against the outcome and the current time.
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// refresh runs one exchange and records the outcome. parent carries the
// triggering request's values but not its cancellation, so a caller that
// gives up does not fail the shared exchange.
func (s *Source) refresh(parent context.Context, done chan struct{}) {
	ctx, cancel := context.WithTimeout(parent, exchangeTimeout)
	defer cancel()
	minted, err := s.exchange(ctx)

	s.mu.Lock()
	servingCached := false
	if err != nil {
		s.lastErr = err
		s.retryAt = s.now().Add(failureBackoff)
		servingCached = s.token != "" && s.now().Before(s.staleAt)
	} else {
		s.store(minted)
	}
	s.inflight = nil
	close(done)
	s.mu.Unlock()

	if err != nil {
		slog.Warn("workload identity token exchange failed",
			"provider", s.cfg.Provider,
			"endpoint", s.cfg.TokenEndpoint,
			"serving_cached_token", servingCached,
			"error", err)
	}
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
