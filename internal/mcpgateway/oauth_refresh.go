package mcpgateway

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"golang.org/x/oauth2"

	"github.com/enterpilot/gomodel/internal/encryption"
)

// oauthLeaseTTL bounds a stored refresh lease: one token request plus the
// save. An instance that dies mid-refresh holds up others no longer.
const oauthLeaseTTL = oauthRefreshTimeout + oauthSaveTimeout

// oauthLeasePoll is how often an instance waiting on another's refresh
// lease reads the stored record again.
const oauthLeasePoll = 200 * time.Millisecond

// renew returns a record with a usable access token other than rejected.
// Gateway instances sharing a store coordinate through the stored record,
// so a rotating refresh token is spent once: a token another instance
// already stored is adopted, a refresh another instance is running is
// waited for, and this instance refreshes only under a lease it wrote with
// a compare-and-swap. Without a readable store it refreshes on its own.
func (o *oauthSource) renew(ctx context.Context, rejected string) (oauthRecord, error) {
	base := o.snapshot()
	deadline := time.Now().Add(oauthLeaseTTL)
	retried := false
	for {
		leased := false
		if stored, ok := o.lookup(ctx, base); ok {
			if stored.usable(rejected) {
				return stored, nil
			}
			base = stored
			if stored.leasedElsewhere(o.owner, time.Now()) {
				if time.Now().After(deadline) {
					return oauthRecord{}, errors.New("another gateway instance is still refreshing the token")
				}
				if err := sleepCtx(ctx, oauthLeasePoll); err != nil {
					return oauthRecord{}, err
				}
				continue
			}
			if base.RefreshToken != "" {
				var conflict bool
				leased, conflict = o.lease(ctx, &base)
				if conflict && time.Now().Before(deadline) {
					continue
				}
			}
		}
		if base.RefreshToken == "" {
			if rejected != "" {
				return oauthRecord{}, errOAuthRejected
			}
			return oauthRecord{}, errOAuthExpired
		}
		fresh, err := o.refresh(ctx, base)
		if err != nil {
			if leased {
				o.release(ctx, base)
			}
			// Another instance may have rotated the refresh token without
			// a lease, such as while the store was unreachable: retry once
			// with what it stored.
			if stored, ok := o.lookup(ctx, base); ok && !retried && stored.RefreshToken != base.RefreshToken {
				retried = true
				base = stored
				continue
			}
			return oauthRecord{}, err
		}
		next := base
		next.AccessToken = fresh.AccessToken
		next.RefreshToken = fresh.RefreshToken
		next.TokenType = fresh.TokenType
		next.Expiry = fresh.Expiry
		next.UpdatedAt = time.Now().UTC()
		next.RefreshOwner = ""
		next.RefreshUntil = time.Time{}
		return o.save(ctx, base, next), nil
	}
}

// lookup reads the stored record of this sign-in. It reports false without
// a store, on a read error, and when the stored record belongs to another
// sign-in or none. The read is detached from the caller's cancellation like
// refresh, and bounded by oauthLookupTimeout.
func (o *oauthSource) lookup(ctx context.Context, base oauthRecord) (oauthRecord, bool) {
	if o.store == nil {
		return oauthRecord{}, false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthLookupTimeout)
	defer cancel()
	stored, err := o.store.get(ctx, o.name)
	if err != nil || !stored.sameSignIn(base) {
		return oauthRecord{}, false
	}
	return *stored, true
}

// lease stores base with this source's refresh lease while the stored
// record still holds base's refresh token and no other lease. It reports
// whether it wrote, and conflict when the stored record changed first. A
// store error leaves the refresh unleased, so a store hiccup never blocks
// tokens.
func (o *oauthSource) lease(ctx context.Context, base *oauthRecord) (leased, conflict bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthSaveTimeout)
	defer cancel()
	next := *base
	next.RefreshOwner = o.owner
	next.RefreshUntil = time.Now().Add(oauthLeaseTTL)
	saved, err := o.store.update(ctx, o.name, next, func(stored oauthRecord) bool {
		return stored.sameSignIn(*base) && stored.RefreshToken == base.RefreshToken &&
			!stored.leasedElsewhere(o.owner, time.Now())
	})
	if err != nil && !errors.Is(err, encryption.ErrSealUnconfirmed) {
		slog.Warn("failed to lease an mcp oauth token refresh; refreshing without it", "server", o.name, "error", err)
		return false, false
	}
	if !saved {
		return false, true
	}
	*base = next
	return true, false
}

// release drops this source's lease after a failed refresh, so other
// instances retry at once instead of waiting for it to lapse.
func (o *oauthSource) release(ctx context.Context, base oauthRecord) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthSaveTimeout)
	defer cancel()
	next := base
	next.RefreshOwner = ""
	next.RefreshUntil = time.Time{}
	_, _ = o.store.update(ctx, o.name, next, func(stored oauthRecord) bool {
		return stored.sameSignIn(base) && stored.RefreshToken == base.RefreshToken && stored.RefreshOwner == o.owner
	})
}

// save stores next, refreshed from base, only while the stored record still
// belongs to this sign-in and holds base's refresh token: a sign-out, a
// newer sign-in, or another instance's refresh that landed meanwhile wins.
// It returns the record to use: next, or the token another instance stored
// first.
func (o *oauthSource) save(ctx context.Context, base, next oauthRecord) oauthRecord {
	if o.store == nil {
		return next
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthSaveTimeout)
	defer cancel()
	saved, err := o.store.update(saveCtx, o.name, next, func(stored oauthRecord) bool {
		return stored.sameSignIn(base) && stored.RefreshToken == base.RefreshToken &&
			!stored.UpdatedAt.After(base.UpdatedAt)
	})
	switch {
	case err != nil && !errors.Is(err, encryption.ErrSealUnconfirmed):
		// The refreshed token keeps working in memory; only a restart loses it.
		slog.Warn("failed to save a refreshed mcp oauth token", "server", o.name, "error", err)
	case !saved && err == nil:
		if stored, ok := o.lookup(ctx, base); ok && stored.usable("") {
			slog.Info("another gateway instance refreshed the mcp oauth token first; using its token", "server", o.name)
			return stored
		}
		slog.Info("mcp oauth sign-in or tokens changed while a token refresh ran; keeping the stored ones", "server", o.name)
	}
	return next
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// refresh runs the refresh-token grant, sending the RFC 8707 resource
// indicator like the authorization and code exchange requests did. It is
// detached from the caller's cancellation and bounded by oauthRefreshTimeout
// instead: the authorization server can consume the refresh token before a
// cancellation reaches it, and dropping the rotated one would force a new
// sign-in.
func (o *oauthSource) refresh(ctx context.Context, record oauthRecord) (*oauth2.Token, error) {
	cfg := record.config("")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthRefreshTimeout)
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
