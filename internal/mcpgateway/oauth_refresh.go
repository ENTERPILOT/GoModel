package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/oauth2"

	"github.com/enterpilot/gomodel/internal/encryption"
)

// oauthLeaseTTL bounds a stored refresh lease: writing the lease, one token
// request, and the save. An instance that dies mid-refresh holds up others
// no longer.
const oauthLeaseTTL = 2*oauthSaveTimeout + oauthRefreshTimeout

// oauthLeasePoll is how often an instance waiting on another's refresh
// lease reads the stored record again.
const oauthLeasePoll = 200 * time.Millisecond

// oauthResaveInterval is how often a save that failed is tried again while
// the lease it was made under still holds.
const oauthResaveInterval = time.Second

// errOAuthSignInChanged means the stored sign-in is no longer the one this
// source holds tokens for: the next reload picks the new one up.
var errOAuthSignInChanged = errors.New("the OAuth sign-in changed under this request; retry shortly")

// renew returns a record with a usable access token other than rejected.
// Gateway instances sharing a store coordinate through the stored record,
// so a rotating refresh token is spent once: a token another instance
// already stored is adopted, a refresh another instance is running is
// waited for, and this instance refreshes only under a lease it wrote with
// a compare-and-swap. A store that cannot be read or leased fails the
// request rather than risk spending a refresh token twice; without a store
// at all, the instance refreshes on its own.
//
// A record is returned with errOAuthRejected too, when the server handed
// back the very access token the upstream refused: the grant, and with it a
// rotated refresh token, is the sign-in's current state even though the
// request cannot be retried.
func (o *oauthSource) renew(ctx context.Context, rejected string) (oauthRecord, error) {
	base := o.snapshot()
	if o.store == nil {
		if base.RefreshToken == "" {
			return oauthRecord{}, noRefreshToken(rejected)
		}
		next, err := o.exchange(ctx, base)
		if err != nil {
			return oauthRecord{}, err
		}
		return renewed(next, rejected)
	}
	deadline := time.Now().Add(oauthLeaseTTL)
	retried := false
	for {
		if time.Now().After(deadline) {
			return oauthRecord{}, errors.New("the stored OAuth token did not settle while this gateway instance waited to refresh it")
		}
		stored, err := o.lookup(ctx, base)
		if err != nil {
			return oauthRecord{}, err
		}
		if stored.UpdatedAt.Before(base.UpdatedAt) {
			// The store is behind this instance, as after a failed save:
			// bring it up to date rather than refresh with a spent token.
			base = o.save(ctx, stored, base, rejected)
		} else {
			base = stored
		}
		if base.usable(rejected) {
			return base, nil
		}
		if base.leasedElsewhere(o.owner, time.Now()) {
			if err := sleepCtx(ctx, oauthLeasePoll); err != nil {
				return oauthRecord{}, err
			}
			continue
		}
		if base.RefreshToken == "" {
			return oauthRecord{}, noRefreshToken(rejected)
		}
		leased, err := o.lease(ctx, base)
		if err != nil {
			return oauthRecord{}, err
		}
		if !leased {
			continue
		}
		next, err := o.exchange(ctx, base)
		if err == nil {
			return renewed(o.save(ctx, base, next, rejected), rejected)
		}
		o.release(ctx, base)
		// A lease that lapsed on a slow store lets another instance rotate
		// the refresh token meanwhile: try once with what it stored.
		if retried {
			return oauthRecord{}, err
		}
		if stored, lookupErr := o.lookup(ctx, base); lookupErr != nil || stored.RefreshToken == base.RefreshToken {
			return oauthRecord{}, err
		}
		retried = true
	}
}

// renewed returns record, with errOAuthRejected when its access token is the
// one the upstream refused.
func renewed(record oauthRecord, rejected string) (oauthRecord, error) {
	if rejected != "" && record.AccessToken == rejected {
		return record, errOAuthRejected
	}
	return record, nil
}

// exchange runs the refresh grant for base and returns the refreshed record.
func (o *oauthSource) exchange(ctx context.Context, base oauthRecord) (oauthRecord, error) {
	fresh, err := o.refresh(ctx, base)
	if err != nil {
		return oauthRecord{}, err
	}
	next := base.withLease("", time.Time{})
	next.AccessToken = fresh.AccessToken
	next.RefreshToken = fresh.RefreshToken
	next.TokenType = fresh.TokenType
	next.Expiry = fresh.Expiry
	next.UpdatedAt = time.Now().UTC()
	return next, nil
}

// noRefreshToken is the error for a token that cannot be renewed.
func noRefreshToken(rejected string) error {
	if rejected != "" {
		return errOAuthRejected
	}
	return errOAuthExpired
}

// sameTokens reports whether other is this sign-in with this refresh token.
func (r oauthRecord) sameTokens(other oauthRecord) bool {
	return r.sameSignIn(other) && r.RefreshToken == other.RefreshToken
}

// withLease returns r leased to owner until until; an empty owner clears
// the lease.
func (r oauthRecord) withLease(owner string, until time.Time) oauthRecord {
	r.RefreshOwner, r.RefreshUntil = owner, until
	return r
}

// lookup reads the stored record of this sign-in. The read is detached from
// the caller's cancellation like refresh, and bounded by oauthLookupTimeout.
func (o *oauthSource) lookup(ctx context.Context, base oauthRecord) (oauthRecord, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthLookupTimeout)
	defer cancel()
	stored, err := o.store.get(ctx, o.name)
	if errors.Is(err, ErrNotFound) {
		return oauthRecord{}, errOAuthSignInChanged
	}
	if err != nil {
		return oauthRecord{}, fmt.Errorf("read the stored OAuth tokens: %w", err)
	}
	if !stored.sameSignIn(base) {
		return oauthRecord{}, errOAuthSignInChanged
	}
	return *stored, nil
}

// write stores next while the stored record still satisfies same, detached
// from the caller's cancellation and bounded by oauthSaveTimeout. A record
// written but not confirmed re-sealed (encryption.ErrSealUnconfirmed) counts
// as written.
func (o *oauthSource) write(ctx context.Context, next oauthRecord, same func(stored oauthRecord) bool) (bool, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthSaveTimeout)
	defer cancel()
	saved, err := o.store.update(ctx, o.name, next, same)
	if errors.Is(err, encryption.ErrSealUnconfirmed) {
		err = nil
	}
	return saved, err
}

// lease stores base with this source's refresh lease while the stored
// record is still exactly base: same sign-in, same refresh token, same
// version, and no other instance's lease. It reports whether it wrote; a
// stored record that changed first is reported as not leased, so the caller
// reads it again.
func (o *oauthSource) lease(ctx context.Context, base oauthRecord) (bool, error) {
	leased, err := o.write(ctx, base.withLease(o.owner, time.Now().Add(oauthLeaseTTL)), func(stored oauthRecord) bool {
		return stored.sameTokens(base) && stored.UpdatedAt.Equal(base.UpdatedAt) && !stored.leasedElsewhere(o.owner, time.Now())
	})
	if err != nil {
		return false, fmt.Errorf("lease the OAuth token refresh: %w", err)
	}
	return leased, nil
}

// release drops this source's lease after a failed refresh, so other
// instances retry at once instead of waiting for it to lapse.
func (o *oauthSource) release(ctx context.Context, base oauthRecord) {
	_, _ = o.write(ctx, base.withLease("", time.Time{}), func(stored oauthRecord) bool {
		return stored.sameTokens(base) && stored.RefreshOwner == o.owner
	})
}

// supersedes is the predicate under which next, refreshed from base, may
// replace the stored record: it still belongs to this sign-in and holds
// base's refresh token, and no newer save landed. Another instance's lease
// does not block it: base's refresh token is spent, so that refresh fails,
// and the instance then adopts what was saved here.
func supersedes(base oauthRecord) func(stored oauthRecord) bool {
	return func(stored oauthRecord) bool {
		return stored.sameTokens(base) && !stored.UpdatedAt.After(base.UpdatedAt)
	}
}

// save stores next, refreshed from base, under supersedes(base): a sign-out,
// a newer sign-in, or another instance's refresh that landed meanwhile wins.
// It returns the record to use: next, or a usable token other than rejected
// that another instance stored first. A save the store refused is retried in
// the background, since base's refresh token is spent and must not be leased
// by another instance once this one's lease lapses.
func (o *oauthSource) save(ctx context.Context, base, next oauthRecord, rejected string) oauthRecord {
	saved, err := o.write(ctx, next, supersedes(base))
	switch {
	case err != nil:
		slog.Warn("failed to save a refreshed mcp oauth token; retrying in the background", "server", o.name, "error", err)
		go o.resave(context.WithoutCancel(ctx), base, next)
	case !saved:
		if stored, err := o.lookup(ctx, base); err == nil && stored.usable(rejected) {
			slog.Info("another gateway instance refreshed the mcp oauth token first; using its token", "server", o.name)
			return stored
		}
		slog.Info("mcp oauth sign-in or tokens changed while a token refresh ran; keeping the stored ones", "server", o.name)
	}
	return next
}

// resave retries a save the store refused, until it is written, the stored
// record moved on, or the lease it was made under has lapsed. ctx carries
// the request's values but not its cancellation.
func (o *oauthSource) resave(ctx context.Context, base, next oauthRecord) {
	deadline := time.Now().Add(oauthLeaseTTL)
	for time.Now().Before(deadline) {
		time.Sleep(oauthResaveInterval)
		saved, err := o.write(ctx, next, supersedes(base))
		if err != nil {
			continue
		}
		if saved {
			slog.Info("saved the refreshed mcp oauth token once the store answered again", "server", o.name)
		}
		return
	}
	slog.Warn("gave up saving a refreshed mcp oauth token; it keeps working in memory until the next refresh", "server", o.name)
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
