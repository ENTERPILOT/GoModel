package mcpgateway

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/encryption/encryptiontest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// expiredRecordFor returns a stored sign-in whose access token expired, with
// token requests going to as.
func expiredRecordFor(as *fakeAuthServer) oauthRecord {
	record := testOAuthRecord()
	record.TokenURL = as.URL + "/token"
	record.Expiry = time.Now().Add(-time.Minute)
	as.mu.Lock()
	as.resource = record.Resource
	as.refreshToken = record.RefreshToken
	as.mu.Unlock()
	return record
}

func TestOAuthRefreshOutlivesCanceledRequest(t *testing.T) {
	as := newFakeAuthServer(t, false)
	source := newOAuthSource("notion", expiredRecordFor(as), nil, as.Client())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	token, err := source.accessToken(ctx)
	require.NoError(t, err, "a canceled caller does not lose the rotated refresh token")
	assert.Equal(t, "access-2", token)
	assert.Equal(t, "refresh-2", source.record.RefreshToken)
}

func TestOAuthAdoptedTokenClearsRefreshError(t *testing.T) {
	as := newFakeAuthServer(t, false)
	record := expiredRecordFor(as)
	store, err := NewSQLStore(context.Background(), sqlxtest.NewSQLite(t))
	require.NoError(t, err)
	box, _ := encryptiontest.NewBox(t)
	oauth := newOAuthStore(store, box)
	ctx := context.Background()

	// Another instance rotated the refresh token and stored a valid token.
	stored := record
	stored.AccessToken = "adopted"
	stored.RefreshToken = "rotated-elsewhere"
	stored.Expiry = time.Now().Add(time.Hour)
	require.NoError(t, oauth.put(ctx, "notion", stored))
	as.mu.Lock()
	as.refreshToken = "rotated-elsewhere"
	as.mu.Unlock()

	source := newOAuthSource("notion", record, oauth, as.Client())
	source.lastErr = "an earlier refresh failed"

	token, err := source.accessToken(ctx)
	require.NoError(t, err)
	assert.Equal(t, "adopted", token)
	view := source.view()
	assert.Equal(t, OAuthStatusSignedIn, view.Status)
	assert.Empty(t, view.LastError)
}

func TestOAuthRefreshStartsFromNewerStoredTokens(t *testing.T) {
	as := newFakeAuthServer(t, false)
	record := expiredRecordFor(as)
	store, err := NewSQLStore(context.Background(), sqlxtest.NewSQLite(t))
	require.NoError(t, err)
	box, _ := encryptiontest.NewBox(t)
	oauth := newOAuthStore(store, box)
	ctx := context.Background()

	// Another instance already refreshed and stored a valid token.
	stored := record
	stored.AccessToken = "from-elsewhere"
	stored.RefreshToken = "rotated-elsewhere"
	stored.Expiry = time.Now().Add(time.Hour)
	stored.UpdatedAt = record.UpdatedAt.Add(time.Minute)
	require.NoError(t, oauth.put(ctx, "notion", stored))

	source := newOAuthSource("notion", record, oauth, as.Client())
	token, err := source.accessToken(ctx)
	require.NoError(t, err)
	assert.Equal(t, "from-elsewhere", token)
	as.mu.Lock()
	defer as.mu.Unlock()
	assert.Zero(t, as.refreshes, "the consumed refresh token is never sent again")
}

func TestOAuthRefreshNeverUndoesAnotherSignIn(t *testing.T) {
	store, err := NewSQLStore(context.Background(), sqlxtest.NewSQLite(t))
	require.NoError(t, err)
	box, _ := encryptiontest.NewBox(t)
	oauth := newOAuthStore(store, box)
	ctx := context.Background()

	record := testOAuthRecord()
	source := newOAuthSource("notion", record, oauth, nil)
	refreshed := func() {
		source.mu.Lock()
		defer source.mu.Unlock()
		source.record.AccessToken = "refreshed"
		source.persist(ctx, record.UpdatedAt)
	}

	// Same sign-in: the refreshed token is saved.
	require.NoError(t, oauth.put(ctx, "notion", record))
	refreshed()
	stored, err := oauth.get(ctx, "notion")
	require.NoError(t, err)
	assert.Equal(t, "refreshed", stored.AccessToken)

	// Another instance's later refresh of the same sign-in is kept.
	elsewhere := record
	elsewhere.AccessToken = "elsewhere"
	elsewhere.UpdatedAt = record.UpdatedAt.Add(time.Minute)
	require.NoError(t, oauth.put(ctx, "notion", elsewhere))
	refreshed()
	stored, err = oauth.get(ctx, "notion")
	require.NoError(t, err)
	assert.Equal(t, "elsewhere", stored.AccessToken)

	// A newer sign-in is kept.
	newer := testOAuthRecord()
	newer.SignedInAt = record.SignedInAt.Add(time.Minute)
	newer.AccessToken = "newer"
	require.NoError(t, oauth.put(ctx, "notion", newer))
	refreshed()
	stored, err = oauth.get(ctx, "notion")
	require.NoError(t, err)
	assert.Equal(t, "newer", stored.AccessToken)

	// A sign-out is not undone.
	require.NoError(t, oauth.delete(ctx, "notion"))
	refreshed()
	_, err = oauth.get(ctx, "notion")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestOAuthSourceReportsExpiredWithoutRefreshToken(t *testing.T) {
	record := testOAuthRecord()
	record.RefreshToken = ""
	record.Expiry = time.Now().Add(-time.Minute)
	source := newOAuthSource("notion", record, nil, nil)
	assert.Equal(t, OAuthStatusExpired, source.view().Status)
	_, err := source.accessToken(context.Background())
	require.ErrorIs(t, err, errOAuthExpired)
	assert.Equal(t, OAuthStatusError, source.view().Status)
}
